// Command all-paths-demo 是三路徑齊跑的端到端合併驗證（CLAUDE.md 2.M + 3.M）。
//
// 路徑 A（電腦使用）、路徑 C（雲端儲存）、路徑 B（印表機）各自以獨立 goroutine、各自的
// 節奏採集，全部匯入**同一份持久化佇列**，再由 uploader 的**四重觸發**統一批次上傳到
// in-process mock 端點，並依路徑分流協定（A/B 走 MQTT、C 走 HTTPS）。
//
// 各路徑的資料來源（有真的用真的，沒有才降級為 mock，並於畫面標示）：
//   - 路徑 A：一律真實取樣（GetLastInputInfo／IOHID + gopsutil）。平台不支援則跳過該路徑。
//   - 路徑 C：設好 Google OAuth 憑證（見 §6）就真串 Drive API；否則用 mock QuotaSampler。
//   - 路徑 B：設好 ECO_AGENT_PRINTER_HOST 就真查該印表機；否則起本機 mock SNMP responder。
//
// 三條路徑任一不可用都只降級該路徑，其餘照跑——這正是 Agent 在 BYOD 現場的預期行為。
// 設了但當下連不到（不同網段／關機）的印表機亦然：只記 log、不入列，不影響 A/C。
//
// 執行：go run ./cmd/all-paths-demo            （約 45 秒，Ctrl+C 可提前）
//
//	go run ./cmd/all-paths-demo -duration 90s
//	go run ./cmd/all-paths-demo -mock-printer   # 忽略 .env 的印表機，強制用本機 mock
//	go run ./cmd/all-paths-demo -mock-drive     # 忽略 Google 憑證，強制用 mock 用量
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/sensors/computer"
	"eco-agent/internal/sensors/drive"
	"eco-agent/internal/sensors/printer"
	"eco-agent/internal/uploader"
)

// demo 用的縮短參數：讓三條路徑各自的節奏在數十秒內都跑到好幾輪。
const (
	demoComputerInterval = 2 * time.Second // 路徑 A 輪詢區間（正式 60s）
	demoIdleThreshold    = 5 * time.Second // 路徑 A idle 閾值（正式 10min）
	demoDriveInterval    = 6 * time.Second // 路徑 C 到期門檻（正式 24h）
	demoPrinterInterval  = 4 * time.Second // 路徑 B 到期門檻（正式 300s）
	demoCheckInterval    = 1 * time.Second // 巡檢（正式 60s）
	demoThresholdCount   = 3               // 累積達量（正式 60 筆）
	demoMaxAge           = 10 * time.Second

	demoPrintEvery = 5 * time.Second // mock 印表機每隔多久「印」一次
	demoPrintPages = 2               // 每次印幾頁
)

// mockQuotaSampler 為 drive.QuotaSampler 的假實作（憑證未就緒時用），每次查詢用量遞增。
//
// MOCK: 取代真實 Google Drive API，讓合併 demo 免憑證亦可跑通路徑 C。
type mockQuotaSampler struct {
	calls atomic.Int64
}

func (m *mockQuotaSampler) StorageQuota(_ context.Context) (drive.Quota, error) {
	n := m.calls.Add(1)
	gb := 10 + 0.5*float64(n-1)
	return drive.Quota{
		Usage:             int64(gb * 1.5 * 1e9),
		UsageInDrive:      int64(gb * 1e9),
		UsageInDriveTrash: int64(gb * 0.1 * 1e9),
	}, nil
}

