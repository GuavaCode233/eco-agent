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
// 取 10 分鐘對齊常見「使用者離開」認定（螢幕保護／away 偵測量級）；模型目的為歸戶
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
	// power 為即時功耗量測（Step 1.5 精度增強 overlay）；預設不支援，Agent 回退使用率
	// 加權模型。結構預留、平台端不實作（見 platform.PowerSampler）。
	power platform.PowerSampler

	interval            time.Duration
	idleThreshold       time.Duration
	suspendGapThreshold time.Duration
	log                 *slog.Logger
	now                 func() time.Time

	// lastPollAt 為上次輪詢的「牆鐘」時間戳（已剝除 monotonic 讀數）。用於 sleep/喚醒
	// 偵測：牆鐘於掛起期間照走，故 now-lastPollAt 反映真實流逝，可辨識掛起空白。零值
	// 表示尚未有基準（首次輪詢）。
	lastPollAt time.Time

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

// WithPowerSampler 注入即時功耗量測器（Step 1.5 精度增強 overlay）。預設為不支援實作；
// 日後補平台端 RAPL/powermetrics 後由此注入，無需改動 sensor 其餘結構。
func WithPowerSampler(p platform.PowerSampler) Option {
	return func(s *Sensor) {
		if p != nil {
			s.power = p
		}
	}
}

// WithSuspendGapThreshold 覆寫 sleep/掛起判定門檻（測試／demo 用小值即可觸發掛起偵測）。
func WithSuspendGapThreshold(d time.Duration) Option {
	return func(s *Sensor) {
		if d > 0 {
			s.suspendGapThreshold = d
		}
	}
}

// WithNow 注入時間函式（測試用，控制日期切換與 sleep/喚醒牆鐘跳躍）。
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
		power:         platform.NewPowerSampler(), // 預設不支援；Step 1.5 結構預留
		interval:      interval,
		idleThreshold: DefaultIdleThreshold,
		// 掛起判定門檻：牆鐘 gap 達此值即視為 sleep/hibernate/關機空白，該段不計。
		// 取 3×輪詢區間——真實掛起為分鐘～小時級，遠超此值；僅極端行程飢餓（連續數個
		// 區間未被排程）才會誤判，代價為略過一個區間，可接受。可經 WithSuspendGapThreshold 覆寫。
		suspendGapThreshold: 3 * interval,
		log:                 slog.Default(),
		now:                 time.Now,
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

	// 即時功耗量測能力（Step 1.5）：預設不支援 → 回退使用率加權模型。此處僅記錄能力，
	// 平台端 RAPL/powermetrics 現階段不實作（見 platform.PowerSampler）。
	if err := s.power.Available(); err != nil {
		s.log.Info("path A: real-time power sampling unavailable; using utilization-weighted model", "err", err)
	} else {
		s.log.Info("path A: real-time power sampling available (accuracy-enhancement overlay)")
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
	now := s.now()
	date := now.Format("2006-01-02")
	if date != s.acc.date {
		// 跨日：舊日最後一筆累計已於前一次輪詢入列，這裡起算新的一天。
		s.acc = dayAccumulator{date: date}
	}

	// sleep/喚醒偵測（Step 1.4）：以牆鐘差分辨識掛起空白。牆鐘於 sleep/hibernate/關機
	// 期間照走（monotonic 則凍結），故 .Round(0) 剝除 monotonic 讀數後的差分反映真實流逝。
	wallNow := now.Round(0)
	if !s.lastPollAt.IsZero() {
		if gap := wallNow.Sub(s.lastPollAt); gap >= s.suspendGapThreshold {
			// 掛起空白：機器 sleep/關機期間耗電趨近零、無時數可採，該段不計 active/idle
			// （v13：歸戶 idle 開機浪費而非睡眠）。重設基準，下一輪恢復正常計數。
			s.log.Info("path A: resumed from suspend; gap not billed",
				"gap", gap, "interval", s.interval)
			s.lastPollAt = wallNow
			return
		}
	}
	s.lastPollAt = wallNow

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

	// 正常區間以名目輪詢區間計時（不修正 ticker drift，與 1.3 一致）；掛起空白已於上方
	// 排除。sub-threshold 的小幅延遲（GC、排程抖動）仍視為正常，攤一個名目區間。
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

	// Step 1.5 精度增強 overlay 接點：有即時功耗量測時附上實測功耗供後端優先採用。
	// 現階段 s.power 預設不支援（ok=false），故不加此欄，Agent 回退使用率加權模型。
	// TODO(backend): 即時功耗覆蓋（RAPL/powermetrics）作為精度增強——平台實作就緒後，
	// 此處應改累計「時間加權平均功耗」（比照 pc_avg_cpu_util），而非附上瞬時值。
	if w, ok, err := s.power.PowerWatts(); ok && err == nil {
		payload["pc_power_w"] = round2(w)
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
