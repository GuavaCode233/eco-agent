// Command eco-agent 是 Eco-Agent 的正式進入點（無人值守背景常駐程式）。
//
// 職責：載入配置（含 §後端串接 sensor_config 真串）、開啟本機持久化佇列、完成裝置綁定
// （真實五端點流程或 mock，依是否設定後端 base URL 分派，見 internal/enroll）、啟動三條
// 感測路徑（各自獨立 goroutine，任一路徑不可用僅降級跳過，不使 Agent 崩潰）、啟動 uploader
// 的四重觸發批次上傳迴圈，直到收到中斷訊號（相當於關機/登出前 hook）為止。
//
// 各 demo（cmd/*-demo）用假資料、mock sampler、暫存佇列目錄驗證單一環節；本進入點串起
// 整個生產路徑：真實感測來源（有則真、無則優雅降級）、持久化佇列落地於使用者設定目錄、
// 真實系統金鑰庫（platform.NewOSKeychain）。
//
// 執行：go run ./cmd/eco-agent
//
// 環境變數（詳見 .env.example）：
//   - ECO_AGENT_PROFILE=testing 可切換為測試參數（數分鐘內觀察完整流程），預設正式值。
//   - ECO_AGENT_API_BASE_URL 覆寫後端 base URL；留空則依 profile 預設值，且裝置綁定/上傳
//     走 mock 捷徑（後端尚未就緒時仍可完整跑通佇列與四重觸發）。
//   - ECO_AGENT_QUEUE_PATH 覆寫佇列 SQLite 檔案路徑；未設時預設落於使用者設定目錄下
//     eco-agent/queue.db（Windows: %AppData%、macOS: ~/Library/Application Support）。
//   - GOOGLE_OAUTH_CLIENT_ID/SECRET、ECO_AGENT_PRINTER_HOST 等見 .env.example；未設定時
//     對應路徑（C／B）記 log 並跳過，不影響其餘路徑。
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/sensors/computer"
	"eco-agent/internal/sensors/drive"
	"eco-agent/internal/sensors/printer"
	"eco-agent/internal/uploader"
)

// EnvQueuePath 覆寫佇列 SQLite 檔案路徑。
const EnvQueuePath = "ECO_AGENT_QUEUE_PATH"

func main() {
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

	enr := enroll.New(platform.NewOSKeychain(), q,
		enroll.WithBaseURL(cfg.BaseURL),
		enroll.WithBindingCodeTTL(cfg.BindingCodeTTL),
		enroll.WithLogger(log),
	)
	if err := enr.EnsureBound(ctx); err != nil {
		fatal(log, "EnsureBound", err)
	}
	log.Info("device bound")

	up := uploader.New(q, enr, cfg, uploader.WithLogger(log))

	startComputerSensor(ctx, q, enr, cfg, log)
	startDriveSensor(ctx, q, enr, cfg, log)
	startPrinterSensor(ctx, q, enr, cfg, log)

	log.Info("eco-agent started")
	err = up.Run(ctx)
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		log.Info("eco-agent stopped (shutdown signal)")
	case errors.Is(err, uploader.ErrStopped):
		log.Warn("eco-agent stopped (credentials revoked)")
	default:
		log.Warn("eco-agent stopped", "err", err)
	}
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

// startComputerSensor 啟動路徑 A；平台不支援活動偵測則記 log 並跳過（不影響其餘路徑）。
func startComputerSensor(ctx context.Context, q *queue.Queue, enr *enroll.Enroller, cfg config.Config, log *slog.Logger) {
	detector := platform.NewActivityDetector()
	if err := detector.Available(); err != nil {
		log.Warn("path A (computer) skipped", "err", err)
		return
	}
	s := computer.New(q, enr, detector, platform.NewCPUSampler(), cfg.ComputerUsageRecordInterval,
		computer.WithLogger(log))
	go func() {
		if err := s.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("path A (computer) exited", "err", err)
		}
	}()
	log.Info("path A (computer) started", "interval", cfg.ComputerUsageRecordInterval)
}

// startDriveSensor 啟動路徑 C；Google OAuth 憑證未設定或尚未完成首次授權則優雅降級跳過
// （CLAUDE.md §6：不使整個 Agent 崩潰），其餘路徑照常運作。
func startDriveSensor(ctx context.Context, q *queue.Queue, enr *enroll.Enroller, cfg config.Config, log *slog.Logger) {
	hc, err := drive.NewClient(ctx)
	if err != nil {
		if errors.Is(err, drive.ErrCredentialsNotConfigured) || errors.Is(err, drive.ErrNotAuthorized) {
			log.Warn("path C (drive) skipped", "err", err)
		} else {
			log.Warn("path C (drive) skipped (client init failed)", "err", err)
		}
		return
	}
	s := drive.NewSensor(q, enr, drive.NewAPIClient(hc), cfg.CheckInterval, cfg.DriveQuotaInterval,
		drive.WithSensorLogger(log))
	go func() {
		if err := s.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("path C (drive) exited", "err", err)
		}
	}()
	log.Info("path C (drive) started", "interval", cfg.DriveQuotaInterval)
}

// startPrinterSensor 啟動路徑 B；未設定 ECO_AGENT_PRINTER_HOST（本機無個人專屬印表機）
// 或連不到目標則記 log 並跳過（CLAUDE.md 3.4 BYOD 摩擦點），不使 Agent 卡住。
func startPrinterSensor(ctx context.Context, q *queue.Queue, enr *enroll.Enroller, cfg config.Config, log *slog.Logger) {
	client, err := printer.NewSNMPClientFromEnv()
	if err != nil {
		log.Warn("path B (printer) skipped", "err", err)
		return
	}
	s := printer.NewSensor(q, enr, client, cfg.CheckInterval, cfg.PrinterPollInterval,
		printer.WithSensorLogger(log))
	go func() {
		if err := s.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn("path B (printer) exited", "err", err)
		}
	}()
	addr, oid := client.Target()
	log.Info("path B (printer) started", "target", addr, "oid", oid, "interval", cfg.PrinterPollInterval)
}

func fatal(log *slog.Logger, msg string, err error) {
	log.Error(msg, "err", err)
	os.Exit(1)
}
