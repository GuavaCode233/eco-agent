// 本檔實作路徑 B 的感測器 Run 迴圈（CLAUDE.md Step 3.2–3.4）。
//
// 感測模式（3.2）：page counter 為累計狀態量、無推播，只能輪詢。沿用 Step 2 的觸發模型
// （關鍵不可違反：不用絕對計時器）：
//   - 掛 checkInterval（60 秒巡檢），與路徑 C 同一條巡檢節奏；
//   - 每次巡檢以持久化時間戳 lastPrinterPollAt 判斷 now-lastPrinterPollAt >= printerPollInterval
//     才查、入列、更新時間戳；時間戳不存在（冷啟動）或無法解析視為「已到期」；
//   - 查詢／入列失敗不更新時間戳，下次巡檢自然重試（不設獨立重試計時器、不指數退避）。
//
// 送出（3.3）：Agent 純感測、只送原始量（比照路徑 A/C）——payload {date, print_pages}
// 僅含「當日累計增量頁數」這個感測值。能耗（= 頁數 × 紙張生命週期係數）一律由後端計算，
// Agent 端不做任何換算，也不送任何係數。走 MQTT（協定分流由 uploader 依 PathPrinter 決定，
// 現階段 mock 送出）。
//
// BYOD 摩擦點（3.4）：SNMP 需與印表機同網段。啟動時的首次巡檢即為連通性檢查——不通則
// 記 log 跳過，Run 不阻塞、不回錯誤，其餘路徑照常運作。
package printer

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"eco-agent/internal/queue"
)

// 持久化狀態鍵（與佇列同一份 SQLite，跨重啟保留）。
const (
	// StateKeyLastPoll 為上次輪詢時間戳，供 3.2 到期判斷（對應路徑 C 的 lastDriveQuotaCheckAt）。
	StateKeyLastPoll = "lastPrinterPollAt"
	// StateKeyPollState 為 page counter 基準與當日累計，見 pollState。
	StateKeyPollState = "printerPollState"
)

// pollState 是路徑 B 跨重啟的持久化狀態。
//
// 三個欄位**整組以單一 state 鍵原子寫入**，這是刻意的：基準（LastPageCount）與當日累計
// （DayPages）必須同進同退。若分開寫而其中一筆失敗，下次輪詢會以「舊基準＋新累計」重算
// 而把同一段增量算兩次；合為一筆後，寫入失敗時兩者都停在舊值，下次輪詢由同一基準重算出
// 同一個當日累計、upsert 覆蓋同一筆事件，結果完全等冪。
type pollState struct {
	// LastPageCount 為上次成功讀到的 page counter 累計值；-1 表示尚無基準（首次輪詢）。
	LastPageCount int64 `json:"last_page_count"`
	// DayDate 為 DayPages 所屬日期（YYYY-MM-DD）。
	DayDate string `json:"day_date"`
	// DayPages 為該日至今累計的增量頁數。
	DayPages int64 `json:"day_pages"`
}

// newPollState 回傳「尚無基準」的初始狀態。
func newPollState() pollState { return pollState{LastPageCount: -1} }

// idTokenProvider 抽象「取員工 ID Token」，由 *enroll.Enroller 滿足（與路徑 A/C 一致）。
type idTokenProvider interface {
	IDToken() (string, error)
}

// Sensor 是路徑 B 印表機感測器：掛 checkInterval 巡檢，到期時查 page counter、
// 相減得增量頁數並累計入列。
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
// （config.PrinterPollInterval）。sampler 為 page counter 取得器（真實 *SNMPClient 或測試
// fake）；本機無個人專屬印表機時（NewSNMPClientFromEnv 回 ErrNotConfigured），由呼叫端
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

// checkAndPoll 執行一次到期判斷；到期（或冷啟動）則查 page counter、記錄增量、更新時間戳。
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

	if err := s.record(ctx, cur); err != nil {
		// 入列失敗同樣不更新時間戳，確保重試。
		s.log.Warn("path B: record page counter failed; will retry next check", "err", err)
		return
	}

	s.saveLastPoll(ctx)
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

