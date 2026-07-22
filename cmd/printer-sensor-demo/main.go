// Command printer-sensor-demo 是路徑 B 觸發模型與送出（Step 3.2–3.4）的獨立驗證（3.V）。
//
// 為求數十秒內看完且無需真實印表機，對本機 mock SNMP responder 輪詢，並把
// printerPollInterval/checkInterval 大幅縮短。示範：
//   - 冷啟動（3.2）：無時間戳 → 感測器一啟動即查，首次只建立基準、不入列；
//   - 到期即查＋增量累計（3.2/3.3）：mock 印表機的 page counter 增加後，到期輪詢取得增量，
//     以 payload {date, print_pages}（當日累計）入列，經四重觸發上傳到 mock 端點；
//   - 開機補查：預先寫入「很久以前」的時間戳 → 新感測器一啟動即立刻補查；
//   - BYOD 不可達（3.4）：關掉 mock 印表機 → 感測器只記 log、不入列、不卡住，恢復後續採。
//
// 真實部署改注入 printer.NewSNMPClientFromEnv()；未設 ECO_AGENT_PRINTER_HOST 時整條路徑 B 略過。
//
// 執行：go run ./cmd/printer-sensor-demo   （約 15 秒後自動結束，Ctrl+C 可提前）
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/sensors/printer"
	"eco-agent/internal/uploader"
)

// pollStateView 是 printer.StateKeyPollState 的唯讀視角（欄位對齊 printer 內部的 pollState），
// 供 demo 觀察基準與當日累計；正式流程不需要外部讀取這份狀態。
type pollStateView struct {
	LastPageCount int64  `json:"last_page_count"`
	DayDate       string `json:"day_date"`
	DayPages      int64  `json:"day_pages"`
}

