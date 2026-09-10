package uploader

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
)

// harness 組出 queue + enroll + mock server + uploader，指向 httptest。
type harness struct {
	q      *queue.Queue
	enr    *enroll.Enroller
	mock   *MockIngestServer
	server *httptest.Server
	up     *Uploader
}

func newHarness(t *testing.T, cfg config.Config) *harness {
	t.Helper()
	ctx := context.Background()

	q, err := queue.Open(ctx, filepath.Join(t.TempDir(), "queue.db"))
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { q.Close() })

	enr := enroll.New(platform.NewMemoryKeychain(), q)
	if err := enr.EnsureBound(ctx); err != nil {
		t.Fatalf("EnsureBound: %v", err)
	}

	mock := NewMockIngestServer(http.StatusOK)
	server := httptest.NewServer(mock.Handler())
	t.Cleanup(server.Close)

	// 靜音日誌避免測試輸出雜訊。
	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	up := New(q, enr, cfg,
		WithUploadURL(server.URL+"/mock/ingest"),
		WithLogger(quiet),
	)
	return &harness{q: q, enr: enr, mock: mock, server: server, up: up}
}

func testCfg() config.Config {
	c := config.LoadProfile(config.ProfileTesting)
	return c
}

func enqueueN(t *testing.T, q *queue.Queue, p queue.PathType, n int) {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < n; i++ {
		date := time.Date(2026, 7, 1+i, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		e := queue.Event{
			ID:        queue.EventID("mock-emp-idtoken-eco-0001", date, p),
			PathType:  p,
			UsageDate: date,
			Payload:   map[string]any{"pc_active_hours": 1.0},
		}
		if err := q.Enqueue(ctx, e); err != nil {
			t.Fatalf("Enqueue: %v", err)
		}
	}
}

func mustCount(t *testing.T, q *queue.Queue) int {
	t.Helper()
	n, err := q.Count(context.Background())
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	return n
}

// TestFlushClearsOnlyOn200 驗證正常上傳：mock 收到、佇列於 200 後清空。
func TestFlushClearsOnlyOn200(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, testCfg())
	enqueueN(t, h.q, queue.PathComputer, 5)

	if err := h.up.Flush(ctx, ReasonManual); err != nil {
		t.Fatalf("Flush: %v", err)
	}
	if got := h.mock.EventCount(); got != 5 {
		t.Fatalf("mock received %d events, want 5", got)
	}
	if n := mustCount(t, h.q); n != 0 {
		t.Fatalf("queue count = %d, want 0 (cleared after 200)", n)
	}
}

// TestAtLeastOnceKeepOnNon200 驗證非 200 不清除，200 後才清（搭下次觸發重送）。
func TestAtLeastOnceKeepOnNon200(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, testCfg())
	enqueueN(t, h.q, queue.PathComputer, 3)

	// 後端不可用（500）：資料須保留。
	h.mock.SetStatus(http.StatusInternalServerError)
	if err := h.up.Flush(ctx, ReasonManual); err != nil {
		t.Fatalf("Flush(500): %v", err)
	}
	if n := mustCount(t, h.q); n != 3 {
		t.Fatalf("queue count = %d after 500, want 3 (must not clear)", n)
	}

	// 恢復（200）：下次觸發重送並清除。
	h.mock.SetStatus(http.StatusOK)
	if err := h.up.Flush(ctx, ReasonManual); err != nil {
		t.Fatalf("Flush(200): %v", err)
	}
	if n := mustCount(t, h.q); n != 0 {
		t.Fatalf("queue count = %d after 200, want 0", n)
	}
}

