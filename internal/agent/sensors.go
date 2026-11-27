package agent

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/sensors/computer"
	"eco-agent/internal/sensors/drive"
	"eco-agent/internal/sensors/printer"
)

// DefaultSensors 回傳啟動真實三路徑的 SensorStarter（自 cmd/eco-agent 移入）。任一路徑
// 不可用（平台不支援、未設定憑證、無印表機）僅記 log 並跳過，不影響其餘路徑（CLAUDE.md
// §6、3.4）。每次進入已綁定階段（及解綁取消後恢復）都會重新呼叫。
func DefaultSensors(q *queue.Queue, enr *enroll.Enroller, cfg config.Config, log *slog.Logger) SensorStarter {
	return func(ctx context.Context, wg *sync.WaitGroup) map[queue.PathType]bool {
		return map[queue.PathType]bool{
			queue.PathComputer: startComputerSensor(ctx, wg, q, enr, cfg, log),
			queue.PathDrive:    startDriveSensor(ctx, wg, q, enr, cfg, log),
			queue.PathPrinter:  startPrinterSensor(ctx, wg, q, enr, cfg, log),
		}
	}
}

// runSensor 以 goroutine 執行感測器 Run，並登記於 wg。
func runSensor(ctx context.Context, wg *sync.WaitGroup, log *slog.Logger, name string, run func(context.Context) error) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			log.Warn(name+" exited", "err", err)
		}
	}()
}

// startComputerSensor 啟動路徑 A；平台不支援活動偵測則記 log 並跳過。
func startComputerSensor(ctx context.Context, wg *sync.WaitGroup, q *queue.Queue, enr *enroll.Enroller, cfg config.Config, log *slog.Logger) bool {
	detector := platform.NewActivityDetector()
	if err := detector.Available(); err != nil {
		log.Warn("path A (computer) skipped", "err", err)
		return false
	}
	s := computer.New(q, enr, detector, platform.NewCPUSampler(), cfg.ComputerUsageRecordInterval,
		computer.WithLogger(log))
	runSensor(ctx, wg, log, "path A (computer)", s.Run)
	log.Info("path A (computer) started", "interval", cfg.ComputerUsageRecordInterval)
	return true
}

// startDriveSensor 啟動路徑 C；Google OAuth 憑證未設定或尚未完成首次授權則優雅降級跳過。
func startDriveSensor(ctx context.Context, wg *sync.WaitGroup, q *queue.Queue, enr *enroll.Enroller, cfg config.Config, log *slog.Logger) bool {
	// ctx 會被 oauth2 TokenSource 保存並用於日後換發 token，必須是 session ctx（不可加逾時）。
	hc, err := drive.NewClient(ctx)
	if err != nil {
		if errors.Is(err, drive.ErrCredentialsNotConfigured) || errors.Is(err, drive.ErrNotAuthorized) {
			log.Warn("path C (drive) skipped", "err", err)
		} else {
			log.Warn("path C (drive) skipped (client init failed)", "err", err)
		}
		return false
	}
	s := drive.NewSensor(q, enr, drive.NewAPIClient(hc), cfg.CheckInterval, cfg.DriveQuotaInterval,
		drive.WithSensorLogger(log))
	runSensor(ctx, wg, log, "path C (drive)", s.Run)
	log.Info("path C (drive) started", "interval", cfg.DriveQuotaInterval)
	return true
}

// startPrinterSensor 啟動路徑 B；未設定 ECO_AGENT_PRINTER_HOST 或連不到目標則記 log 並跳過。
func startPrinterSensor(ctx context.Context, wg *sync.WaitGroup, q *queue.Queue, enr *enroll.Enroller, cfg config.Config, log *slog.Logger) bool {
	client, err := printer.NewSNMPClientFromEnv()
	if err != nil {
		log.Warn("path B (printer) skipped", "err", err)
		return false
	}
	s := printer.NewSensor(q, enr, client, cfg.CheckInterval, cfg.PrinterPollInterval,
		printer.WithSensorLogger(log))
	runSensor(ctx, wg, log, "path B (printer)", s.Run)
	addr, oid := client.Target()
	log.Info("path B (printer) started", "target", addr, "oid", oid, "interval", cfg.PrinterPollInterval)
	return true
}
