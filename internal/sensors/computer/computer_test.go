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

// TestDateRollover：跨午夜切換日期，產生兩筆各自累計的事件。
func TestDateRollover(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	act := &fakeActivity{idle: time.Second}
	cpu := &fakeCPU{pct: 10}

	day := "2026-07-17"
	nowFn := func() time.Time {
		ts, _ := time.Parse("2006-01-02", day)
		return ts
	}
	s := New(q, fakeEnroll{testToken}, act, cpu, 60*time.Second, WithNow(nowFn))

	s.pollOnce(ctx) // 第一天
	day = "2026-07-18"
	s.pollOnce(ctx) // 第二天

	n, _ := q.Count(ctx)
	if n != 2 {
		t.Fatalf("queue count = %d; want 2 (one per day)", n)
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
