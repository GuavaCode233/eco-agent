// Package computer 實作路徑 A：電腦使用感測（CLAUDE.md Step 1.3）。
//
// 感測模型（v13 [D7]）：Agent 純感測、只送原始量，不算能耗。每個輪詢區間依「距上次
// 輸入的間隔」是否超過閾值判為 active／idle 兩態，分別累計時數，並記該日時間加權平均
// CPU 使用率。能耗由後端以使用率加權模型 P_idle + 使用率×(P_active−P_idle) 計算，
// 誘因對齊「節能」（歸戶 idle 開機的浪費）而非「少用」。
//
// 佇列模型（狀態值輪詢）：事件 ID 為 idToken+日期+路徑（見 queue.EventID），故一天一筆
// 累計事件；每次輪詢以當日累計 upsert 覆蓋同一筆，後端冪等 upsert 取最新累計。因此
// payload 為「當日至今的累計量」而非單一區間增量，重送／重啟皆可重現。
package computer

import (
	"context"
	"log/slog"
	"math"
	"time"

	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
)

// DefaultIdleThreshold 為判定 idle 的預設閾值：距上次輸入超過此值，該輪詢區間判為 idle。
//
// 取 5 分鐘對齊常見「使用者離開」認定（螢幕保護／away 偵測量級）；模型目的為歸戶
// 「開機但長時間未使用」的浪費，短暫停頓不應誤判為 idle。
//
// TODO(backend): idle 閾值日後併入 5.2 集中配置服務（sensor_config）下發；現為本機預設，
// 可經 WithIdleThreshold 覆寫（測試／demo 用小值）。
const DefaultIdleThreshold = 10 * time.Minute

// idTokenProvider 抽象「取員工 ID Token」，由 *enroll.Enroller 滿足。
// 以介面注入便於測試，且維持路徑 A 對 enroll 的最小依賴面。
type idTokenProvider interface {
	IDToken() (string, error)
}

// Sensor 是路徑 A 電腦使用感測器：定時輪詢活動與 CPU，累計 active/idle 時數並入列。
type Sensor struct {
	q        *queue.Queue
	enr      idTokenProvider
	activity platform.ActivityDetector
	cpu      platform.CPUSampler

	interval      time.Duration
	idleThreshold time.Duration
	log           *slog.Logger
	now           func() time.Time

	acc dayAccumulator
}

// dayAccumulator 保存「當日至今」的累計量。跨午夜切換日期即重置。
//
// 不變式：cpuHours == activeHours+idleHours（任一取樣失敗的區間整段跳過、兩者皆不計），
// 使 avgCPU = cpuWeightedSum/(activeHours+idleHours)，且重啟回填可由 payload 精確反推。
type dayAccumulator struct {
	date           string
	activeHours    float64
	idleHours      float64
	cpuWeightedSum float64 // Σ(cpu% × 區間時數)，用於時間加權平均
}

func (a dayAccumulator) totalHours() float64 { return a.activeHours + a.idleHours }

func (a dayAccumulator) avgCPU() float64 {
	th := a.totalHours()
	if th <= 0 {
		return 0
	}
	return a.cpuWeightedSum / th
}

// Option 以函式選項調整 Sensor（對齊 uploader 的 WithX 慣例）。
type Option func(*Sensor)

// WithLogger 設定日誌器；預設 slog.Default()。
func WithLogger(l *slog.Logger) Option {
	return func(s *Sensor) {
		if l != nil {
			s.log = l
		}
	}
}

// WithIdleThreshold 覆寫 idle 判定閾值（測試／demo 用小值即可在數秒內觀察 idle 累計）。
func WithIdleThreshold(d time.Duration) Option {
	return func(s *Sensor) {
		if d > 0 {
			s.idleThreshold = d
		}
	}
}

// WithNow 注入時間函式（測試用，控制日期切換）。
func WithNow(now func() time.Time) Option {
	return func(s *Sensor) {
		if now != nil {
			s.now = now
		}
	}
}