func main() {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 縮短參數：數秒即到期，十餘秒內看完多輪輪詢。
	cfg := config.LoadProfile(config.ProfileTesting)
	cfg.CheckInterval = 500 * time.Millisecond
	cfg.PrinterPollInterval = 1 * time.Second
	cfg.ThresholdCount = 1 // 每筆即上傳，方便觀察 mock 端點收到
	cfg.MaxAge = 2 * time.Second

	dir, err := os.MkdirTemp("", "printer-sensor-demo-")
	if err != nil {
		fatal("mkdir temp", err)
	}
	defer os.RemoveAll(dir)

	q, err := queue.Open(ctx, filepath.Join(dir, "queue.db"))
	if err != nil {
		fatal("queue.Open", err)
	}
	enr := enroll.New(platform.NewMemoryKeychain())
	if err := enr.EnsureBound(ctx); err != nil {
		fatal("EnsureBound", err)
	}

	mock := uploader.NewMockIngestServer(200)
	ts := httptest.NewServer(mock.Handler())
	defer ts.Close()
	up := uploader.New(q, enr, cfg, uploader.WithUploadURL(ts.URL+"/mock/ingest"), uploader.WithLogger(log))

	// MOCK: 本機 SNMP responder 取代真實印表機，起始累計值 1000 頁。
	counter := uint64(1000)
	agent, err := printer.StartMockAgent("127.0.0.1:0", printer.DefaultCommunity,
		map[string]uint64{printer.DefaultPageCounterOID: counter})
	if err != nil {
		fatal("StartMockAgent", err)
	}
	host, port := agent.Addr()
	client, err := printer.NewSNMPClient(host, printer.WithPort(port))
	if err != nil {
		fatal("NewSNMPClient", err)
	}

	fmt.Printf("\n=== 路徑 B 觸發模型 demo（3.V）===\n")
	fmt.Printf("checkInterval=%v printerPollInterval=%v（持久化時間戳到期判斷，非絕對計時器）\n",
		cfg.CheckInterval, cfg.PrinterPollInterval)
	fmt.Printf("mock 印表機：%s:%d　OID：%s\n", host, port, printer.DefaultPageCounterOID)
	fmt.Printf("mock 端點：%s（路徑 B 走 %s）\n", ts.URL, uploader.ProtocolFor(queue.PathPrinter))

	// ── 情境 1：冷啟動只建立基準 ──
	banner("情境 1：冷啟動即查，首次只建立基準、不入列（3.2）")
	runCtx, cancel := context.WithCancel(ctx)
	go func() { _ = up.Run(runCtx) }()
	sensor := printer.NewSensor(q, enr, client, cfg.CheckInterval, cfg.PrinterPollInterval,
		printer.WithSensorLogger(log))
	go func() { _ = sensor.Run(runCtx) }()

	waitState(ctx, q, func(st pollStateView) bool { return st.LastPageCount == 1000 }, "基準 = 1000")
	fmt.Printf("  佇列待送=%d（首次不入列，因為沒有前值可減）\n", count(ctx, q))

	// ── 情境 2：列印後到期輪詢取得增量、當日累計、上傳 ──
	banner("情境 2：列印 5 頁 → 到期輪詢取增量並上傳（3.2/3.3）")
	counter += 5
	agent.SetValue(printer.DefaultPageCounterOID, counter)
	waitState(ctx, q, func(st pollStateView) bool { return st.DayPages == 5 }, "當日累計 print_pages = 5")
	fmt.Printf("  mock 端點累計收到 %d 筆事件\n", mock.EventCount())

	banner("情境 3：再列印 3 頁 → 同一天 upsert 同一筆事件，累計為 8（3.3）")
	counter += 3
	agent.SetValue(printer.DefaultPageCounterOID, counter)
	waitState(ctx, q, func(st pollStateView) bool { return st.DayPages == 8 }, "當日累計 print_pages = 8")
	fmt.Printf("  事件 ID 為 idToken+日期+printer，一天一筆；後端以此冪等 upsert 取最新累計\n")
	cancel()
	time.Sleep(300 * time.Millisecond) // 讓 goroutine 收斂

	// ── 情境 4：開機補查 ──
	banner("情境 4：開機補查 — 預置很久以前的時間戳，新感測器啟動即補查")
	old := time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)
	if err := q.SetState(ctx, printer.StateKeyLastPoll, old); err != nil {
		fatal("SetState", err)
	}
	fmt.Printf("  已將 %s 設為 10 天前：%s\n", printer.StateKeyLastPoll, old)
	counter += 2
	agent.SetValue(printer.DefaultPageCounterOID, counter)

	runCtx2, cancel2 := context.WithCancel(ctx)
	go func() { _ = up.Run(runCtx2) }()
	sensor2 := printer.NewSensor(q, enr, client, cfg.CheckInterval, cfg.PrinterPollInterval,
		printer.WithSensorLogger(log))
	go func() { _ = sensor2.Run(runCtx2) }()
	waitState(ctx, q, func(st pollStateView) bool { return st.DayPages == 10 }, "補查後當日累計 = 10")
	cancel2()
	time.Sleep(300 * time.Millisecond)

	// ── 情境 5：BYOD 印表機不可達 ──
	banner("情境 5：印表機不可達（不同網段／關機）— 只記 log、不入列、Agent 不卡住（3.4）")
	agent.Close()
	fmt.Println("  已關閉 mock 印表機，觀察感測器行為（應只有一筆 warn，不阻塞）...")

	before := count(ctx, q)
	runCtx3, cancel3 := context.WithTimeout(ctx, 3*time.Second)
	sensor3 := printer.NewSensor(q, enr, client, cfg.CheckInterval, cfg.PrinterPollInterval,
		printer.WithSensorLogger(log))
	done := make(chan struct{})
	go func() { _ = sensor3.Run(runCtx3); close(done) }()
	<-done
	cancel3()
	fmt.Printf("  ✓ 感測器已正常結束（未卡住）；佇列筆數 %d → %d（不可達期間不入列）\n",
		before, count(ctx, q))

	q.Close()
	fmt.Printf("\n=== demo 結束：冷啟動建立基準、到期取增量、當日累計 upsert、開機補查、不可達降級 ===\n")
}

// ── 小工具 ──

// waitState 輪詢持久化狀態直到條件成立（或逾時失敗）。
func waitState(ctx context.Context, q *queue.Queue, cond func(pollStateView) bool, what string) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if st, ok := readState(ctx, q); ok && cond(st) {
			fmt.Printf("  ✓ %s（基準=%d，日期=%s）\n", what, st.LastPageCount, st.DayDate)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fatal(fmt.Sprintf("等待「%s」逾時", what), nil)
}

func readState(ctx context.Context, q *queue.Queue) (pollStateView, bool) {
	v, ok, err := q.GetState(ctx, printer.StateKeyPollState)
	if err != nil || !ok {
		return pollStateView{}, false
	}
	var st pollStateView
	if err := json.Unmarshal([]byte(v), &st); err != nil {
		return pollStateView{}, false
	}
	return st, true
}

func count(ctx context.Context, q *queue.Queue) int {
	n, err := q.Count(ctx)
	if err != nil {
		fatal("Count", err)
	}
	return n
}

func banner(msg string) { fmt.Printf("\n── %s ──\n", msg) }

func fatal(msg string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo failed: %s: %v\n", msg, err)
	} else {
		fmt.Fprintf(os.Stderr, "demo failed: %s\n", msg)
	}
	os.Exit(1)
}
