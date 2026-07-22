package printer

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"eco-agent/internal/queue"
)

// ── 測試替身 ──

// fakeSampler 依序回傳預設的 page counter 值（用盡後停在最後一個），並計數呼叫次數。
// err 非 nil 時一律回錯誤，用於模擬印表機不可達。
type fakeSampler struct {
	values []int64
	err    error
	calls  int
}

func (f *fakeSampler) PageCounter(_ context.Context) (int64, error) {
	f.calls++
	if f.err != nil {
		return 0, f.err
	}
	if len(f.values) == 0 {
		return 0, errors.New("fakeSampler: no values")
	}
	i := f.calls - 1
	if i >= len(f.values) {
		i = len(f.values) - 1
	}
	return f.values[i], nil
}

type fakeEnroll struct {
	token string
	err   error
}

func (f fakeEnroll) IDToken() (string, error) { return f.token, f.err }

const testToken = "test-idtoken-B"

// clock 是可推進的假時鐘。
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newTestQueue(t *testing.T) *queue.Queue {
	t.Helper()
	q, err := queue.Open(context.Background(), filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

const (
	testCheckInterval = 5 * time.Second   // 測試不實際掛 ticker，直接呼叫 checkAndPoll
	testPollInterval  = 300 * time.Second // 到期門檻
)

func newSensor(q *queue.Queue, sampler PageCounterSampler, clk *clock, enr idTokenProvider) *Sensor {
	if enr == nil {
		enr = fakeEnroll{token: testToken}
	}
	return NewSensor(q, enr, sampler, testCheckInterval, testPollInterval, WithSensorNow(clk.now))
}

// todayEvent 讀回當日的路徑 B 事件。
func todayEvent(t *testing.T, q *queue.Queue, clk *clock) (queue.Event, bool) {
	t.Helper()
	date := clk.now().Format("2006-01-02")
	e, ok, err := q.Get(context.Background(), queue.EventID(testToken, date, queue.PathPrinter))
	if err != nil {
		t.Fatalf("queue.Get: %v", err)
	}
	return e, ok
}

func printPages(t *testing.T, e queue.Event) int64 {
	t.Helper()
	v, ok := e.Payload["print_pages"]
	if !ok {
		t.Fatalf("payload 缺 print_pages：%v", e.Payload)
	}
	f, ok := v.(float64) // payload 經 JSON 往返，數值型別為 float64
	if !ok {
		t.Fatalf("print_pages 型別非數值：%T", v)
	}
	return int64(f)
}

// ── 3.2 觸發模型 ──

// 冷啟動：無時間戳 → 首次巡檢即查；首次只建立基準、不入列（不可把 life count 當增量）。
func TestColdStartEstablishesBaselineOnly(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	s := newSensor(q, &fakeSampler{values: []int64{1000}}, clk, nil)

	s.checkAndPoll(ctx)

	if _, ok := todayEvent(t, q, clk); ok {
		t.Error("首次輪詢不應入列（無前值可減）")
	}
	st := s.loadState(ctx)
	if st.LastPageCount != 1000 {
		t.Errorf("基準 = %d, want 1000", st.LastPageCount)
	}
	if _, ok, _ := q.GetState(ctx, StateKeyLastPoll); !ok {
		t.Error("應寫入 lastPrinterPollAt 時間戳")
	}
}

// 未到期不查；到期才查（沿用 Step 2 的持久化時間戳到期判斷，非絕對計時器）。
func TestPollsOnlyWhenDue(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	sampler := &fakeSampler{values: []int64{1000, 1005}}
	s := newSensor(q, sampler, clk, nil)

	s.checkAndPoll(ctx) // 冷啟動：查一次建立基準
	if sampler.calls != 1 {
		t.Fatalf("冷啟動應查 1 次，實際 %d", sampler.calls)
	}

	clk.add(testPollInterval - time.Second) // 差一秒到期
	s.checkAndPoll(ctx)
	if sampler.calls != 1 {
		t.Errorf("未到期不應查，實際累計 %d 次", sampler.calls)
	}

	clk.add(time.Second) // 剛好到期
	s.checkAndPoll(ctx)
	if sampler.calls != 2 {
		t.Errorf("到期應再查，實際累計 %d 次", sampler.calls)
	}
	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("到期輪詢有增量時應入列")
	}
	if got := printPages(t, e); got != 5 {
		t.Errorf("print_pages = %d, want 5", got)
	}
}

// 開機補查：預置很久以前的時間戳 → 新感測器啟動的首次巡檢即補查（與「開機後檢查」合流）。
func TestBootCatchUp(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}

	old := clk.now().Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)
	if err := q.SetState(ctx, StateKeyLastPoll, old); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	sampler := &fakeSampler{values: []int64{2000}}
	newSensor(q, sampler, clk, nil).checkAndPoll(ctx)

	if sampler.calls != 1 {
		t.Errorf("距上次輪詢已逾門檻，啟動即應補查，實際 %d 次", sampler.calls)
	}
}