// New 建立路徑 A 感測器。interval 為輪詢區間（config.ComputerUsageRecordInterval）。
func New(q *queue.Queue, enr idTokenProvider, activity platform.ActivityDetector, cpu platform.CPUSampler, interval time.Duration, opts ...Option) *Sensor {
	s := &Sensor{
		q:             q,
		enr:           enr,
		activity:      activity,
		cpu:           cpu,
		interval:      interval,
		idleThreshold: DefaultIdleThreshold,
		log:           slog.Default(),
		now:           time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Run 啟動輪詢迴圈，直到 ctx 取消。
//
// 平台不支援活動偵測時（非 Windows/macOS）優雅降級：記 log 並回 nil，不使 Agent 卡住
// （路徑 A 於此類平台停用，其餘路徑照常）。
func (s *Sensor) Run(ctx context.Context) error {
	if err := s.activity.Available(); err != nil {
		s.log.Warn("path A disabled: activity detection unavailable", "err", err)
		return nil
	}
	if err := s.cpu.Available(); err != nil {
		s.log.Warn("path A disabled: cpu sampling unavailable", "err", err)
		return nil
	}

	// 重啟回填：若當日累計事件仍在佇列（尚未上傳清除），讀回作為累計基準，
	// 避免下次 upsert 以較小的重啟後累計覆蓋。
	s.restoreToday(ctx)

	s.log.Info("path A (computer) sensor started",
		"interval", s.interval, "idleThreshold", s.idleThreshold)

	ticker := time.NewTicker(s.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			s.log.Info("path A (computer) sensor stopped")
			return nil
		case <-ticker.C:
			s.pollOnce(ctx)
		}
	}
}

// restoreToday 以佇列中當日事件回填記憶體累計（若存在）。
func (s *Sensor) restoreToday(ctx context.Context) {
	date := s.now().Format("2006-01-02")
	idToken, err := s.enr.IDToken()
	if err != nil {
		// 尚未綁定等：無法組事件 ID，跳過回填；首次 pollOnce 取得 token 後自然累計。
		return
	}
	id := queue.EventID(idToken, date, queue.PathComputer)
	e, ok, err := s.q.Get(ctx, id)
	if err != nil {
		s.log.Warn("path A restore: queue get failed", "err", err)
		return
	}
	if !ok {
		return
	}
	s.acc = accumulatorFromPayload(date, e.Payload)
	s.log.Info("path A restored today's accumulator from queue",
		"date", date, "active_h", s.acc.activeHours, "idle_h", s.acc.idleHours)
}

// pollOnce 執行一次輪詢：取樣、分態累計、入列當日累計。供 Run 與測試呼叫。
func (s *Sensor) pollOnce(ctx context.Context) {
	date := s.now().Format("2006-01-02")
	if date != s.acc.date {
		// 跨日：舊日最後一筆累計已於前一次輪詢入列，這裡起算新的一天。
		s.acc = dayAccumulator{date: date}
	}

	idle, err := s.activity.IdleTime()
	if err != nil {
		s.log.Warn("path A: read idle time failed; skipping interval", "err", err)
		return
	}
	cpuPct, err := s.cpu.Percent()
	if err != nil {
		// 整段跳過以維持 cpuHours==totalHours 不變式（重啟回填可精確反推）。
		s.log.Warn("path A: read cpu percent failed; skipping interval", "err", err)
		return
	}

	// TODO(step1.4): 以 wall-clock 時間戳差分辨識 sleep/hibernate 掛起空白，該段（間隔遠
	// 大於輪詢區間）不計 active/idle；現以名目輪詢區間計時。
	hours := s.interval.Hours()

	if idle >= s.idleThreshold {
		s.acc.idleHours += hours
	} else {
		s.acc.activeHours += hours
	}
	s.acc.cpuWeightedSum += cpuPct * hours

	s.enqueueToday(ctx)
}

// enqueueToday 以當日累計組 payload 並 upsert 入列。
func (s *Sensor) enqueueToday(ctx context.Context) {
	idToken, err := s.enr.IDToken()
	if err != nil {
		// 未綁定／已撤銷：無法歸戶，暫不入列；累計保留，取得 token 後下次輪詢入列。
		s.log.Warn("path A: id token unavailable; deferring enqueue", "err", err)
		return
	}

	payload := map[string]any{
		"date":            s.acc.date,
		"pc_active_hours": round6(s.acc.activeHours),
		"pc_idle_hours":   round6(s.acc.idleHours),
		"pc_avg_cpu_util": round2(s.acc.avgCPU()),
	}
	// cpu_model 於部分虛擬機為空；未知即略過該欄位（見 platform.CPUSampler.Model）。
	if model, err := s.cpu.Model(); err == nil && model != "" {
		payload["cpu_model"] = model
	}

	e := queue.Event{
		ID:       queue.EventID(idToken, s.acc.date, queue.PathComputer),
		PathType: queue.PathComputer,
		Payload:  payload,
	}
	if err := s.q.Enqueue(ctx, e); err != nil {
		s.log.Warn("path A: enqueue failed; retained in memory", "err", err)
		return
	}
	s.log.Debug("path A enqueued daily cumulative",
		"date", s.acc.date, "active_h", round6(s.acc.activeHours),
		"idle_h", round6(s.acc.idleHours), "avg_cpu", round2(s.acc.avgCPU()))
}

// accumulatorFromPayload 由既有 payload 精確反推累計狀態（重啟回填用）。
func accumulatorFromPayload(date string, p map[string]any) dayAccumulator {
	active := toFloat(p["pc_active_hours"])
	idle := toFloat(p["pc_idle_hours"])
	avg := toFloat(p["pc_avg_cpu_util"])
	return dayAccumulator{
		date:           date,
		activeHours:    active,
		idleHours:      idle,
		cpuWeightedSum: avg * (active + idle), // avg×totalHours 反推加權和
	}
}

// toFloat 容錯地把 payload 值（可能為 float64／json.Number／int）轉為 float64。
func toFloat(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case float32:
		return float64(x)
	case int:
		return float64(x)
	case int64:
		return float64(x)
	default:
		return 0
	}
}

func round6(x float64) float64 { return math.Round(x*1e6) / 1e6 }
func round2(x float64) float64 { return math.Round(x*1e2) / 1e2 }
