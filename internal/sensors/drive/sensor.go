// 本檔實作路徑 C 的感測器 Run 迴圈（CLAUDE.md Step 2.2–2.5）。
//
// 觸發模型（§2 Step 2.2，關鍵不可違反：不可用 sleep(24h) 絕對計時器）：
//   - 掛 checkInterval（60 秒巡檢）而非 computerUsageRecordInterval（職責分離）；
//   - 每次巡檢以持久化時間戳 lastDriveQuotaCheckAt 做到期判斷：
//     now() - lastDriveQuotaCheckAt >= driveQuotaInterval（24h）才查、Enqueue、更新時間戳；
//   - 時間戳與佇列同一份 SQLite 落磁碟，跨重啟保留。
//
// 冷啟動（2.3）：時間戳不存在視為「已到期」→ 首次巡檢即查並寫入時間戳。
// 開機補查（2.4）：關機數日後開機，距上次查詢已超過門檻 → 開機後首次巡檢自動補查；
//
//	與四重觸發的「開機後檢查」合流——Run 啟動時先立即巡檢一次，不等第一個 tick。
//
// 送出（2.5）：Agent 純感測、只送原始量（比照路徑 A）；payload {date, drive_usage_gb}，
//
//	能耗（= 儲存量GB × PUE × 電力係數）由後端計算。走 HTTPS（協定分流由 uploader 處理，
//	現階段 mock 送出）。
package drive

import (
	"context"
	"log/slog"
	"math"
	"time"

	"eco-agent/internal/queue"
)

// StateKeyLastCheck 為路徑 C 上次查詢時間戳的持久化鍵名（存 queue 的 state KV）。
const StateKeyLastCheck = "lastDriveQuotaCheckAt"

// idTokenProvider 抽象「取員工 ID Token」，由 *enroll.Enroller 滿足（與路徑 A 一致）。
type idTokenProvider interface {
	IDToken() (string, error)
}

// Sensor 是路徑 C 雲端儲存感測器：掛 checkInterval 巡檢，到期時查 Drive 用量並入列。
type Sensor struct {
	q       *queue.Queue
	enr     idTokenProvider
	sampler QuotaSampler

	checkInterval time.Duration // 巡檢節奏（config.CheckInterval，60 秒）
	quotaInterval time.Duration // 到期門檻（config.DriveQuotaInterval，24h）

	log *slog.Logger
	now func() time.Time
}

// Option 以函式選項調整 Sensor（對齊 sensors/computer 慣例）。
type SensorOption func(*Sensor)

// WithSensorLogger 設定日誌器；預設 slog.Default()。
func WithSensorLogger(l *slog.Logger) SensorOption {
	return func(s *Sensor) {
		if l != nil {
			s.log = l
		}
	}
}

// WithSensorNow 注入時間函式（測試用，控制到期判斷與冷啟動/補查情境）。
func WithSensorNow(now func() time.Time) SensorOption {
	return func(s *Sensor) {
		if now != nil {
			s.now = now
		}
	}
}