// TestRevocationSelfClear 驗證 401 → 自清憑證、停止上傳、資料保留。
func TestRevocationSelfClear(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, testCfg())
	enqueueN(t, h.q, queue.PathComputer, 3)

	h.mock.SetStatus(http.StatusUnauthorized)
	err := h.up.Flush(ctx, ReasonManual)
	if !errors.Is(err, ErrStopped) {
		t.Fatalf("Flush err = %v, want ErrStopped", err)
	}
	if !h.up.Stopped() {
		t.Fatal("uploader not stopped after 401")
	}
	// 憑證已自清（enroll 進入撤銷終止態）。
	if _, err := h.enr.IDToken(); !errors.Is(err, enroll.ErrRevoked) {
		t.Fatalf("IDToken after revoke = %v, want ErrRevoked", err)
	}
	// 撤銷前未成功上傳，資料保留（未清除）。
	if n := mustCount(t, h.q); n != 3 {
		t.Fatalf("queue count = %d after revoke, want 3", n)
	}
	// 再次 Flush 仍為停止態。
	if err := h.up.Flush(ctx, ReasonManual); !errors.Is(err, ErrStopped) {
		t.Fatalf("second Flush = %v, want ErrStopped", err)
	}
}

// TestAllPathsSingleHTTPSBatch 驗證 [D13]：三路徑不再分流協定，同一次 flush 只發一次
// HTTPS 請求、整批一起送達（不再依路徑拆成兩批）。
func TestAllPathsSingleHTTPSBatch(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, testCfg())
	enqueueN(t, h.q, queue.PathComputer, 2)
	enqueueN(t, h.q, queue.PathPrinter, 1)
	enqueueN(t, h.q, queue.PathDrive, 2)

	if err := h.up.Flush(ctx, ReasonManual); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got := h.mock.Received()
	if len(got) != 1 {
		t.Fatalf("mock received %d batches, want 1 (三路徑共用單一 HTTPS 批次)", len(got))
	}
	if n := len(got[0].Events); n != 5 {
		t.Errorf("batch events = %d, want 5 (computer 2 + printer 1 + drive 2)", n)
	}
	if n := mustCount(t, h.q); n != 0 {
		t.Fatalf("queue count = %d, want 0", n)
	}
}

// TestFlatRecordOnWire 驗證上送記錄為**扁平**單層（v20 §4.4）：共同欄位
// usage_date／path_type／collected_at 與該路徑量值同層，不另包 payload 物件；
// 且 collected_at 為 UTC RFC3339Nano，供後端做亂序抵達勝出判定（[D14]）。
func TestFlatRecordOnWire(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t, testCfg())

	// 以非 UTC 時區的採集時戳入列，驗證上送前確實轉為 UTC。
	collectedAt := time.Date(2026, 7, 16, 17, 30, 15, 123456789, time.FixedZone("CST", 8*3600))
	e := queue.Event{
		ID:          queue.EventID("mock-emp-idtoken-eco-0001", "2026-07-16", queue.PathComputer),
		PathType:    queue.PathComputer,
		UsageDate:   "2026-07-16",
		Payload:     map[string]any{"pc_active_hours": 1.0},
		CollectedAt: collectedAt,
	}
	if err := h.q.Enqueue(ctx, e); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if err := h.up.Flush(ctx, ReasonManual); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	got := h.mock.Received()
	if len(got) != 1 || len(got[0].Events) != 1 {
		t.Fatalf("mock received %d batches, want 1 batch × 1 event", len(got))
	}
	we := got[0].Events[0]
	if we.CollectedAt == "" {
		t.Fatal("collected_at 未上送（[D14] 要求每筆皆帶採集時間戳）")
	}
	parsed, err := time.Parse(time.RFC3339Nano, we.CollectedAt)
	if err != nil {
		t.Fatalf("collected_at %q 非 RFC3339Nano: %v", we.CollectedAt, err)
	}
	if !parsed.Equal(collectedAt) {
		t.Errorf("collected_at = %v, want %v（同一時刻）", parsed, collectedAt)
	}
	if _, offset := parsed.Zone(); offset != 0 {
		t.Errorf("collected_at 時區偏移 = %d, want 0（[D14] 規定 UTC）：%q", offset, we.CollectedAt)
	}
	// path_type 由 Agent 明送、不由後端推斷（[D12]），值域即 queue.PathType（兩端無翻譯層）。
	if we.PathType != string(queue.PathComputer) {
		t.Errorf("path_type = %q, want %q", we.PathType, queue.PathComputer)
	}
	if we.UsageDate != "2026-07-16" {
		t.Errorf("usage_date = %q, want 2026-07-16", we.UsageDate)
	}

	// 扁平：量值與共同欄位同層，且不得殘留巢狀 payload 物件或舊的 date 鍵。
	if got := we.Fields["pc_active_hours"]; got != 1.0 {
		t.Errorf("pc_active_hours = %v, want 1（量值須與共同欄位同層）；實收：%v", got, we.Fields)
	}
	if _, nested := we.Fields["payload"]; nested {
		t.Errorf("記錄仍含巢狀 payload 物件：%v", we.Fields)
	}
	if _, old := we.Fields["date"]; old {
		t.Errorf("記錄仍含舊的 date 鍵（應為 usage_date）：%v", we.Fields)
	}
}

