// Command eco-agent-demo 是 Step 0 地基的端到端獨立驗證（CLAUDE.md 0.V）。
//
// 單一指令跑通：手動 Enqueue 假資料 → 觀察四重觸發各自正確 flush → mock 端點收到 →
// 佇列僅在後端回 200 後才清除。並額外演示 at-least-once（非 200 不清）與撤銷自清（401）。
//
// 為求數十秒內看完，使用測試 profile 並再縮短巡檢/滯留參數。內建 in-process mock 端點
// （真實 HTTP round-trip），無需另開 cmd/mock-ingest。
//
// 執行：go run ./cmd/eco-agent-demo
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
	"eco-agent/internal/uploader"
)

var seq int // 保證每筆 demo 事件的事件 ID 唯一（避免 upsert 撞號）。

func main() {
	ctx := context.Background()
	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// 測試 profile 再縮短，讓四重觸發在數十秒內全部觀察到。
	cfg := config.LoadProfile(config.ProfileTesting)
	cfg.CheckInterval = 1 * time.Second
	cfg.ThresholdCount = 3
	cfg.MaxAge = 4 * time.Second

	dir, err := os.MkdirTemp("", "eco-agent-demo-")
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

	fmt.Printf("\n=== Eco-Agent Step 0 端到端 demo (0.V) ===\n")
	fmt.Printf("設定：checkInterval=%v thresholdCount=%d maxAge=%v uploadBatchMax=%d\n",
		cfg.CheckInterval, cfg.ThresholdCount, cfg.MaxAge, cfg.UploadBatchMax)
	fmt.Printf("mock 端點：%s/mock/ingest（預設回 200）\n", ts.URL)

	demoFourTriggers(ctx, cfg, q, enr, up, mock)
	demoAtLeastOnceAndRevoke(ctx, q, enr, up, mock)

	fmt.Printf("\n=== demo 結束：四重觸發、at-least-once、撤銷自清 全數演示完成 ===\n")
}

// demoFourTriggers 演示開機補送、累積達量、最長滯留、關機搶送。
func demoFourTriggers(ctx context.Context, cfg config.Config, q *queue.Queue, enr *enroll.Enroller, up *uploader.Uploader, mock *uploader.MockIngestServer) {
	// ── 觸發 1／4：開機後補送（startup）──
	// Run 尚未啟動前先塞 2 筆，模擬「上次未送完」；Run 一啟動即補送。
	banner("觸發 1/4 開機後補送 (startup)")
	enqueue(ctx, q, enr, queue.PathComputer, 2)
	report(ctx, q, mock, "Run 啟動前")

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- up.Run(runCtx) }()

	want := 2
	waitReceived(mock, want, "開機補送")
	report(ctx, q, mock, "startup flush 後")

	// ── 觸發 2／4：累積達量（threshold）──
	banner("觸發 2/4 累積達量 (thresholdCount=" + itoa(cfg.ThresholdCount) + ")")
	// 混路徑：路徑 C 走 HTTPS、其餘走 MQTT，一併演示協定分流。
	enqueue(ctx, q, enr, queue.PathComputer, 2)
	enqueue(ctx, q, enr, queue.PathDrive, 1)
	want += 3
	waitReceived(mock, want, "達量 flush")
	report(ctx, q, mock, "threshold flush 後")

	// ── 觸發 3／4：最長滯留（maxAge）──
	banner("觸發 3/4 最長滯留 (maxAge=" + cfg.MaxAge.String() + ")")
	// 只塞 1 筆（未達門檻），靠滯留超時觸發。
	enqueue(ctx, q, enr, queue.PathPrinter, 1)
	fmt.Printf("  塞入 1 筆（< 門檻），等待滯留超過 %v ...\n", cfg.MaxAge)
	want++
	waitReceived(mock, want, "滯留 flush")
	report(ctx, q, mock, "maxAge flush 後")

	// ── 觸發 4／4：關機前搶送（shutdown）──
	banner("觸發 4/4 關機前搶送 (shutdown hook)")
	enqueue(ctx, q, enr, queue.PathComputer, 2)
	report(ctx, q, mock, "cancel 前（佇列有零頭）")
	fmt.Printf("  送出關機訊號（cancel ctx）...\n")
	cancel() // 模擬 OS 關機事件 → ctx 取消

	select {
	case err := <-done:
		fmt.Printf("  Run 已退出：%v\n", err)
	case <-time.After(5 * time.Second):
		fatal("Run did not return after cancel", nil)
	}
	want += 2
	waitReceived(mock, want, "關機搶送")
	report(ctx, q, mock, "shutdown flush 後")
}

