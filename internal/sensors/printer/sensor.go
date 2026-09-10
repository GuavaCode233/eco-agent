// 本檔實作路徑 B 的感測器 Run 迴圈（CLAUDE.md Step 3.2–3.4）。
//
// 感測模式（3.2）：page counter 為累計狀態量、無推播，只能輪詢。沿用 Step 2 的觸發模型
// （關鍵不可違反：不用絕對計時器）：
//   - 掛 checkInterval（60 秒巡檢），與路徑 C 同一條巡檢節奏；
//   - 每次巡檢以持久化時間戳 lastPrinterPollAt 判斷 now-lastPrinterPollAt >= printerPollInterval
//     才查、入列、更新時間戳；時間戳不存在（冷啟動）或無法解析視為「已到期」；
//   - 查詢／入列失敗不更新時間戳，下次巡檢自然重試（不設獨立重試計時器、不指數退避）。
//
// 送出（3.3，v0.23 [D15] 修訂）：Agent 純感測、只送原始量——payload
// {usage_date, printer_page_counter, printer_serial}。printer_page_counter 為 SNMP 讀到的
// **壽命累計絕對值，原樣上送、不在本機相減**：區間差分與 counter 重置防呆全部移至後端
// （後端以「當日最新讀數－前一日最新讀數」求得頁數）。故本機不再需要任何跨重啟的差分
// 基準狀態——每次到期輪詢讀到什麼就送什麼，upsert 覆蓋同一筆事件即完成「最新讀數勝出」
// （多觀測者讀到的是同一台印表機的同一個絕對值，collected_at 最新者勝的規則才成立）。
// printer_serial 供 [D14] 缺口二的 per-printer 歸鍵用；查無序號時省略該欄位，由後端以
// device_id 回退歸鍵並標記「印表機身份不明」。能耗換算全在後端，Agent 不算也不送係數。
// 走 HTTPS（v20 §4.4 [D13] 起三路徑一律 HTTPS，由 uploader 統一送出；現階段 mock 端點）。
//
// BYOD 摩擦點（3.4）：SNMP 需與印表機同網段。啟動時的首次巡檢即為連通性檢查——不通則
// 記 log 跳過，Run 不阻塞、不回錯誤，其餘路徑照常運作。
package printer

import (
	"context"
	"log/slog"
	"time"

	"eco-agent/internal/queue"
)

// StateKeyLastPoll 為上次輪詢時間戳，供 3.2 到期判斷（對應路徑 C 的 lastDriveQuotaCheckAt）。
// 與佇列同一份 SQLite，跨重啟保留。
const StateKeyLastPoll = "lastPrinterPollAt"

// idTokenProvider 抽象「取員工 ID Token」，由 *enroll.Enroller 滿足（與路徑 A/C 一致）。
type idTokenProvider interface {
	IDToken() (string, error)
}

// Sensor 是路徑 B 印表機感測器：掛 checkInterval 巡檢，到期時查 page counter 與序號、
// 原樣入列（差分與歸鍵防呆皆在後端，見 [D14]／[D15]）。
type Sensor struct {
	q       *queue.Queue
	enr     idTokenProvider
	sampler PageCounterSampler

	checkInterval time.Duration // 巡檢節奏（config.CheckInterval，60 秒）
	pollInterval  time.Duration // 到期門檻（config.PrinterPollInterval，暫定 300 秒）

	log *slog.Logger
	now func() time.Time

	// failures 為連續查詢失敗次數；僅用來控制 log 音量（BYOD 常態離線時不刷版）。
	failures int
	// serialFailures 為連續取序號失敗次數，獨立計數（page counter 可能查得到、序號查不到，
	// 兩者互不影響彼此的 log 音量控制）。
	serialFailures int
}

// SensorOption 以函式選項調整 Sensor（對齊 sensors/computer、sensors/drive 慣例）。
type SensorOption func(*Sensor)

// WithSensorLogger 設定日誌器；預設 slog.Default()。
func WithSensorLogger(l *slog.Logger) SensorOption {
	return func(s *Sensor) {
		if l != nil {
			s.log = l
		}
	}
}

