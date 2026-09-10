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
// err 非 nil 時一律回錯誤，用於模擬印表機不可達。序號另由 serial／serialErr 控制，
// 與 page counter 的成功/失敗互不影響（真實裝置也可能讀得到其一、讀不到另一）。
type fakeSampler struct {
	values []int64
	err    error
	calls  int

	serial    string
	serialErr error
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

func (f *fakeSampler) SerialNumber(_ context.Context) (string, error) {
	if f.serialErr != nil {
		return "", f.serialErr
	}
	if f.serial == "" {
		return "", ErrNoSerialNumber
	}
	return f.serial, nil
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

// pageCounterOf 讀回事件 payload 的 printer_page_counter。
func pageCounterOf(t *testing.T, e queue.Event) int64 {
	t.Helper()
	v, ok := e.Payload["printer_page_counter"]
	if !ok {
		t.Fatalf("payload 缺 printer_page_counter：%v", e.Payload)
	}
	f, ok := v.(float64) // payload 經 JSON 往返，數值型別為 float64
	if !ok {
		t.Fatalf("printer_page_counter 型別非數值：%T", v)
	}
	return int64(f)
}

// ── 3.2 觸發模型 ──

// 冷啟動：無時間戳 → 首次巡檢即查；讀到即原樣入列（[D15] 起無需先建立基準）。
func TestColdStartEnqueuesImmediately(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	s := newSensor(q, &fakeSampler{values: []int64{1000}}, clk, nil)

	s.checkAndPoll(ctx)

	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("首次輪詢應直接入列（[D15]：原樣送出絕對讀數，不需先建立基準）")
	}
	if got := pageCounterOf(t, e); got != 1000 {
		t.Errorf("printer_page_counter = %d, want 1000", got)
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

	s.checkAndPoll(ctx) // 到期即查並入列
	if sampler.calls != 1 {
		t.Fatalf("首次應查 1 次，實際 %d", sampler.calls)
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
		t.Fatal("到期輪詢應 upsert 同一筆事件")
	}
	if got := pageCounterOf(t, e); got != 1005 {
		t.Errorf("printer_page_counter = %d, want 1005（最新讀數，非累加）", got)
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

// ── 3.3 送出（[D15]：原樣送出絕對讀數）──

// 同一天多次輪詢：payload 恆為「本次讀到的最新絕對值」，事件 ID 固定故 upsert 同一筆
// ——不再本機累加，差分留給後端以「當日最新讀數－前一日最新讀數」計算。
func TestEachPollUpsertsLatestAbsoluteReading(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	s := newSensor(q, &fakeSampler{values: []int64{1000, 1003, 1010}}, clk, nil)

	s.checkAndPoll(ctx) // 讀到 1000，入列
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // 讀到 1003，upsert 覆蓋
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // 讀到 1010，upsert 覆蓋

	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("應有當日事件")
	}
	if got := pageCounterOf(t, e); got != 1010 {
		t.Errorf("printer_page_counter = %d, want 1010（最新讀數，非累加）", got)
	}
	if n, _ := q.Count(ctx); n != 1 {
		t.Errorf("當日應僅一筆事件（upsert），實際 %d 筆", n)
	}
	// usage_date 為共同欄位，存於事件專屬欄而非 payload 內。
	if got := e.UsageDate; got != "2026-07-22" {
		t.Errorf("UsageDate = %v, want 2026-07-22", got)
	}
	// [D14]：採集時間戳取自感測器時鐘，且隨每次 upsert 前進到最後一次輪詢的時刻。
	if want := clk.now(); !e.CollectedAt.Equal(want) {
		t.Errorf("CollectedAt = %v, want %v（最後一次採集的時刻）", e.CollectedAt, want)
	}
}

// counter 不增（期間沒列印）或倒退（counter 重置）皆原樣送出——重置防呆已移至後端
// （[D15]：多觀測者讀到的是同一個絕對值，不需要 Agent 端自行判斷是否為「重置」）。
func TestFlatOrDecreasingReadingStillEnqueued(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	s := newSensor(q, &fakeSampler{values: []int64{5000, 5000, 12}}, clk, nil)

	s.checkAndPoll(ctx) // 5000
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // 仍 5000（無列印）
	e, ok := todayEvent(t, q, clk)
	if !ok || pageCounterOf(t, e) != 5000 {
		t.Fatalf("無列印仍應原樣入列 5000，實際 ok=%v e=%v", ok, e.Payload)
	}

	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // 讀到 12（換機／韌體重置）→ 原樣送出，不在本機判斷或回補
	e, ok = todayEvent(t, q, clk)
	if !ok {
		t.Fatal("counter 重置後仍應正常入列")
	}
	if got := pageCounterOf(t, e); got != 12 {
		t.Errorf("printer_page_counter = %d, want 12（原樣送出，重置防呆在後端）", got)
	}
}

// 跨日：新日期起算新的一筆事件，各自持有自己那次輪詢讀到的絕對值。
func TestCrossDayStartsNewEvent(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 23, 50, 0, 0, time.UTC)}
	s := newSensor(q, &fakeSampler{values: []int64{100, 104, 109}}, clk, nil)

	s.checkAndPoll(ctx) // 22 日：100
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // 仍 22 日（區間未跨午夜）：104
	day1, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("應有 22 日事件")
	}
	if got := pageCounterOf(t, day1); got != 104 {
		t.Fatalf("22 日 printer_page_counter = %d, want 104", got)
	}

	clk.add(24 * time.Hour) // 跨到 23 日
	s.checkAndPoll(ctx)     // 109 應計入新的一天

	day2, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("應有 23 日事件")
	}
	if got := pageCounterOf(t, day2); got != 109 {
		t.Errorf("23 日 printer_page_counter = %d, want 109", got)
	}
	if n, _ := q.Count(ctx); n != 2 {
		t.Errorf("應為兩日各一筆，實際 %d 筆", n)
	}
}