// record 以本次讀到的累計值算增量、累加到當日累計並入列，最後原子更新持久化狀態。
//
// 首次輪詢（無基準）只建立基準、不入列：沒有前值可減，硬算會把「出廠以來的累計頁數」
// 整包記到員工頭上。增量為 0（期間沒列印）時亦不入列，避免每日產生 0 頁的空事件白佔
// 佇列與上傳批次。
func (s *Sensor) record(ctx context.Context, cur int64) error {
	st := s.loadState(ctx)

	if st.LastPageCount < 0 {
		s.log.Info("path B: baseline established (first poll; no delta counted)", "page_counter", cur)
		return s.saveState(ctx, pollState{LastPageCount: cur, DayDate: st.DayDate, DayPages: st.DayPages})
	}

	delta := PageDelta(st.LastPageCount, cur)
	if cur < st.LastPageCount {
		// counter 重置（換機／韌體重置）：PageDelta 已回 0，這裡只記錄並改以 cur 為新基準。
		s.log.Info("path B: page counter reset detected; rebasing without back-filling",
			"prev", st.LastPageCount, "current", cur)
	}

	date := s.now().Format("2006-01-02")
	if st.DayDate != date {
		// 跨日：舊日累計最後一筆已入列，這裡起算新的一天。
		// 跨越午夜的那段增量整段歸到「輪詢當下」的日期——輪詢區間為分鐘級，歸日誤差有限，
		// 且 page counter 無法回推各頁的實際列印時刻。
		st.DayDate, st.DayPages = date, 0
	}

	if delta == 0 {
		return s.saveState(ctx, pollState{LastPageCount: cur, DayDate: st.DayDate, DayPages: st.DayPages})
	}

	pages := st.DayPages + delta
	if err := s.enqueue(ctx, date, pages); err != nil {
		return err // 不更新狀態：下次輪詢由同一基準重算出同一累計，等冪
	}
	s.log.Info("path B enqueued print pages",
		"date", date, "delta_pages", delta, "print_pages", pages)

	return s.saveState(ctx, pollState{LastPageCount: cur, DayDate: date, DayPages: pages})
}

// enqueue 以當日累計增量頁數組 payload 並 upsert 入列（事件 ID = idToken+日期+printer，
// 一天一筆；後端以事件 ID 冪等 upsert 取最新累計）。
func (s *Sensor) enqueue(ctx context.Context, date string, pages int64) error {
	idToken, err := s.enr.IDToken()
	if err != nil {
		return err // 未綁定／已撤銷：無法歸戶，保留狀態不動，下次輪詢重試
	}

	// 純感測、只送原始量：僅「當日累計增量頁數」一個感測值。
	// 能耗 = 頁數 × 紙張生命週期係數，換算完全在後端進行——Agent 不算、也不送係數。
	payload := map[string]any{
		"date":        date,
		"print_pages": pages,
	}

	e := queue.Event{
		ID:       queue.EventID(idToken, date, queue.PathPrinter),
		PathType: queue.PathPrinter,
		Payload:  payload,
	}
	return s.q.Enqueue(ctx, e)
}

// loadState 讀回持久化狀態；不存在或無法解析視為「尚無基準」（下次輪詢重新建立基準，
// 不會誤把整個 life count 當增量）。
func (s *Sensor) loadState(ctx context.Context) pollState {
	v, ok, err := s.q.GetState(ctx, StateKeyPollState)
	if err != nil {
		s.log.Warn("path B: read poll state failed; treating as no baseline", "err", err)
		return newPollState()
	}
	if !ok {
		return newPollState()
	}
	st := newPollState()
	if err := json.Unmarshal([]byte(v), &st); err != nil {
		s.log.Warn("path B: parse poll state failed; treating as no baseline", "value", v, "err", err)
		return newPollState()
	}
	return st
}

// saveState 原子寫入持久化狀態（基準與當日累計同進同退，見 pollState）。
func (s *Sensor) saveState(ctx context.Context, st pollState) error {
	b, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return s.q.SetState(ctx, StateKeyPollState, string(b))
}

// saveLastPoll 以現在時間更新持久化時間戳（RFC3339Nano）。
func (s *Sensor) saveLastPoll(ctx context.Context) {
	now := s.now().Format(time.RFC3339Nano)
	if err := s.q.SetState(ctx, StateKeyLastPoll, now); err != nil {
		// 時間戳未寫入：下次巡檢會因未更新而再查一次；因 pollState 未變，重算結果相同，等冪。
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