// demoAtLeastOnceAndRevoke 演示：非 200 不清除（保留重送）、200 才清、401 撤銷自清。
func demoAtLeastOnceAndRevoke(ctx context.Context, q *queue.Queue, enr *enroll.Enroller, up *uploader.Uploader, mock *uploader.MockIngestServer) {
	// ── at-least-once：後端不可用（500）→ 佇列保留 ──
	banner("At-least-once：後端 500 不清除、恢復 200 才清")
	enqueue(ctx, q, enr, queue.PathComputer, 3)
	mock.SetStatus(500)
	fmt.Printf("  mock 切為 500，手動 Flush ...\n")
	_ = up.Flush(ctx, uploader.ReasonManual)
	report(ctx, q, mock, "500 之後（佇列應仍有 3 筆）")

	mock.SetStatus(200)
	fmt.Printf("  mock 恢復 200，再次 Flush ...\n")
	_ = up.Flush(ctx, uploader.ReasonManual)
	report(ctx, q, mock, "恢復 200 之後（佇列應清空）")

	// ── 撤銷自清：401 → 清憑證、停止上傳、資料保留 ──
	banner("撤銷夾帶：mock 回 401 → 自清憑證、停止上傳")
	enqueue(ctx, q, enr, queue.PathComputer, 1)
	mock.SetStatus(401)
	fmt.Printf("  mock 切為 401，手動 Flush ...\n")
	err := up.Flush(ctx, uploader.ReasonManual)
	fmt.Printf("  Flush 回傳：%v（uploader.Stopped=%v）\n", err, up.Stopped())
	if _, iderr := enr.IDToken(); iderr != nil {
		fmt.Printf("  憑證狀態：IDToken() → %v（已自清）\n", iderr)
	}
	report(ctx, q, mock, "401 之後（憑證已清、資料保留待重綁）")
}

// ── 小工具 ──

func enqueue(ctx context.Context, q *queue.Queue, enr *enroll.Enroller, p queue.PathType, n int) {
	idToken, err := enr.IDToken()
	if err != nil {
		fatal("IDToken", err)
	}
	for i := 0; i < n; i++ {
		seq++
		date := time.Now().Format("2006-01-02")
		e := queue.Event{
			// 事件 ID 併入 seq 確保唯一（真實情境的穩定鍵為 idToken+date+path）。
			ID:       queue.EventID(idToken, fmt.Sprintf("%s#%d", date, seq), p),
			PathType: p,
			Payload:  demoPayload(p, date),
		}
		if err := q.Enqueue(ctx, e); err != nil {
			fatal("Enqueue", err)
		}
	}
	fmt.Printf("  Enqueue %d 筆（路徑 %s，協定 %s）\n", n, p, uploader.ProtocolFor(p))
}

func demoPayload(p queue.PathType, date string) map[string]any {
	switch p {
	case queue.PathComputer:
		return map[string]any{"date": date, "pc_active_hours": 1.5, "pc_tdp_w": 45}
	case queue.PathDrive:
		return map[string]any{"date": date, "drive_usage_gb": 12.3}
	case queue.PathPrinter:
		return map[string]any{"date": date, "print_pages": 7}
	default:
		return map[string]any{"date": date}
	}
}

func report(ctx context.Context, q *queue.Queue, mock *uploader.MockIngestServer, label string) {
	n, err := q.Count(ctx)
	if err != nil {
		fatal("Count", err)
	}
	fmt.Printf("  [%s] 佇列待送=%d，mock 累計收到=%d\n", label, n, mock.EventCount())
}

// waitReceived 等待 mock 端點累計收到達 want 筆（觸發生效的觀察點）。
func waitReceived(mock *uploader.MockIngestServer, want int, what string) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if mock.EventCount() >= want {
			fmt.Printf("  ✓ %s 生效：mock 累計收到 %d 筆\n", what, mock.EventCount())
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	fatal(fmt.Sprintf("%s 逾時：mock 只收到 %d，期望 %d", what, mock.EventCount(), want), nil)
}

func banner(msg string) { fmt.Printf("\n── %s ──\n", msg) }

func itoa(n int) string { return fmt.Sprintf("%d", n) }

func fatal(msg string, err error) {
	if err != nil {
		fmt.Fprintf(os.Stderr, "demo failed: %s: %v\n", msg, err)
	} else {
		fmt.Fprintf(os.Stderr, "demo failed: %s\n", msg)
	}
	os.Exit(1)
}