// NewSensor 建立路徑 C 感測器。
//
// checkInterval 為巡檢節奏（config.CheckInterval）；quotaInterval 為到期門檻
// （config.DriveQuotaInterval）。sampler 為 Drive 用量取得器（真實 *APIClient 或測試 fake）；
// 憑證未就緒時由呼叫端（wiring）決定不啟動本感測器，優雅降級（§6）。
func NewSensor(q *queue.Queue, enr idTokenProvider, sampler QuotaSampler, checkInterval, quotaInterval time.Duration, opts ...SensorOption) *Sensor {
	s := &Sensor{
		q:             q,
		enr:           enr,
		sampler:       sampler,
		checkInterval: checkInterval,
		quotaInterval: quotaInterval,
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
// 啟動時先立即巡檢一次（開機後檢查／補查合流，2.4），其後每 checkInterval 巡檢。
func (s *Sensor) Run(ctx context.Context) error {
	s.log.Info("path C (drive) sensor started",
		"checkInterval", s.checkInterval, "quotaInterval", s.quotaInterval)

	s.checkAndSample(ctx) // 開機後首次巡檢：冷啟動即查（2.3）／關機數日後補查（2.4）

	ticker := time.NewTicker(s.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("path C (drive) sensor stopped")
			return nil
		case <-ticker.C:
			s.checkAndSample(ctx)
		}
	}
}

// checkAndSample 執行一次到期判斷；到期（或冷啟動）則查用量、入列、更新時間戳。
func (s *Sensor) checkAndSample(ctx context.Context) {
	if !s.due(ctx) {
		return
	}

	q, err := s.sampler.StorageQuota(ctx)
	if err != nil {
		// 查詢失敗（離線/API 錯誤）：不更新時間戳，下次巡檢自然重試（不設獨立重試計時器）。
		s.log.Warn("path C: query storage quota failed; will retry next check", "err", err)
		return
	}

	if err := s.enqueue(ctx, q); err != nil {
		// 入列失敗同樣不更新時間戳，確保重試。
		s.log.Warn("path C: enqueue failed; will retry next check", "err", err)
		return
	}

	// 僅在查詢＋入列皆成功後才更新時間戳——避免失敗時提前推進門檻而漏採。
	s.saveLastCheck(ctx)
}

// due 依持久化時間戳判斷是否到期。時間戳不存在（冷啟動）或無法解析一律視為「已到期」（2.3）。
func (s *Sensor) due(ctx context.Context) bool {
	v, ok, err := s.q.GetState(ctx, StateKeyLastCheck)
	if err != nil {
		// 讀狀態失敗：保守視為到期（寧可多查一次也不漏採）；log 供排查。
		s.log.Warn("path C: read last-check timestamp failed; treating as due", "err", err)
		return true
	}
	if !ok {
		return true // 冷啟動：時間戳不存在視為已到期
	}
	last, perr := time.Parse(time.RFC3339Nano, v)
	if perr != nil {
		s.log.Warn("path C: parse last-check timestamp failed; treating as due", "value", v, "err", perr)
		return true
	}
	return s.now().Sub(last) >= s.quotaInterval
}

// enqueue 以當日用量組 payload 並 upsert 入列（事件 ID = idToken+日期+drive，一天一筆）。
func (s *Sensor) enqueue(ctx context.Context, quota Quota) error {
	idToken, err := s.enr.IDToken()
	if err != nil {
		return err // 未綁定／已撤銷：無法歸戶，交由呼叫端記 log 並重試
	}
	date := s.now().Format("2006-01-02")

	// 純感測、只送原始量：drive_usage_gb 為帳號雲端儲存取用量（storageQuota.usage，
	// 涵蓋 Drive/Gmail/Photos，代表使用者雲端儲存足跡）；能耗由後端換算。usageInDrive
	// 可另供後端做 Drive-only 分析，本 payload 依 §2 Step 2.5 僅帶 drive_usage_gb。
	payload := map[string]any{
		"date":           date,
		"drive_usage_gb": round6(quota.UsageGB()),
	}
	e := queue.Event{
		ID:       queue.EventID(idToken, date, queue.PathDrive),
		PathType: queue.PathDrive,
		Payload:  payload,
	}
	if err := s.q.Enqueue(ctx, e); err != nil {
		return err
	}
	s.log.Info("path C enqueued drive usage",
		"date", date, "drive_usage_gb", round6(quota.UsageGB()))
	return nil
}

// saveLastCheck 以現在時間更新持久化時間戳（RFC3339Nano）。
func (s *Sensor) saveLastCheck(ctx context.Context) {
	now := s.now().Format(time.RFC3339Nano)
	if err := s.q.SetState(ctx, StateKeyLastCheck, now); err != nil {
		// 時間戳未寫入：下次巡檢會因未更新而再查一次；入列 upsert 冪等，不致重複計算。
		s.log.Warn("path C: persist last-check timestamp failed", "err", err)
		return
	}
}

func round6(x float64) float64 { return math.Round(x*1e6) / 1e6 }