// 時間戳無法解析視為「已到期」（保守，寧可多查一次也不漏採）。
func TestUnparsableTimestampTreatedAsDue(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Now()}
	if err := q.SetState(ctx, StateKeyLastPoll, "not-a-timestamp"); err != nil {
		t.Fatalf("SetState: %v", err)
	}

	sampler := &fakeSampler{values: []int64{10}}
	newSensor(q, sampler, clk, nil).checkAndPoll(ctx)

	if sampler.calls != 1 {
		t.Errorf("無法解析的時間戳應視為到期，實際查 %d 次", sampler.calls)
	}
}

// ── 3.3 增量累計與 payload ──

// 同一天多次輪詢：payload 為「當日累計」增量頁數，事件 ID 固定故 upsert 同一筆。
func TestAccumulatesDailyPages(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	s := newSensor(q, &fakeSampler{values: []int64{1000, 1003, 1010}}, clk, nil)

	s.checkAndPoll(ctx) // 基準 1000
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // +3
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // +7 → 累計 10

	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("應有當日事件")
	}
	if got := printPages(t, e); got != 10 {
		t.Errorf("print_pages = %d, want 10（3+7 當日累計）", got)
	}
	if n, _ := q.Count(ctx); n != 1 {
		t.Errorf("當日應僅一筆事件（upsert），實際 %d 筆", n)
	}
	if got := e.Payload["date"]; got != "2026-07-22" {
		t.Errorf("date = %v, want 2026-07-22", got)
	}
	// 純感測：payload 只有感測值，不得夾帶任何能耗換算結果或係數。
	if len(e.Payload) != 2 {
		t.Errorf("payload 應只有 date 與 print_pages，實際：%v", e.Payload)
	}
}

// 期間沒列印（增量 0）不入列空事件，但基準與時間戳照樣推進。
func TestNoDeltaDoesNotEnqueue(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	s := newSensor(q, &fakeSampler{values: []int64{500, 500}}, clk, nil)

	s.checkAndPoll(ctx)
	clk.add(testPollInterval)
	s.checkAndPoll(ctx)

	if n, _ := q.Count(ctx); n != 0 {
		t.Errorf("無增量不應入列，實際 %d 筆", n)
	}
	if st := s.loadState(ctx); st.LastPageCount != 500 {
		t.Errorf("基準 = %d, want 500", st.LastPageCount)
	}
}

// 跨日：新日期起算新的一筆事件，舊日事件保留原累計。
func TestCrossDayStartsNewEvent(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 23, 50, 0, 0, time.UTC)}
	s := newSensor(q, &fakeSampler{values: []int64{100, 104, 109}}, clk, nil)

	s.checkAndPoll(ctx) // 基準 100
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // 22 日 +4
	day1, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("應有 22 日事件")
	}
	if got := printPages(t, day1); got != 4 {
		t.Fatalf("22 日 print_pages = %d, want 4", got)
	}

	clk.add(24 * time.Hour) // 跨到 23 日
	s.checkAndPoll(ctx)     // +5 應計入新的一天

	day2, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("應有 23 日事件")
	}
	if got := printPages(t, day2); got != 5 {
		t.Errorf("23 日 print_pages = %d, want 5（不含前一日的 4）", got)
	}
	if n, _ := q.Count(ctx); n != 2 {
		t.Errorf("應為兩日各一筆，實際 %d 筆", n)
	}
}

// counter 重置（換機／韌體重置）：不回補、以新值為基準重新起算。
func TestCounterResetRebasesWithoutBackfill(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	s := newSensor(q, &fakeSampler{values: []int64{5000, 12, 15}}, clk, nil)

	s.checkAndPoll(ctx) // 基準 5000
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // 讀到 12（重置）→ 不入列、改以 12 為基準

	if n, _ := q.Count(ctx); n != 0 {
		t.Errorf("counter 重置不應入列任何頁數，實際 %d 筆", n)
	}
	if st := s.loadState(ctx); st.LastPageCount != 12 {
		t.Errorf("重置後基準 = %d, want 12", st.LastPageCount)
	}

	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // 15 - 12 = 3
	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("重置後的增量仍應正常入列")
	}
	if got := printPages(t, e); got != 3 {
		t.Errorf("print_pages = %d, want 3", got)
	}
}