// 重啟：全新 Sensor 實例（無任何本機基準狀態可言，[D15] 起已無跨重啟差分狀態）
// 直接讀到什麼就送什麼，upsert 覆蓋同一筆事件。
func TestRestartHasNoLocalBaselineToLose(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}

	s1 := newSensor(q, &fakeSampler{values: []int64{1000}}, clk, nil)
	s1.checkAndPoll(ctx)

	// 模擬重啟：全新 Sensor 實例，共用同一份佇列/狀態。
	s2 := newSensor(q, &fakeSampler{values: []int64{1009}}, clk, nil)
	clk.add(testPollInterval)
	s2.checkAndPoll(ctx)

	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("應有當日事件")
	}
	if got := pageCounterOf(t, e); got != 1009 {
		t.Errorf("printer_page_counter = %d, want 1009（重啟不影響：原樣送出最新讀數）", got)
	}
}

// ── 序號（[D14] 缺口二）──

// 序號可得：payload 應帶 printer_serial。
func TestSerialIncludedWhenAvailable(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	sampler := &fakeSampler{values: []int64{1000}, serial: "SN-001"}
	s := newSensor(q, sampler, clk, nil)

	s.checkAndPoll(ctx)

	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("應有當日事件")
	}
	if got, _ := e.Payload["printer_serial"].(string); got != "SN-001" {
		t.Errorf("printer_serial = %q, want SN-001", got)
	}
}

// 序號查無（三候選皆空）：不影響 page counter 入列，只省略 printer_serial 欄位——
// 由後端以 device_id 回退歸鍵並標記「印表機身份不明」（[D14]）。
func TestSerialUnavailableOmitsFieldButStillEnqueues(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	sampler := &fakeSampler{values: []int64{1000}} // serial 留空 → SerialNumber 回 ErrNoSerialNumber
	s := newSensor(q, sampler, clk, nil)

	s.checkAndPoll(ctx)

	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("序號查無不應阻擋 page counter 入列")
	}
	if got := pageCounterOf(t, e); got != 1000 {
		t.Errorf("printer_page_counter = %d, want 1000", got)
	}
	if _, present := e.Payload["printer_serial"]; present {
		t.Errorf("序號查無時 payload 不應帶 printer_serial，實際：%v", e.Payload)
	}
}