// TestReservedFieldsWinOverPayload 驗證共同欄位恆取自 Event 的專屬欄位：即使 payload 內混入
// 同名鍵，攤平後也不得覆蓋——usage_date／path_type 是冪等唯一鍵組成，不可由量值 map 決定。
func TestReservedFieldsWinOverPayload(t *testing.T) {
	e := queue.Event{
		ID:        "evt-1",
		PathType:  queue.PathComputer,
		UsageDate: "2026-07-16",
		Payload: map[string]any{
			"usage_date": "1999-01-01", "path_type": "bogus",
			"event_id": "bogus", "collected_at": "bogus",
			"pc_active_hours": 1.0,
		},
		CollectedAt: time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC),
	}
	m := wireEventOf(e)
	for field, want := range map[string]any{
		"event_id": "evt-1", "path_type": string(queue.PathComputer),
		"usage_date": "2026-07-16", "collected_at": "2026-07-16T09:00:00Z",
	} {
		if m[field] != want {
			t.Errorf("%s = %v, want %v（共同欄位不可被 payload 同名鍵覆蓋）", field, m[field], want)
		}
	}
	if m["pc_active_hours"] != 1.0 {
		t.Errorf("量值遺失：%v", m)
	}
}

// TestDueTriggerThreshold 驗證累積達量觸發判斷。
func TestDueTriggerThreshold(t *testing.T) {
	ctx := context.Background()
	cfg := testCfg() // ThresholdCount = 3
	h := newHarness(t, cfg)

	enqueueN(t, h.q, queue.PathComputer, cfg.ThresholdCount-1)
	if _, due, err := h.up.dueTrigger(ctx); err != nil || due {
		t.Fatalf("dueTrigger below threshold = %v, %v; want not due", due, err)
	}
	enqueueN(t, h.q, queue.PathDrive, 1) // 達門檻（用不同路徑避免事件 ID 撞號）
	reason, due, err := h.up.dueTrigger(ctx)
	if err != nil || !due || reason != ReasonThreshold {
		t.Fatalf("dueTrigger at threshold = %v, %v, %v; want threshold/true", reason, due, err)
	}
}

// TestDueTriggerMaxAge 驗證最長滯留觸發判斷。
func TestDueTriggerMaxAge(t *testing.T) {
	ctx := context.Background()
	cfg := testCfg()
	cfg.MaxAge = 20 * time.Millisecond // 縮到毫秒級便於測試
	cfg.ThresholdCount = 1000          // 抬高避免達量搶先
	h := newHarness(t, cfg)

	enqueueN(t, h.q, queue.PathComputer, 1)
	time.Sleep(40 * time.Millisecond)

	reason, due, err := h.up.dueTrigger(ctx)
	if err != nil || !due || reason != ReasonMaxAge {
		t.Fatalf("dueTrigger after maxAge = %v, %v, %v; want maxage/true", reason, due, err)
	}
}

// TestRunStartupThenShutdown 驗證 Run 開機補送與關機搶送（ctx 取消）。
func TestRunStartupThenShutdown(t *testing.T) {
	cfg := testCfg()
	cfg.CheckInterval = time.Hour // 讓巡檢 ticker 不在測試期間觸發，隔離 startup/shutdown
	h := newHarness(t, cfg)

	enqueueN(t, h.q, queue.PathComputer, 2)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.up.Run(ctx) }()

	// 開機後檢查：啟動即補送既有 2 筆。
	waitFor(t, time.Second, func() bool { return h.mock.EventCount() == 2 })

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
	if n := mustCount(t, h.q); n != 0 {
		t.Fatalf("queue count = %d, want 0", n)
	}
}

func waitFor(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("condition not met within timeout")
}
