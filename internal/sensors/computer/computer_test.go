package computer

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"eco-agent/internal/queue"
)

// ── 測試替身 ──

// fakeActivity 回傳可控的 idle 間隔，驅動 active/idle 分類。
type fakeActivity struct {
	idle time.Duration
	err  error
}

func (f *fakeActivity) IdleTime() (time.Duration, error) { return f.idle, f.err }
func (f *fakeActivity) Available() error                 { return nil }

// fakeCPU 回傳可控的 CPU 使用率與型號。
type fakeCPU struct {
	pct   float64
	model string
	err   error
}

func (f *fakeCPU) Percent() (float64, error) { return f.pct, f.err }
func (f *fakeCPU) Model() (string, error)    { return f.model, nil }
func (f *fakeCPU) Available() error          { return nil }

// fakeEnroll 提供固定 ID Token。
type fakeEnroll struct{ token string }

func (f fakeEnroll) IDToken() (string, error) { return f.token, nil }

const testToken = "test-idtoken-A"

func newTestQueue(t *testing.T) *queue.Queue {
	t.Helper()
	q, err := queue.Open(context.Background(), filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

// fixedNow 回傳固定日期的 now 函式。
func fixedNow(date string) func() time.Time {
	ts, _ := time.Parse("2006-01-02", date)
	return func() time.Time { return ts }
}

// TestActiveIdleClassification：idle < 閾值計 active、>= 閾值計 idle，時數各自累計。
func TestActiveIdleClassification(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	act := &fakeActivity{}
	cpu := &fakeCPU{pct: 50, model: "TestCPU"}

	interval := 60 * time.Second
	s := New(q, fakeEnroll{testToken}, act, cpu, interval,
		WithIdleThreshold(5*time.Minute), WithNow(fixedNow("2026-07-17")))

	// 兩個 active 區間（idle 遠小於閾值）。
	act.idle = 2 * time.Second
	s.pollOnce(ctx)
	s.pollOnce(ctx)
	// 一個 idle 區間（idle 超過閾值）。
	act.idle = 10 * time.Minute
	s.pollOnce(ctx)

	id := queue.EventID(testToken, "2026-07-17", queue.PathComputer)
	e, ok, err := q.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("Get event: ok=%v err=%v", ok, err)
	}
	wantActive := round6(2 * interval.Hours())
	wantIdle := round6(1 * interval.Hours())
	if got := e.Payload["pc_active_hours"].(float64); got != wantActive {
		t.Errorf("pc_active_hours = %v; want %v", got, wantActive)
	}
	if got := e.Payload["pc_idle_hours"].(float64); got != wantIdle {
		t.Errorf("pc_idle_hours = %v; want %v", got, wantIdle)
	}
	// 三區間 CPU 皆 50%，時間加權平均仍為 50。
	if got := e.Payload["pc_avg_cpu_util"].(float64); got != 50 {
		t.Errorf("pc_avg_cpu_util = %v; want 50", got)
	}
	if got := e.Payload["cpu_model"].(string); got != "TestCPU" {
		t.Errorf("cpu_model = %q; want TestCPU", got)
	}
}

// TestCumulativeUpsertSingleEvent：多次輪詢僅產生一筆事件（同日同路徑 upsert）。
func TestCumulativeUpsertSingleEvent(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	act := &fakeActivity{idle: time.Second}
	cpu := &fakeCPU{pct: 30}
	s := New(q, fakeEnroll{testToken}, act, cpu, 60*time.Second,
		WithNow(fixedNow("2026-07-17")))

	for i := 0; i < 5; i++ {
		s.pollOnce(ctx)
	}
	n, err := q.Count(ctx)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Fatalf("queue count = %d; want 1 (cumulative single daily event)", n)
	}
}

// TestWeightedAvgCPU：不同區間 CPU 值，驗證時間加權平均。
func TestWeightedAvgCPU(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	act := &fakeActivity{idle: time.Second} // 皆 active
	cpu := &fakeCPU{}
	s := New(q, fakeEnroll{testToken}, act, cpu, 60*time.Second,
		WithNow(fixedNow("2026-07-17")))

	cpu.pct = 20
	s.pollOnce(ctx)
	cpu.pct = 80
	s.pollOnce(ctx)

	id := queue.EventID(testToken, "2026-07-17", queue.PathComputer)
	e, _, _ := q.Get(ctx, id)
	// 等長區間，(20+80)/2 = 50。
	if got := e.Payload["pc_avg_cpu_util"].(float64); got != 50 {
		t.Errorf("pc_avg_cpu_util = %v; want 50", got)
	}
}