// WithSensorNow 注入時間函式（測試用，控制到期判斷與跨日）。
func WithSensorNow(now func() time.Time) SensorOption {
	return func(s *Sensor) {
		if now != nil {
			s.now = now
		}
	}
}

// NewSensor 建立路徑 B 感測器。
//
// checkInterval 為巡檢節奏（config.CheckInterval）；pollInterval 為到期門檻
// （config.PrinterPollInterval）。sampler 為 page counter／序號取得器（真實 *SNMPClient 或
// 測試 fake）；本機無個人專屬印表機時（NewSNMPClientFromEnv 回 ErrNotConfigured），由呼叫端
// 決定不啟動本感測器，優雅降級（3.4）。
func NewSensor(q *queue.Queue, enr idTokenProvider, sampler PageCounterSampler, checkInterval, pollInterval time.Duration, opts ...SensorOption) *Sensor {
	s := &Sensor{
		q:             q,
		enr:           enr,
		sampler:       sampler,
		checkInterval: checkInterval,
		pollInterval:  pollInterval,
		log:           slog.Default(),
		now:           time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Run 啟動巡檢迴圈，直到 ctx 取消。
//
// 啟動時先立即巡檢一次：兼作 3.4 的連通性檢查（不通只記 log、不中斷），並與四重觸發的
// 「開機後檢查」合流——關機數日後開機，距上次輪詢已超過門檻即自動補查。
func (s *Sensor) Run(ctx context.Context) error {
	if s.sampler == nil {
		s.log.Warn("path B disabled: no page counter sampler configured")
		return nil
	}
	s.log.Info("path B (printer) sensor started",
		"checkInterval", s.checkInterval, "pollInterval", s.pollInterval)

	s.checkAndPoll(ctx) // 開機後首次巡檢：連通性檢查（3.4）＋冷啟動／補查

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("path B (printer) sensor stopped")
			return nil
		case <-ticker.C:
			s.checkAndPoll(ctx)
		}
	}
}

// checkAndPoll 執行一次到期判斷；到期（或冷啟動）則查 page counter 與序號、入列、更新時間戳。
func (s *Sensor) checkAndPoll(ctx context.Context) {
	if ctx.Err() != nil {
		// 已取消（關機／停止中）：不再發查詢，也避免以失效 ctx 讀狀態而記出誤導性的錯誤 log。
		return
	}
	if !s.due(ctx) {
		return
	}

	cur, err := s.sampler.PageCounter(ctx)
	if err != nil {
		// 查不到（不同網段／印表機關機／未開 SNMP）：不更新時間戳，下次巡檢自然重試。
		// 不因此停用路徑 B——BYOD 筆電可能稍後才接回辦公室網段（3.4）。
		s.reportFailure(err)
		return
	}
	s.reportRecovery()

	serial := s.readSerial(ctx)

	if err := s.enqueue(ctx, cur, serial); err != nil {
		// 入列失敗不更新時間戳，確保重試；下次輪詢重新讀到的絕對值原樣覆蓋，等冪。
		s.log.Warn("path B: enqueue reading failed; will retry next check", "err", err)
		return
	}

	s.saveLastPoll(ctx)
}

// readSerial 取印表機序號；查無序號（三候選 OID 皆空、或明確指定的單一 OID 查無值）
// 不視為本次輪詢失敗——page counter 仍正常入列，只是 payload 省略 printer_serial，
// 由後端以 device_id 回退歸鍵並標記「印表機身份不明」（[D14] 缺口二）。
func (s *Sensor) readSerial(ctx context.Context) string {
	v, err := s.sampler.SerialNumber(ctx)
	if err != nil {
		s.reportSerialFailure(err)
		return ""
	}
	s.reportSerialRecovery()
	return v
}

// due 依持久化時間戳判斷是否到期。時間戳不存在（冷啟動）或無法解析一律視為「已到期」。
func (s *Sensor) due(ctx context.Context) bool {
	v, ok, err := s.q.GetState(ctx, StateKeyLastPoll)
	if err != nil {
		// 讀狀態失敗：保守視為到期（多查一次的代價遠小於漏採）；log 供排查。
		s.log.Warn("path B: read last-poll timestamp failed; treating as due", "err", err)
		return true
	}
	if !ok {
		return true // 冷啟動：時間戳不存在視為已到期
	}
	last, perr := time.Parse(time.RFC3339Nano, v)
	if perr != nil {
		s.log.Warn("path B: parse last-poll timestamp failed; treating as due", "value", v, "err", perr)
		return true
	}
	return s.now().Sub(last) >= s.pollInterval
}

