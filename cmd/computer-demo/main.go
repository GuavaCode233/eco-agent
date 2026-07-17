// Command computer-demo 是 Step 1.3 路徑 A 電腦使用感測的獨立驗證（1.V）＋與 Step 0
// 佇列/四重觸發串起的端到端合併驗證（1.M）。
//
// 使用「真實」活動偵測與 CPU 取樣（reflect 你當下的操作/閒置），以縮短的輪詢區間與
// idle 閾值，讓數十秒內即可觀察：
//   - 操作鍵鼠 → active 時數累計；放手超過 idle 閾值 → idle 時數累計；
//   - 平均 CPU 使用率隨負載變動；
//   - 佇列僅一筆「當日累計事件」隨輪詢 upsert 更新（狀態值輪詢）；
//   - 佇列達 thresholdCount／maxAge 時，四重觸發把累計事件上傳到 in-process mock 端點。
//
// 於不支援平台（非 Windows/macOS）會優雅降級：印出訊息並結束。
//
// 執行：go run ./cmd/computer-demo   （約 40 秒後自動結束，Ctrl+C 可提前）
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/http/httptest"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/sensors/computer"
	"eco-agent/internal/uploader"
)

const (
	demoInterval      = 2 * time.Second  // 縮短輪詢區間（正式 60s）
	demoIdleThreshold = 5 * time.Second  // 縮短 idle 閾值（正式 10min）
	demoDuration      = 40 * time.Second // demo 總時長
)

func main() {
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 提前檢查平台支援，優雅降級。
	if err := platform.NewActivityDetector().Available(); err != nil {
		fmt.Fprintf(os.Stderr, "此平台不支援路徑 A 活動偵測（優雅降級）：%v\n", err)
		os.Exit(0)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	// 測試 profile 再縮短觸發參數，讓上傳在數十秒內可見。
	cfg := config.LoadProfile(config.ProfileTesting)
	cfg.CheckInterval = 1 * time.Second
	cfg.ThresholdCount = 3
	cfg.MaxAge = 15 * time.Second

	dir, err := os.MkdirTemp("", "computer-demo-")
	if err != nil {
		fatal("mkdir temp", err)
	}
	defer os.RemoveAll(dir)

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

	sensor := computer.New(q, enr,
		platform.NewActivityDetector(), platform.NewCPUSampler(),
		demoInterval,
		computer.WithIdleThreshold(demoIdleThreshold),
		computer.WithLogger(log),
	)

	fmt.Printf("\n=== Eco-Agent Step 1.3 路徑 A 端到端 demo (1.V + 1.M) ===\n")
	fmt.Printf("輪詢區間=%v idle閾值=%v ｜ thresholdCount=%d maxAge=%v\n",
		demoInterval, demoIdleThreshold, cfg.ThresholdCount, cfg.MaxAge)
	fmt.Printf("請操作/閒置電腦觀察 active/idle 累計；閒置超過 %v 即計 idle。\n", demoIdleThreshold)
	fmt.Printf("mock 端點：%s/mock/ingest（回 200）\n\n", ts.URL)

	// 啟動 uploader 與 sensor。
	go func() { _ = up.Run(ctx) }()
	go func() { _ = sensor.Run(ctx) }()

	// 每 2 秒印出當日累計事件與 mock 收到筆數。
	idToken, _ := enr.IDToken()
	eventID := queue.EventID(idToken, time.Now().Format("2006-01-02"), queue.PathComputer)

	deadline := time.After(demoDuration)
	tick := time.NewTicker(2 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			fmt.Println("\n收到中斷，結束。")
			return
		case <-deadline:
			fmt.Println("\n=== demo 時間到，結束 ===")
			printState(ctx, q, eventID, mock)
			return
		case <-tick.C:
			printState(ctx, q, eventID, mock)
		}
	}
}

func printState(ctx context.Context, q *queue.Queue, eventID string, mock *uploader.MockIngestServer) {
	n, _ := q.Count(ctx)
	e, ok, _ := q.Get(ctx, eventID)
	if !ok {
		fmt.Printf("  佇列待送=%d｜當日事件已上傳並清除（mock 累計收到=%d）\n", n, mock.EventCount())
		return
	}
	fmt.Printf("  佇列待送=%d｜當日累計 active=%.4fh idle=%.4fh avgCPU=%.1f%%｜mock 收到=%d\n",
		n, toF(e.Payload["pc_active_hours"]), toF(e.Payload["pc_idle_hours"]),
		toF(e.Payload["pc_avg_cpu_util"]), mock.EventCount())
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