// TestRestartRestore：重啟後以佇列既有事件回填累計，續累不覆蓋較小值。
func TestRestartRestore(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	act := &fakeActivity{idle: time.Second}
	cpu := &fakeCPU{pct: 40}
	interval := 60 * time.Second
	now := fixedNow("2026-07-17")

	// 第一段：累計 3 個 active 區間後「重啟」。
	s1 := New(q, fakeEnroll{testToken}, act, cpu, interval, WithNow(now))
	for i := 0; i < 3; i++ {
		s1.pollOnce(ctx)
	}

	// 第二個 Sensor 模擬重啟（記憶體歸零），Run 內會呼 restoreToday；此處直接呼叫。
	s2 := New(q, fakeEnroll{testToken}, act, cpu, interval, WithNow(now))
	s2.restoreToday(ctx)
	if got := round6(s2.acc.activeHours); got != round6(3*interval.Hours()) {
		t.Fatalf("restored active hours = %v; want %v", got, round6(3*interval.Hours()))
	}
	// 再累計 2 個區間 → 應為 5 個區間總量，而非被覆蓋為 2。
	s2.pollOnce(ctx)
	s2.pollOnce(ctx)

	id := queue.EventID(testToken, "2026-07-17", queue.PathComputer)
	e, _, _ := q.Get(ctx, id)
	want := round6(5 * interval.Hours())
	if got := e.Payload["pc_active_hours"].(float64); got != want {
		t.Errorf("after restart pc_active_hours = %v; want %v (continued, not overwritten)", got, want)
	}
}

// TestDateRollover：每區間連續輪詢跨過午夜（gap 僅一個區間、非掛起），產生兩筆各自累計
// 的事件。跨日與「相隔一整天無輪詢＝掛起」不同：後者由 suspend 偵測攔截（見
// TestSuspendGapExcluded），此處驗證的是正常連續運轉下的日界切換。
func TestDateRollover(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	act := &fakeActivity{idle: time.Second}
	cpu := &fakeCPU{pct: 10}
	interval := 60 * time.Second
	clk := newStepClock("2026-07-17 23:59:30") // 午夜前 30 秒

	s := New(q, fakeEnroll{testToken}, act, cpu, interval, WithNow(clk.now))

	s.pollOnce(ctx) // 2026-07-17 23:59:30 → 第一天
	clk.add(interval)
	s.pollOnce(ctx) // 2026-07-18 00:00:30 → 第二天（gap=60s，非掛起）

	n, _ := q.Count(ctx)
	if n != 2 {
		t.Fatalf("queue count = %d; want 2 (one per day)", n)
	}
}

// fakePower 是可控的即時功耗量測（Step 1.5 seam 測試用）。
type fakePower struct {
	watts     float64
	supported bool
}

func (f fakePower) Available() error {
	if !f.supported {
		return context.Canceled // 任意非 nil 錯誤，模擬不支援
	}
	return nil
}
func (f fakePower) PowerWatts() (float64, bool, error) { return f.watts, f.supported, nil }

// TestPowerOverlayDefaultOmitted：預設不支援即時功耗，payload 不含 pc_power_w（回退加權模型）。
func TestPowerOverlayDefaultOmitted(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	s := New(q, fakeEnroll{testToken}, &fakeActivity{idle: time.Second}, &fakeCPU{pct: 30},
		60*time.Second, WithNow(fixedNow("2026-07-17")))
	s.pollOnce(ctx)

	id := queue.EventID(testToken, "2026-07-17", queue.PathComputer)
	e, _, _ := q.Get(ctx, id)
	if _, present := e.Payload["pc_power_w"]; present {
		t.Fatalf("pc_power_w present with unsupported power sampler; want omitted")
	}
}

