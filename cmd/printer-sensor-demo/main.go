// Command printer-sensor-demo 是路徑 B 觸發模型與送出（Step 3.2–3.4）的獨立驗證（3.V）。
//
// 為求數十秒內看完且無需真實印表機，對本機 mock SNMP responder 輪詢，並把
// printerPollInterval/checkInterval 大幅縮短。示範：
//   - 冷啟動（3.2）：無時間戳 → 感測器一啟動即查，讀到即原樣入列（[D15] 起不需先建立基準）；
//   - 到期即查＋原樣送出最新讀數（3.2/3.3/[D15]）：mock 印表機的 page counter 增加後，
//     到期輪詢取得最新絕對值，以 payload {usage_date, printer_page_counter, printer_serial}
//     upsert 同一筆事件、經四重觸發上傳到 mock 端點——差分與 counter 重置防呆移至後端；
//   - 序號查無時的降級（[D14] 缺口二）：三候選 OID 皆空 → payload 省略 printer_serial，
//     由後端以 device_id 回退歸鍵並標記「印表機身份不明」；
//   - 開機補查：預先寫入「很久以前」的時間戳 → 新感測器一啟動即立刻補查；
//   - BYOD 不可達（3.4）：關掉 mock 印表機 → 感測器只記 log、不入列、不卡住。
//
// 真實部署改注入 printer.NewSNMPClientFromEnv()；未設 ECO_AGENT_PRINTER_HOST 時整條路徑 B 略過。
//
// 執行：go run ./cmd/printer-sensor-demo   （約 15 秒後自動結束，Ctrl+C 可提前）
package main

