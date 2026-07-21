// Command drive-sensor-demo 是路徑 C 觸發模型（Step 2.2–2.4）的獨立驗證（2.V）＋與
// Step 0 佇列/四重觸發串起的合併驗證（2.M 的 C 軌）。
//
// 為求數十秒內看完且無需真實 Google 憑證，注入 mock QuotaSampler（遞增用量）取代真實
// Drive API，並把 driveQuotaInterval/checkInterval 大幅縮短。示範：
//   - 冷啟動（2.3）：無時間戳 → 感測器一啟動即查、入列、經四重觸發上傳到 mock 端點；
//   - 到期即查（2.2）：每過 driveQuotaInterval 巡檢即再查一次（觀察 sampler 呼叫數遞增）；
//   - 開機補查（2.4）：預先寫入「很久以前」的時間戳 → 新感測器一啟動即立刻補查。
//
// 真實部署改注入 drive.NewAPIClient(drive.NewClient(ctx))；憑證未就緒時整條路徑 C 略過。
//
// 執行：go run ./cmd/drive-sensor-demo   （約 20 秒後自動結束，Ctrl+C 可提前）
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/sensors/drive"
	"eco-agent/internal/uploader"
)

// mockSampler 為 drive.QuotaSampler 的假實作：每次查詢回傳遞增用量，供觀察「有無查」。
//
// MOCK: 取代真實 Google Drive API，讓 demo 免憑證即可跑通觸發模型。
type mockSampler struct {
	calls  atomic.Int64
	baseGB float64
	stepGB float64
}

func (m *mockSampler) StorageQuota(_ context.Context) (drive.Quota, error) {
	n := m.calls.Add(1)
	gb := m.baseGB + m.stepGB*float64(n-1)
	return drive.Quota{Usage: int64(gb * 1e9)}, nil
}

func main() {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 縮短參數：數秒即到期，數十秒內看完多輪查詢。
	cfg := config.LoadProfile(config.ProfileTesting)
	cfg.CheckInterval = 1 * time.Second
	cfg.DriveQuotaInterval = 3 * time.Second
	cfg.ThresholdCount = 1 // 每筆即上傳，方便觀察 mock 端點收到
	cfg.MaxAge = 2 * time.Second

	dir, err := os.MkdirTemp("", "drive-sensor-demo-")
	if err != nil {
		fatal("mkdir temp", err)
	}
	defer os.RemoveAll(dir)
	dbPath := filepath.Join(dir, "queue.db")

	q, err := queue.Open(ctx, dbPath)
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

	sampler := &mockSampler{baseGB: 10, stepGB: 2.5}

	fmt.Printf("\n=== 路徑 C 觸發模型 demo（2.V / 2.M-C）===\n")
	fmt.Printf("checkInterval=%v driveQuotaInterval=%v（不用 sleep(24h) 絕對計時器）\n",
		cfg.CheckInterval, cfg.DriveQuotaInterval)
	fmt.Printf("mock 端點：%s（路徑 C 走 HTTPS，協定=%s）\n", ts.URL, uploader.ProtocolFor(queue.PathDrive))

	// ── 情境 1：冷啟動 + 週期到期即查 ──
	banner("情境 1：冷啟動即查（2.3）＋每 driveQuotaInterval 到期再查（2.2）")
	runCtx, cancel := context.WithCancel(ctx)
	go func() { _ = up.Run(runCtx) }()
	sensor := drive.NewSensor(q, enr, sampler, cfg.CheckInterval, cfg.DriveQuotaInterval,
		drive.WithSensorLogger(log), drive.WithSensorNow(time.Now))
	go func() { _ = sensor.Run(runCtx) }()

	// 冷啟動：啟動後應立即查一次（上傳為非同步，稍後於巡檢 flush）。
	waitCalls(sampler, 1, "冷啟動立即查")
	report(ctx, q, mock, "冷啟動查詢入列後（上傳非同步）")

	// 到期即查：等 ~2 個 driveQuotaInterval，應再查 2 次左右。
	fmt.Printf("  等待 %v 觀察週期到期再查 ...\n", 2*cfg.DriveQuotaInterval+cfg.CheckInterval)
	waitCalls(sampler, 3, "週期到期再查")
	fmt.Printf("  sampler 至今被呼叫 %d 次（每次到期查一輪）\n", sampler.calls.Load())
	cancel()
	time.Sleep(200 * time.Millisecond) // 讓 goroutine 收斂

	// ── 情境 2：開機補查（關機數日後開機）──
	banner("情境 2：開機補查（2.4）— 預置很久以前的時間戳，新感測器啟動即補查")
	// 直接把時間戳改成 10 天前（模擬關機數日），並用「不再增加呼叫」的新 sampler 觀察補查。
	old := time.Now().Add(-10 * 24 * time.Hour).Format(time.RFC3339Nano)
	if err := q.SetState(ctx, drive.StateKeyLastCheck, old); err != nil {
		fatal("SetState", err)
	}
	fmt.Printf("  已將 %s 設為 10 天前：%s\n", drive.StateKeyLastCheck, old)

	sampler2 := &mockSampler{baseGB: 42, stepGB: 0}
	runCtx2, cancel2 := context.WithCancel(ctx)
	go func() { _ = up.Run(runCtx2) }()
	sensor2 := drive.NewSensor(q, enr, sampler2, cfg.CheckInterval, cfg.DriveQuotaInterval,
		drive.WithSensorLogger(log), drive.WithSensorNow(time.Now))
	go func() { _ = sensor2.Run(runCtx2) }()

	waitCalls(sampler2, 1, "開機補查立即查")
	report(ctx, q, mock, "補查上傳後")
	cancel2()
	time.Sleep(200 * time.Millisecond)

	q.Close()
	fmt.Printf("\n=== demo 結束：冷啟動、週期到期、開機補查 全數以持久化時間戳觸發 ===\n")
}

// ── 小工具 ──

func waitCalls(m *mockSampler, want int64, what string) {
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if m.calls.Load() >= want {
			fmt.Printf("  ✓ %s：sampler 累計查詢 %d 次\n", what, m.calls.Load())
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fatal(fmt.Sprintf("%s 逾時：只查了 %d，期望 %d", what, m.calls.Load(), want), nil)
}

func report(ctx context.Context, q *queue.Queue, mock *uploader.MockIngestServer, label string) {
	n, err := q.Count(ctx)
	if err != nil {
		fatal("Count", err)
	}
	fmt.Printf("  [%s] 佇列待送=%d，mock 累計收到=%d\n", label, n, mock.EventCount())
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