// 重啟：基準與當日累計由持久化狀態讀回，續accumulate 而非從零覆蓋。
func TestStateSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}

	s1 := newSensor(q, &fakeSampler{values: []int64{1000, 1006}}, clk, nil)
	s1.checkAndPoll(ctx)
	clk.add(testPollInterval)
	s1.checkAndPoll(ctx) // 當日累計 6

	// 模擬重啟：全新 Sensor 實例，共用同一份佇列/狀態。
	s2 := newSensor(q, &fakeSampler{values: []int64{1009}}, clk, nil)
	clk.add(testPollInterval)
	s2.checkAndPoll(ctx) // +3 → 應為 9 而非 3

	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("應有當日事件")
	}
	if got := printPages(t, e); got != 9 {
		t.Errorf("print_pages = %d, want 9（重啟後續累計）", got)
	}
}

// ── 失敗處理（3.2 重試語意／3.4 BYOD）──

// 印表機不可達：不更新時間戳，下次巡檢自然重試；不入列、不推進基準。
func TestUnreachableDoesNotAdvanceTimestamp(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	sampler := &fakeSampler{err: errors.New("i/o timeout")}
	s := newSensor(q, sampler, clk, nil)

	s.checkAndPoll(ctx)
	if _, ok, _ := q.GetState(ctx, StateKeyLastPoll); ok {
		t.Error("查詢失敗不應更新時間戳")
	}
	// 時間未推進，但因時間戳未寫入仍視為到期 → 下次巡檢立即重試。
	s.checkAndPoll(ctx)
	if sampler.calls != 2 {
		t.Errorf("失敗後應於下次巡檢重試，實際查 %d 次", sampler.calls)
	}
	if n, _ := q.Count(ctx); n != 0 {
		t.Errorf("查詢失敗不應入列，實際 %d 筆", n)
	}
}

// 無法取得 ID Token（未綁定／已撤銷）：不入列、不推進狀態與時間戳，取得後下次輪詢照常。
func TestIDTokenUnavailableDefersEverything(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	sampler := &fakeSampler{values: []int64{1000, 1004}}

	s := newSensor(q, sampler, clk, fakeEnroll{err: errors.New("not bound")})
	s.checkAndPoll(ctx) // 建立基準（不需 token）
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // 有增量但無 token → 不入列

	if n, _ := q.Count(ctx); n != 0 {
		t.Fatalf("無 ID Token 不應入列，實際 %d 筆", n)
	}
	if st := s.loadState(ctx); st.LastPageCount != 1000 {
		t.Errorf("入列失敗不應推進基準（下次由同一基準重算），實際 %d", st.LastPageCount)
	}

	// 取得 token 後：由同一基準重算，增量不遺失也不重複。
	s2 := newSensor(q, &fakeSampler{values: []int64{1004}}, clk, nil)
	clk.add(testPollInterval)
	s2.checkAndPoll(ctx)

	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("取得 token 後應入列")
	}
	if got := printPages(t, e); got != 4 {
		t.Errorf("print_pages = %d, want 4（不遺失也不重複）", got)
	}
}

// Run：印表機一直不可達也不得阻塞或回錯誤（3.4「不使 Agent 卡住」）。
func TestRunDoesNotBlockWhenUnreachable(t *testing.T) {
	q := newTestQueue(t)
	clk := &clock{t: time.Now()}
	s := NewSensor(q, fakeEnroll{token: testToken}, &fakeSampler{err: errors.New("no route to host")},
		20*time.Millisecond, testPollInterval, WithSensorNow(clk.now))

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- s.Run(ctx) }()

	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run 應優雅結束，實際回錯誤：%v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run 未於 ctx 取消後結束（卡住）")
	}
}

// 未提供 sampler（本機無個人專屬印表機）：Run 立即優雅結束，不影響其餘路徑。
func TestRunWithoutSamplerDisablesPath(t *testing.T) {
	q := newTestQueue(t)
	clk := &clock{t: time.Now()}
	s := NewSensor(q, fakeEnroll{token: testToken}, nil, testCheckInterval, testPollInterval,
		WithSensorNow(clk.now))

	if err := s.Run(context.Background()); err != nil {
		t.Errorf("無 sampler 時 Run 應回 nil，實際：%v", err)
	}
}

// 端到端（本機 mock SNMP responder）：確認增量頁數正確且歸戶到 ID Token（3.V）。
func TestSensorWithMockSNMPAgent(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}

	client, agent := newTestClient(t, map[string]uint64{DefaultPageCounterOID: 800})
	s := newSensor(q, client, clk, nil)

	s.checkAndPoll(ctx) // 基準 800
	agent.SetValue(DefaultPageCounterOID, 812)
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // +12

	date := clk.now().Format("2006-01-02")
	e, ok, err := q.Get(ctx, queue.EventID(testToken, date, queue.PathPrinter))
	if err != nil {
		t.Fatalf("queue.Get: %v", err)
	}
	if !ok {
		t.Fatal("應有當日事件")
	}
	if got := printPages(t, e); got != 12 {
		t.Errorf("print_pages = %d, want 12", got)
	}
	if e.PathType != queue.PathPrinter {
		t.Errorf("PathType = %s, want %s", e.PathType, queue.PathPrinter)
	}
}