import (
	"context"
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
	enr := enroll.New(platform.NewMemoryKeychain(), q)
	if err := enr.EnsureBound(ctx); err != nil {
		fatal("EnsureBound", err)
	}

	mock := uploader.NewMockIngestServer(200)
	ts := httptest.NewServer(mock.Handler())
	defer ts.Close()
	up := uploader.New(q, enr, cfg, uploader.WithUploadURL(ts.URL+"/mock/ingest"), uploader.WithLogger(log))

	// MOCK: 本機 SNMP responder 取代真實印表機，起始累計值 1000 頁，序號 DEMO-SN-0001。
	counter := uint64(1000)
	agent, err := printer.StartMockAgent("127.0.0.1:0", printer.DefaultCommunity,
		map[string]uint64{printer.DefaultPageCounterOID: counter})
	if err != nil {
		fatal("StartMockAgent", err)
	}
	agent.SetString(printer.DefaultSerialPrtGeneralOID, "DEMO-SN-0001")
	host, port := agent.Addr()
	client, err := printer.NewSNMPClient(host, printer.WithPort(port))
	if err != nil {
		fatal("NewSNMPClient", err)
	}

	fmt.Printf("\n=== 路徑 B 觸發模型 demo（3.V）===\n")
	fmt.Printf("checkInterval=%v printerPollInterval=%v（持久化時間戳到期判斷，非絕對計時器）\n",
		cfg.CheckInterval, cfg.PrinterPollInterval)
	fmt.Printf("mock 印表機：%s:%d　page counter OID：%s　序號：DEMO-SN-0001\n",
		host, port, printer.DefaultPageCounterOID)
	fmt.Printf("mock 端點：%s（路徑 B 走 HTTPS，[D13] 起不再走 MQTT）\n", ts.URL)

	// ── 情境 1：冷啟動即原樣入列 ──
	banner("情境 1：冷啟動即查，讀到即原樣入列（3.2／[D15]，不需先建立基準）")
	runCtx, cancel := context.WithCancel(ctx)
	go func() { _ = up.Run(runCtx) }()
	sensor := printer.NewSensor(q, enr, client, cfg.CheckInterval, cfg.PrinterPollInterval,
		printer.WithSensorLogger(log))
	go func() { _ = sensor.Run(runCtx) }()

	waitCounter(ctx, q, enr, func(v int64, serial string) bool { return v == 1000 }, "counter = 1000")
	fmt.Printf("  mock 端點累計收到 %d 筆事件\n", mock.EventCount())

	// ── 情境 2：列印後到期輪詢取得最新絕對值、upsert 同一筆事件並上傳 ──
	banner("情境 2：列印 5 頁 → 到期輪詢讀到最新絕對值 1005 並上傳（3.2/3.3／[D15]）")
	counter += 5
	agent.SetValue(printer.DefaultPageCounterOID, counter)
	waitCounter(ctx, q, enr, func(v int64, serial string) bool { return v == 1005 }, "printer_page_counter = 1005")
	fmt.Printf("  mock 端點累計收到 %d 筆事件\n", mock.EventCount())

	banner("情境 3：再列印 3 頁 → 同一天 upsert 同一筆事件，讀數更新為 1008（非累加，[D15]）")
	counter += 3
	agent.SetValue(printer.DefaultPageCounterOID, counter)
	waitCounter(ctx, q, enr, func(v int64, serial string) bool { return v == 1008 }, "printer_page_counter = 1008")
	fmt.Printf("  事件 ID 為 idToken+日期+printer，一天一筆；後端以「當日最新讀數－前一日最新讀數」求頁數\n")
	cancel()
	time.Sleep(300 * time.Millisecond) // 讓 goroutine 收斂

	// ── 情境 4：序號查無時的降級（[D14] 缺口二）──
	banner("情境 4：印表機不支援序號 OID → payload 省略 printer_serial，仍正常送出讀數（[D14]）")
	agent.DeleteString(printer.DefaultSerialPrtGeneralOID) // 移除模擬三候選皆查無
	runCtx4, cancel4 := context.WithCancel(ctx)
	go func() { _ = up.Run(runCtx4) }()
	sensor4 := printer.NewSensor(q, enr, client, cfg.CheckInterval, cfg.PrinterPollInterval,
		printer.WithSensorLogger(log))
	go func() { _ = sensor4.Run(runCtx4) }()
	counter += 1
	agent.SetValue(printer.DefaultPageCounterOID, counter)
	waitCounter(ctx, q, enr, func(v int64, serial string) bool { return v == 1009 && serial == "" },
		"printer_page_counter = 1009，printer_serial 省略")
	cancel4()
	time.Sleep(300 * time.Millisecond)
	agent.SetString(printer.DefaultSerialPrtGeneralOID, "DEMO-SN-0001") // 復原供後續情境使用

	// ── 情境 5：開機補查 ──
	banner("情境 5：開機補查 — 預置很久以前的時間戳，新感測器啟動即補查")
	old := time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)
	if err := q.SetState(ctx, printer.StateKeyLastPoll, old); err != nil {
		fatal("SetState", err)
	}
	fmt.Printf("  已將 %s 設為 10 天前：%s\n", printer.StateKeyLastPoll, old)
	counter += 2 // 1009 → 1011
	agent.SetValue(printer.DefaultPageCounterOID, counter)

	runCtx5, cancel5 := context.WithCancel(ctx)
	go func() { _ = up.Run(runCtx5) }()
	sensor5 := printer.NewSensor(q, enr, client, cfg.CheckInterval, cfg.PrinterPollInterval,
		printer.WithSensorLogger(log))
	go func() { _ = sensor5.Run(runCtx5) }()
	waitCounter(ctx, q, enr, func(v int64, serial string) bool { return v == 1011 }, "補查後 printer_page_counter = 1011")
	cancel5()
	time.Sleep(300 * time.Millisecond)

	// ── 情境 6：BYOD 印表機不可達 ──
	banner("情境 6：印表機不可達（不同網段／關機）— 只記 log、不入列、Agent 不卡住（3.4）")
	agent.Close()
	fmt.Println("  已關閉 mock 印表機，觀察感測器行為（應只有一筆 warn，不阻塞）...")

	before := count(ctx, q)
	runCtx6, cancel6 := context.WithTimeout(ctx, 3*time.Second)
	sensor6 := printer.NewSensor(q, enr, client, cfg.CheckInterval, cfg.PrinterPollInterval,
		printer.WithSensorLogger(log))
	done := make(chan struct{})
	go func() { _ = sensor6.Run(runCtx6); close(done) }()
	<-done
	cancel6()
	fmt.Printf("  ✓ 感測器已正常結束（未卡住）；佇列筆數 %d → %d（不可達期間不入列）\n",
		before, count(ctx, q))

	q.Close()
	fmt.Printf("\n=== demo 結束：冷啟動即送、最新讀數 upsert、序號降級、開機補查、不可達降級 全數演示完成 ===\n")
}

// ── 小工具 ──

// waitCounter 輪詢當日事件的 printer_page_counter／printer_serial 直到條件成立（或逾時失敗）。
func waitCounter(ctx context.Context, q *queue.Queue, enr *enroll.Enroller, cond func(v int64, serial string) bool, what string) {
	idToken, err := enr.IDToken()
	if err != nil {
		fatal("IDToken", err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		date := time.Now().Format("2006-01-02")
		e, ok, err := q.Get(ctx, queue.EventID(idToken, date, queue.PathPrinter))
		if err == nil && ok {
			v, _ := e.Payload["printer_page_counter"].(float64)
			serial, _ := e.Payload["printer_serial"].(string)
			if cond(int64(v), serial) {
				fmt.Printf("  ✓ %s\n", what)
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	fatal(fmt.Sprintf("等待「%s」逾時", what), nil)
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