// 序號查詢本身出錯（如逾時，而非「查無」）：同樣降級為省略欄位，不阻擋 page counter。
func TestSerialQueryErrorOmitsFieldButStillEnqueues(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	sampler := &fakeSampler{values: []int64{1000}, serialErr: errors.New("i/o timeout")}
	s := newSensor(q, sampler, clk, nil)

	s.checkAndPoll(ctx)

	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("序號查詢出錯不應阻擋 page counter 入列")
	}
	if _, present := e.Payload["printer_serial"]; present {
		t.Errorf("序號查詢出錯時 payload 不應帶 printer_serial，實際：%v", e.Payload)
	}
}

// ── 失敗處理（3.2 重試語意／3.4 BYOD）──

// 印表機不可達：不更新時間戳，下次巡檢自然重試；不入列。
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

// 無法取得 ID Token（未綁定／已撤銷）：不入列、不推進時間戳，取得後下次輪詢照常。
func TestIDTokenUnavailableDefersEverything(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}
	sampler := &fakeSampler{values: []int64{1000, 1004}}

	s := newSensor(q, sampler, clk, fakeEnroll{err: errors.New("not bound")})
	s.checkAndPoll(ctx) // 讀到 1000，但無 token → 不入列

	if n, _ := q.Count(ctx); n != 0 {
		t.Fatalf("無 ID Token 不應入列，實際 %d 筆", n)
	}
	if _, ok, _ := q.GetState(ctx, StateKeyLastPoll); ok {
		t.Error("入列失敗不應推進時間戳（下次巡檢重試）")
	}

	// 取得 token 後：下次輪詢正常入列最新讀數。
	s2 := newSensor(q, &fakeSampler{values: []int64{1004}}, clk, nil)
	clk.add(testPollInterval)
	s2.checkAndPoll(ctx)

	e, ok := todayEvent(t, q, clk)
	if !ok {
		t.Fatal("取得 token 後應入列")
	}
	if got := pageCounterOf(t, e); got != 1004 {
		t.Errorf("printer_page_counter = %d, want 1004", got)
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

// 端到端（本機 mock SNMP responder）：確認絕對讀數與序號正確且歸戶到 ID Token（3.V）。
func TestSensorWithMockSNMPAgent(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	clk := &clock{t: time.Date(2026, 7, 22, 9, 0, 0, 0, time.UTC)}

	client, agent := newTestClient(t, map[string]uint64{DefaultPageCounterOID: 800})
	agent.SetString(DefaultSerialPrtGeneralOID, "SN-E2E-001")
	s := newSensor(q, client, clk, nil)

	s.checkAndPoll(ctx) // 800
	agent.SetValue(DefaultPageCounterOID, 812)
	clk.add(testPollInterval)
	s.checkAndPoll(ctx) // 812（原樣讀出，非增量）

	date := clk.now().Format("2006-01-02")
	e, ok, err := q.Get(ctx, queue.EventID(testToken, date, queue.PathPrinter))
	if err != nil {
		t.Fatalf("queue.Get: %v", err)
	}
	if !ok {
		t.Fatal("應有當日事件")
	}
	if got := pageCounterOf(t, e); got != 812 {
		t.Errorf("printer_page_counter = %d, want 812", got)
	}
	if got, _ := e.Payload["printer_serial"].(string); got != "SN-E2E-001" {
		t.Errorf("printer_serial = %q, want SN-E2E-001", got)
	}
	if e.PathType != queue.PathPrinter {
		t.Errorf("PathType = %s, want %s", e.PathType, queue.PathPrinter)
	}
}