// TestPowerOverlaySeam：注入「支援」的功耗量測時，seam 正確把 pc_power_w 附入 payload。
// 驗證 Step 1.5 的結構接點可用（平台實作就緒後即插即用），現階段預設仍不啟用。
func TestPowerOverlaySeam(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	s := New(q, fakeEnroll{testToken}, &fakeActivity{idle: time.Second}, &fakeCPU{pct: 30},
		60*time.Second, WithNow(fixedNow("2026-07-17")),
		WithPowerSampler(fakePower{watts: 42.5, supported: true}))
	s.pollOnce(ctx)

	id := queue.EventID(testToken, "2026-07-17", queue.PathComputer)
	e, _, _ := q.Get(ctx, id)
	got, present := e.Payload["pc_power_w"]
	if !present {
		t.Fatalf("pc_power_w absent with supported power sampler; want present")
	}
	if got.(float64) != 42.5 {
		t.Fatalf("pc_power_w = %v; want 42.5", got)
	}
}

// stepClock 是可推進的假時鐘（回傳無 monotonic 讀數的牆鐘時間），供 sleep/喚醒測試。
type stepClock struct{ t time.Time }

func newStepClock(base string) *stepClock {
	ts, _ := time.Parse("2006-01-02 15:04:05", base)
	return &stepClock{t: ts}
}
func (c *stepClock) now() time.Time      { return c.t }
func (c *stepClock) add(d time.Duration) { c.t = c.t.Add(d) }

// TestSuspendGapExcluded：牆鐘跳躍超過門檻（模擬 sleep/hibernate）時，該段不計 active/idle。
func TestSuspendGapExcluded(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	act := &fakeActivity{idle: time.Second} // 皆 active
	cpu := &fakeCPU{pct: 50}
	interval := 60 * time.Second
	clk := newStepClock("2026-07-17 09:00:00")

	s := New(q, fakeEnroll{testToken}, act, cpu, interval,
		WithNow(clk.now), WithSuspendGapThreshold(3*interval))

	// 兩個正常區間。
	s.pollOnce(ctx)
	clk.add(interval)
	s.pollOnce(ctx)
	afterTwo := s.acc.activeHours

	// 模擬 sleep 8 小時（牆鐘跳躍 >> 門檻）。
	clk.add(8 * time.Hour)
	s.pollOnce(ctx) // 應偵測掛起、整段跳過，不累計

	if s.acc.activeHours != afterTwo {
		t.Fatalf("active hours changed across suspend: %v -> %v; want unchanged", afterTwo, s.acc.activeHours)
	}
	// 掛起後恢復正常：再一個區間應正常累計。
	clk.add(interval)
	s.pollOnce(ctx)
	if got, want := round6(s.acc.activeHours), round6(afterTwo+interval.Hours()); got != want {
		t.Fatalf("post-resume active hours = %v; want %v (normal accounting resumed)", got, want)
	}
}

// TestSubThresholdGapCountsNormally：小於門檻的延遲（排程抖動）仍以名目區間計時。
func TestSubThresholdGapCountsNormally(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	act := &fakeActivity{idle: time.Second}
	cpu := &fakeCPU{pct: 50}
	interval := 60 * time.Second
	clk := newStepClock("2026-07-17 09:00:00")

	s := New(q, fakeEnroll{testToken}, act, cpu, interval,
		WithNow(clk.now), WithSuspendGapThreshold(3*interval))

	s.pollOnce(ctx)
	clk.add(2 * interval) // 延遲但 < 3×門檻
	s.pollOnce(ctx)

	// 兩次輪詢皆應計一個名目區間（不因 gap=2×interval 而多攤，也不誤判掛起）。
	if got, want := round6(s.acc.activeHours), round6(2*interval.Hours()); got != want {
		t.Fatalf("active hours = %v; want %v (nominal interval per poll)", got, want)
	}
}

// TestSkipIntervalOnSampleError：取樣失敗整段跳過，不累計、不入列。
func TestSkipIntervalOnSampleError(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	act := &fakeActivity{idle: time.Second}
	cpu := &fakeCPU{pct: 50}
	s := New(q, fakeEnroll{testToken}, act, cpu, 60*time.Second,
		WithNow(fixedNow("2026-07-17")))

	cpu.err = context.DeadlineExceeded // 模擬 CPU 取樣失敗
	s.pollOnce(ctx)

	n, _ := q.Count(ctx)
	if n != 0 {
		t.Fatalf("queue count = %d; want 0 (interval skipped on sample error)", n)
	}
	if s.acc.totalHours() != 0 {
		t.Fatalf("accumulated %v hours; want 0 on skipped interval", s.acc.totalHours())
	}
}