func main() {
	duration := flag.Duration("duration", 45*time.Second, "demo 總時長")
	mockPrinter := flag.Bool("mock-printer", false, "忽略 ECO_AGENT_PRINTER_HOST，強制用本機 mock SNMP responder")
	mockDrive := flag.Bool("mock-drive", false, "忽略 Google OAuth 憑證，強制用 mock 用量")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	if p := config.LoadDotEnv(); p != "" {
		fmt.Printf("（已載入 %s）\n", p)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	cfg := config.LoadProfile(config.ProfileTesting)
	cfg.CheckInterval = demoCheckInterval
	cfg.ThresholdCount = demoThresholdCount
	cfg.MaxAge = demoMaxAge
	cfg.DriveQuotaInterval = demoDriveInterval
	cfg.PrinterPollInterval = demoPrinterInterval

	dir, err := os.MkdirTemp("", "all-paths-demo-")
	if err != nil {
		fatal("mkdir temp", err)
	}
	defer os.RemoveAll(dir)

	// 單一佇列：三條路徑共用，這是合併驗證的重點之一。
	q, err := queue.Open(ctx, filepath.Join(dir, "queue.db"))
	if err != nil {
		fatal("queue.Open", err)
	}
	defer q.Close()

	enr := enroll.New(platform.NewMemoryKeychain())
	if err := enr.EnsureBound(ctx); err != nil {
		fatal("EnsureBound", err)
	}

	mock := uploader.NewMockIngestServer(200)
	ts := httptest.NewServer(mock.Handler())
	defer ts.Close()
	up := uploader.New(q, enr, cfg,
		uploader.WithUploadURL(ts.URL+"/mock/ingest"),
		uploader.WithLogger(log),
		uploader.WithShutdownTimeout(3*time.Second),
	)

	fmt.Printf("\n=== Eco-Agent 三路徑合併 demo (2.M + 3.M) ===\n")
	fmt.Printf("單一佇列：%s\n", filepath.Join(dir, "queue.db"))
	fmt.Printf("四重觸發：checkInterval=%v thresholdCount=%d maxAge=%v（關機搶送於結束時演示）\n",
		cfg.CheckInterval, cfg.ThresholdCount, cfg.MaxAge)
	fmt.Printf("mock 端點：%s/mock/ingest（回 200；僅 200 才清佇列）\n\n", ts.URL)

	// 三條感測路徑掛在自己的 ctx 上：demo 結束時先停感測、再讓 uploader 搶送零頭，
	// 順序與真實關機一致（感測停止 → shutdown hook flush）。
	sensorCtx, stopSensors := context.WithCancel(ctx)
	defer stopSensors()

	// ── 路徑 A：電腦使用（真實取樣）──
	pathA := startPathA(sensorCtx, q, enr, log)

	// ── 路徑 C：雲端儲存（有憑證真串、否則 mock）──
	pathC := startPathC(sensorCtx, q, enr, cfg, log, *mockDrive)

	// ── 路徑 B：印表機（有目標真查、否則本機 mock SNMP responder）──
	pathB, stopPrinter := startPathB(sensorCtx, q, enr, cfg, log, *mockPrinter)
	defer stopPrinter()

	fmt.Printf("\n路徑狀態：A=%s｜C=%s｜B=%s\n", pathA, pathC, pathB)
	fmt.Printf("協定分流：A→%s、B→%s、C→%s\n\n",
		uploader.ProtocolFor(queue.PathComputer),
		uploader.ProtocolFor(queue.PathPrinter),
		uploader.ProtocolFor(queue.PathDrive))

	// uploader 啟動：Run 一啟動即做「開機後檢查」補送（四重觸發之一）。
	runCtx, cancel := context.WithCancel(ctx)
	upDone := make(chan error, 1)
	go func() { upDone <- up.Run(runCtx) }()

	// ── 觀察迴圈 ──
	idToken, _ := enr.IDToken()
	deadline := time.After(*duration)
	tick := time.NewTicker(3 * time.Second)
	defer tick.Stop()

loop:
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n收到中斷，提前結束。")
			break loop
		case <-deadline:
			fmt.Println("\n=== demo 時間到 ===")
			break loop
		case <-tick.C:
			report(ctx, q, mock, idToken)
		}
	}

	// ── 四重觸發之「關機前搶送」：cancel 模擬關機事件，零頭應在退出前送完 ──
	fmt.Printf("\n── 關機前搶送（shutdown hook）──\n")
	stopSensors() // 先停三條感測路徑（同真實關機順序），佇列裡剩下的即是「零頭」
	time.Sleep(200 * time.Millisecond)
	before, _ := q.Count(ctx)
	fmt.Printf("  cancel 前佇列待送=%d\n", before)
	cancel()
	select {
	case err := <-upDone:
		fmt.Printf("  uploader 已退出：%v\n", err)
	case <-time.After(5 * time.Second):
		fmt.Println("  （uploader 未於 5 秒內退出）")
	}
	after, _ := q.Count(ctx)
	fmt.Printf("  shutdown flush 後佇列待送=%d（僅 200 後才清）\n", after)

	summary(mock)
}