// enqueue 以本次讀到的絕對讀數（與序號，若可得）組 payload 並 upsert 入列（事件 ID =
// idToken+日期+printer，一天一筆；後端以事件 ID 冪等 upsert，並以 collected_at 判定
// 「最新讀數勝出」，見 [D15]）。
func (s *Sensor) enqueue(ctx context.Context, counter int64, serial string) error {
	idToken, err := s.enr.IDToken()
	if err != nil {
		return err // 未綁定／已撤銷：無法歸戶，下次輪詢重試
	}

	date := s.now().Format("2006-01-02")

	// 純感測、只送原始量：printer_page_counter 為壽命累計絕對值，差分與重置防呆一律
	// 在後端進行（[D15]）。能耗 = 頁數 × 紙張生命週期係數，Agent 不算、也不送係數。
	payload := map[string]any{
		"printer_page_counter": counter,
	}
	if serial != "" {
		payload["printer_serial"] = serial
	}

	e := queue.Event{
		ID:        queue.EventID(idToken, date, queue.PathPrinter),
		PathType:  queue.PathPrinter,
		UsageDate: date,
		Payload:   payload,
		// [D14]：本次 SNMP 輪詢的時間戳，供後端亂序抵達勝出判定。
		CollectedAt: s.now(),
	}
	if err := s.q.Enqueue(ctx, e); err != nil {
		return err
	}
	s.log.Info("path B enqueued printer reading",
		"date", date, "printer_page_counter", counter, "printer_serial", serial)
	return nil
}

// saveLastPoll 以現在時間更新持久化時間戳（RFC3339Nano）。
func (s *Sensor) saveLastPoll(ctx context.Context) {
	now := s.now().Format(time.RFC3339Nano)
	if err := s.q.SetState(ctx, StateKeyLastPoll, now); err != nil {
		// 時間戳未寫入：下次巡檢會因未更新而再查一次；重讀到的絕對值原樣覆蓋同一事件，等冪。
		s.log.Warn("path B: persist last-poll timestamp failed", "err", err)
	}
}

// reportFailure 記錄查詢失敗。首次（或剛從成功轉為失敗）以 Warn 附 BYOD 排查指引，
// 之後降為 Debug——印表機不在同網段是 BYOD 的常態，不該每分鐘刷一次 Warn（3.4）。
func (s *Sensor) reportFailure(err error) {
	s.failures++
	if s.failures == 1 {
		s.log.Warn("path B: printer unreachable; skipping this poll and retrying on later checks "+
			"(need same subnet as the printer with SNMP enabled)", "err", err)
		return
	}
	s.log.Debug("path B: printer still unreachable", "consecutive_failures", s.failures, "err", err)
}

// reportRecovery 於失敗後首次成功時記一筆，便於對照斷線區間。
func (s *Sensor) reportRecovery() {
	if s.failures > 0 {
		s.log.Info("path B: printer reachable again", "after_failures", s.failures)
		s.failures = 0
	}
}

// reportSerialFailure 記錄序號查詢失敗，音量控制同 reportFailure；低階機種常三個候選
// OID 皆回空值（待實測項），不該每次巡檢都 Warn 一次。
func (s *Sensor) reportSerialFailure(err error) {
	s.serialFailures++
	if s.serialFailures == 1 {
		s.log.Warn("path B: printer serial number unavailable; payload will omit printer_serial "+
			"(backend falls back to device_id keying and flags the row as unidentified, [D14])", "err", err)
		return
	}
	s.log.Debug("path B: printer serial number still unavailable", "consecutive_failures", s.serialFailures, "err", err)
}

// reportSerialRecovery 於序號查詢失敗後首次成功時記一筆。
func (s *Sensor) reportSerialRecovery() {
	if s.serialFailures > 0 {
		s.log.Info("path B: printer serial number available again", "after_failures", s.serialFailures)
		s.serialFailures = 0
	}
}
