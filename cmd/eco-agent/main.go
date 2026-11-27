// Command eco-agent 是 Eco-Agent 的正式進入點（無人值守背景常駐程式）。
//
// 職責：載入配置（含 §後端串接 sensor_config 真串）、開啟本機持久化佇列、建立 enroll／
// uploader，交由 internal/agent 的監督者迴圈執行：綁定（失敗持續重試）→ 啟動三條感測路徑
// 與四重觸發上傳 → 遇解綁／撤銷／Refresh Token 失效回到綁定，直到收到中斷訊號（相當於
// 關機/登出前 hook，uploader 搶送零頭）為止。
//
// 目前為 headless 模式（尚無系統匣，見 docs/Eco-Agent_系統匣UI規劃.md U3）：沒有綁定頁可
// 關閉，因此綁定階段維持持續換碼，QR 內容（custom scheme URI）印在 log。
//
// 各 demo（cmd/*-demo）用假資料、mock sampler、暫存佇列目錄驗證單一環節；本進入點串起
// 整個生產路徑：真實感測來源（有則真、無則優雅降級）、持久化佇列落地於使用者設定目錄、
// 真實系統金鑰庫（platform.NewOSKeychain）。
//
// 執行：go run ./cmd/eco-agent
//
// 員工端主動解綁（換機，進度表 6.1）：go run ./cmd/eco-agent -unbind [-force]
// ——採「先送再丟」：先上傳一次佇列；仍有送不出去的資料時，未加 -force 即中止並告知筆數，
// 加 -force 則捨棄後解綁。解綁會呼叫後端 POST /api/agent/unbind（失敗不阻擋）並清除本機憑證。
//
// 環境變數（詳見 .env.example）：
//   - ECO_AGENT_PROFILE=testing 可切換為測試參數（數分鐘內觀察完整流程），預設正式值。
//   - ECO_AGENT_API_BASE_URL 覆寫後端 base URL；留空則依 profile 預設值。
//   - ECO_AGENT_QUEUE_PATH 覆寫佇列 SQLite 檔案路徑；未設時預設落於使用者設定目錄下
//     eco-agent/queue.db（Windows: %AppData%、macOS: ~/Library/Application Support）。
//   - GOOGLE_OAUTH_CLIENT_ID/SECRET、ECO_AGENT_PRINTER_HOST 等見 .env.example；未設定時
//     對應路徑（C／B）記 log 並跳過，不影響其餘路徑。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"eco-agent/internal/agent"
	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/uploader"
)

// EnvQueuePath 覆寫佇列 SQLite 檔案路徑。
const EnvQueuePath = "ECO_AGENT_QUEUE_PATH"

func main() {
	unbind := flag.Bool("unbind", false, "員工端主動解綁：先上傳佇列，再通知後端並清除本機憑證後結束")
	force := flag.Bool("force", false, "搭配 -unbind：佇列仍有送不出去的資料時直接捨棄")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)

	if p := config.LoadDotEnv(); p != "" {
		log.Info("已載入 .env", "path", p)
	}

	// 中斷訊號視同「關機/登出前 hook」：取消 ctx 讓 uploader.Run 搶送佇列零頭
	// （四重觸發之一，見 internal/uploader.Uploader.Run 的 shutdownFlush）。
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()
	log.Info("config loaded",
		"profile", cfg.Profile, "base_url", cfg.BaseURL,
		"checkInterval", cfg.CheckInterval, "thresholdCount", cfg.ThresholdCount, "maxAge", cfg.MaxAge)

	dbPath, err := queuePath()
	if err != nil {
		fatal(log, "resolve queue path", err)
	}
	q, err := queue.Open(ctx, dbPath)
	if err != nil {
		fatal(log, "queue.Open", err)
	}
	defer q.Close()
	log.Info("queue opened", "path", dbPath)

	// ag 於 enroll 之後建立；綁定碼回呼只在 ag.Run 期間觸發，此時 ag 已賦值。
	var ag *agent.Agent
	enr := enroll.New(platform.NewOSKeychain(), q,
		enroll.WithBaseURL(cfg.BaseURL),
		enroll.WithBindingCodeTTL(cfg.BindingCodeTTL),
		enroll.WithLogger(log),
		enroll.WithBindingCodeHandler(func(uri string, exp time.Time) { ag.OnBindingCode(uri, exp) }),
	)
	newUploader := func() agent.Uploader { return uploader.New(q, enr, cfg, uploader.WithLogger(log)) }
	ag = agent.New(enr, q, newUploader, agent.DefaultSensors(q, enr, cfg, log), agent.WithLogger(log))

	if *unbind {
		runUnbind(ctx, ag, *force, log)
		return
	}

	log.Info("eco-agent started")
	if err := ag.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		log.Warn("eco-agent stopped", "err", err)
		return
	}
	log.Info("eco-agent stopped (shutdown signal)")
}

// runUnbind 執行 CLI 解綁（先送再丟）；佇列有殘留且未加 -force 時中止。
func runUnbind(ctx context.Context, ag *agent.Agent, force bool, log *slog.Logger) {
	err := ag.Unbind(ctx, func(leftover int) bool {
		if !force {
			log.Warn("unbind aborted: queued events could not be uploaded; rerun with -force to discard them",
				"leftover", leftover)
		}
		return force
	})
	switch {
	case errors.Is(err, agent.ErrUnbindCancelled):
		os.Exit(2)
	case err != nil:
		fatal(log, "Unbind", err)
	}
	log.Info("device unbound, local credentials cleared")
}

// queuePath 回傳佇列 SQLite 檔案路徑：ECO_AGENT_QUEUE_PATH 覆寫，否則預設落於使用者
// 設定目錄下 eco-agent/queue.db（佇列必落磁碟，CLAUDE.md §9）。兩種情況都確保父目錄存在。
func queuePath() (string, error) {
	path := os.Getenv(EnvQueuePath)
	if path == "" {
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", fmt.Errorf("resolve user config dir: %w", err)
		}
		path = filepath.Join(dir, "eco-agent", "queue.db")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
	}
	return path, nil
}

func fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, "err", err)
	os.Exit(1)
}