// startPathA 啟動路徑 A；平台不支援則跳過並回傳降級說明。
func startPathA(ctx context.Context, q *queue.Queue, enr *enroll.Enroller, log *slog.Logger) string {
	if err := platform.NewActivityDetector().Available(); err != nil {
		return fmt.Sprintf("跳過（%v）", err)
	}
	s := computer.New(q, enr, platform.NewActivityDetector(), platform.NewCPUSampler(),
		demoComputerInterval,
		computer.WithIdleThreshold(demoIdleThreshold),
		computer.WithLogger(log),
	)
	go func() { _ = s.Run(ctx) }()
	return fmt.Sprintf("真實取樣（每 %v 輪詢，idle 閾值 %v）", demoComputerInterval, demoIdleThreshold)
}

// startPathC 啟動路徑 C；憑證就緒則真串 Drive API，否則降級為 mock sampler。
func startPathC(ctx context.Context, q *queue.Queue, enr *enroll.Enroller, cfg config.Config, log *slog.Logger, forceMock bool) string {
	var (
		sampler drive.QuotaSampler
		desc    string
	)
	hc, err := drive.NewClient(ctx)
	switch {
	case forceMock:
		sampler, desc = &mockQuotaSampler{}, "mock sampler（-mock-drive 指定）"
	case err == nil:
		sampler, desc = drive.NewAPIClient(hc), "真串 Google Drive API v3"
	case errors.Is(err, drive.ErrCredentialsNotConfigured), errors.Is(err, drive.ErrNotAuthorized):
		// §6 優雅降級：憑證未就緒不使 Agent 崩潰；此處改用 mock 讓合併 demo 仍能觀察路徑 C 節奏。
		sampler, desc = &mockQuotaSampler{}, fmt.Sprintf("mock sampler（%v）", err)
	default:
		sampler, desc = &mockQuotaSampler{}, fmt.Sprintf("mock sampler（建立 client 失敗：%v）", err)
	}
	s := drive.NewSensor(q, enr, sampler, cfg.CheckInterval, cfg.DriveQuotaInterval,
		drive.WithSensorLogger(log))
	go func() { _ = s.Run(ctx) }()
	return fmt.Sprintf("%s，每 %v 到期查一次", desc, cfg.DriveQuotaInterval)
}

// startPathB 啟動路徑 B；未設 ECO_AGENT_PRINTER_HOST 則起本機 mock SNMP responder，
// 並定時增加 page counter 模擬列印。回傳的 stop 用於關閉 mock responder。
func startPathB(ctx context.Context, q *queue.Queue, enr *enroll.Enroller, cfg config.Config, log *slog.Logger, forceMock bool) (string, func()) {
	var (
		sampler printer.PageCounterSampler
		desc    string
		stop    = func() {}
	)

	client, err := printer.NewSNMPClientFromEnv()
	if forceMock {
		client, err = nil, errors.New("-mock-printer 指定")
	}
	if err == nil {
		addr, oid := client.Target()
		sampler, desc = client, fmt.Sprintf("真查印表機 %s（OID %s）", addr, oid)
	} else {
		// MOCK: 本機 SNMP responder 取代真實印表機，並以 goroutine 模擬持續列印。
		agent, aerr := printer.StartMockAgent("127.0.0.1:0", printer.DefaultCommunity,
			map[string]uint64{printer.DefaultPageCounterOID: 1000})
		if aerr != nil {
			return fmt.Sprintf("跳過（mock responder 啟動失敗：%v）", aerr), stop
		}
		host, port := agent.Addr()
		c, cerr := printer.NewSNMPClient(host, printer.WithPort(port))
		if cerr != nil {
			agent.Close()
			return fmt.Sprintf("跳過（%v）", cerr), stop
		}
		go simulatePrinting(ctx, agent)
		sampler = c
		desc = fmt.Sprintf("mock SNMP responder %s:%d（%v；每 %v 印 %d 頁）",
			host, port, err, demoPrintEvery, demoPrintPages)
		stop = func() { agent.Close() }
	}

	s := printer.NewSensor(q, enr, sampler, cfg.CheckInterval, cfg.PrinterPollInterval,
		printer.WithSensorLogger(log))
	go func() { _ = s.Run(ctx) }()
	return fmt.Sprintf("%s，每 %v 到期查一次", desc, cfg.PrinterPollInterval), stop
}

// simulatePrinting 定時墊高 mock 印表機的 page counter，模擬使用者持續列印。
func simulatePrinting(ctx context.Context, agent *printer.MockAgent) {
	counter := uint64(1000)
	t := time.NewTicker(demoPrintEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			counter += demoPrintPages
			agent.SetValue(printer.DefaultPageCounterOID, counter)
		}
	}
}

// ── 觀察與彙總 ──

// report 印出三路徑「當日累計事件」在佇列中的現況與 mock 端點累計收到數。
// 顯示「—」表示該路徑當下無待送事件：可能是已上傳清除（狀態值輪詢下，下次輪詢會再
// upsert 回來），也可能是尚未產生（如印表機期間無列印、或不可達）。
func report(ctx context.Context, q *queue.Queue, mock *uploader.MockIngestServer, idToken string) {
	n, _ := q.Count(ctx)
	date := time.Now().Format("2006-01-02")
	fmt.Printf("  佇列待送=%d｜A: %s｜C: %s｜B: %s｜mock 累計收到=%d\n",
		n,
		summarize(ctx, q, idToken, date, queue.PathComputer),
		summarize(ctx, q, idToken, date, queue.PathDrive),
		summarize(ctx, q, idToken, date, queue.PathPrinter),
		mock.EventCount())
}

// summarize 取單一路徑當日事件的關鍵欄位（皆為原始感測值，能耗由後端換算）。
func summarize(ctx context.Context, q *queue.Queue, idToken, date string, p queue.PathType) string {
	e, ok, err := q.Get(ctx, queue.EventID(idToken, date, p))
	if err != nil || !ok {
		return "—"
	}
	switch p {
	case queue.PathComputer:
		return fmt.Sprintf("active=%.4fh idle=%.4fh cpu=%.0f%%",
			toF(e.Payload["pc_active_hours"]), toF(e.Payload["pc_idle_hours"]),
			toF(e.Payload["pc_avg_cpu_util"]))
	case queue.PathDrive:
		return fmt.Sprintf("%.2fGB", toF(e.Payload["drive_usage_gb"]))
	case queue.PathPrinter:
		return fmt.Sprintf("%.0f頁", toF(e.Payload["print_pages"]))
	default:
		return "-"
	}
}

// summary 依協定與路徑彙總 mock 端點實際收到的批次，驗證協定分流與三路徑匯流。
func summary(mock *uploader.MockIngestServer) {
	byProtocol := map[string]int{}
	byPath := map[string]int{}
	for _, b := range mock.Received() {
		byProtocol[b.Protocol] += len(b.EventIDs)
		for _, id := range b.EventIDs {
			// 事件 ID 為 idToken|date|path（見 queue.EventID）。
			if i := strings.LastIndex(id, "|"); i >= 0 {
				byPath[id[i+1:]]++
			}
		}
	}
	fmt.Printf("\n=== 合併驗證結果 ===\n")
	fmt.Printf("mock 端點收到（依協定分流）：mqtt=%d（路徑 A+B）、https=%d（路徑 C）\n",
		byProtocol["mqtt"], byProtocol["https"])
	fmt.Printf("mock 端點收到（依路徑，含同一事件多次 upsert 重送）：A=%d、C=%d、B=%d\n",
		byPath[string(queue.PathComputer)], byPath[string(queue.PathDrive)],
		byPath[string(queue.PathPrinter)])
	fmt.Println("三路徑各以自己的節奏採集 → 匯入同一份持久化佇列 → 四重觸發統一批次上傳。")
	fmt.Println("（同一路徑同一天為同一事件 ID，重送由後端冪等 upsert 去重，不重複計算。）")
}

func toF(v any) float64 {
	if f, ok := v.(float64); ok {
		return f
	}
	return 0
}

func fatal(msg string, err error) {
	fmt.Fprintf(os.Stderr, "demo failed: %s: %v\n", msg, err)
	os.Exit(1)
}
