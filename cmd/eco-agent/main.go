// Command eco-agent 是 Eco-Agent 的正式進入點（無人值守背景常駐程式）。
//
// 職責：載入配置（含 §後端串接 sensor_config 真串）、開啟本機持久化佇列、建立 enroll／
// uploader，交由 internal/agent 的監督者迴圈執行：綁定（失敗持續重試）→ 啟動三條感測路徑
// 與四重觸發上傳 → 遇解綁／撤銷／Refresh Token 失效回到綁定，直到結束（選單「結束」、
// 中斷訊號、Windows 登出／關機）。結束時 uploader 會先搶送佇列零頭（§3 關機／登出前 hook）。
//
// 預設以系統匣模式執行（docs/Eco-Agent_系統匣UI規劃.md U2–U4）：
//   - 本機狀態與綁定網頁＋系統匣（Windows 通知區域／macOS 選單列）；尚未綁定時自動開啟綁定頁。
//   - 關閉綁定頁約 5 秒後暫停綁定（不再打後端；背景分頁或最小化不算關閉），並以系統通知
//     提醒員工綁定（立即 1 次、BindingReminderDelay 後 1 次；冷卻期內略過立即那次）。
//   - 系統匣須在 main goroutine 執行，agent 與網頁另開 goroutine。
//
// 共通（所有模式）：
//   - 單一實例：本機已有 Eco-Agent 在執行時直接結束（U4.3）。
//   - log 同時寫入檔案（platform.LogDir()/agent.log，10MB×3 輪替）與標準輸出（U4.5）。
//   - Windows 登出／關機時先搶送零頭並在關機畫面顯示原因；睡眠喚醒後補送（U4.1、U4.2）。
//
// -headless：不開系統匣與網頁（開發、demo、無桌面環境）。沒有綁定頁可關閉，綁定階段維持
// 持續換碼，QR 內容（custom scheme URI）印在 log。
//
// 正式版 Windows 建置（無主控台視窗）：go build -ldflags "-H windowsgui" ./cmd/eco-agent
// （U4.6）；此建置沒有主控台，log 請看 log 檔。
//
// 員工端主動解綁（換機，進度表 6.1）：eco-agent -unbind [-force]
// ——採「先送再丟」：先上傳一次佇列；仍有送不出去的資料時，未加 -force 即中止並告知筆數，
// 加 -force 則捨棄後解綁。Eco-Agent 正在執行時請改從系統匣「解除綁定…」操作。
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
	"io"
	stdlog "log"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/natefinch/lumberjack.v2"

	"eco-agent/internal/agent"
	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/ui/remind"
	"eco-agent/internal/ui/tray"
	"eco-agent/internal/ui/web"
	"eco-agent/internal/uploader"
)

// EnvQueuePath 覆寫佇列 SQLite 檔案路徑。
const EnvQueuePath = "ECO_AGENT_QUEUE_PATH"

const (
	// shutdownWait 是結束時等待 agent 完成關機前搶送的上限（uploader 本身搶送逾時為 5 秒）。
	shutdownWait = 10 * time.Second
	// resumeFlushDelay 是喚醒後補送前的等待：剛喚醒時網路通常尚未恢復。
	resumeFlushDelay = 15 * time.Second
)

func main() {
	unbind := flag.Bool("unbind", false, "員工端主動解綁：先上傳佇列，再通知後端並清除本機憑證後結束")
	force := flag.Bool("force", false, "搭配 -unbind：佇列仍有送不出去的資料時直接捨棄")
	headless := flag.Bool("headless", false, "不開系統匣與狀態網頁，綁定 QR 內容印在 log（開發／demo 用）")
	flag.Parse()

	log, closeLog := setupLogging()
	defer closeLog()

	// 單一實例（U4.3）：兩個實例會同時寫同一個佇列與金鑰庫，所有模式都必須先取得鎖。
	release, err := platform.AcquireSingleInstance()
	if errors.Is(err, platform.ErrAlreadyRunning) {
		log.Warn("another Eco-Agent is already running, exiting")
		if !*headless {
			msg := "Eco-Agent 已在執行中，請從系統匣圖示開啟狀態頁或進行操作。"
			if *unbind {
				msg = "Eco-Agent 正在執行中，請從系統匣圖示選擇「解除綁定…」。"
			}
			platform.Alert("Eco-Agent", msg)
		}
		return
	}
	if err != nil {
		log.Warn("single instance check failed, continuing", "err", err)
	} else {
		defer release()
	}

	if p := config.LoadDotEnv(); p != "" {
		log.Info("已載入 .env", "path", p)
	}

	// 中斷訊號（Ctrl+C、macOS／Linux 登出時的 SIGTERM）視同「關機/登出前 hook」。
	// 結束原因由最先觸發的路徑記下，於「eco-agent stopped」log 輸出。
	reason := &stopReason{}
	ctx, stop := signalContext(reason)
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

	var (
		srv      *web.Server
		reminder *remind.Reminder
		pageURL  string // 綁定提醒點擊後開啟的網址；srv 建立後設定（提醒只會在 srv 服務後觸發）
	)
	if !*headless {
		reminder = newReminder(ag, cfg, log, func() string { return pageURL })
		defer reminder.Stop()
		srv, err = web.New(ag, web.WithLogger(log), web.WithOnPageClosed(reminder.PageClosed))
		if err != nil {
			// 網頁開不起來（例如本機無法 listen）時退回 headless，Agent 本身照常運作。
			log.Warn("status page unavailable, running headless", "err", err)
			srv = nil
		} else {
			pageURL = srv.URL()
		}
	}

	// 必須在 agent.Run 啟動前讀取：綁定中 EnsureBound 會持有 Enroller 鎖直到綁定碼逾時或被
	// 取消，之後才呼叫 IsBound 會卡住——系統匣因此延後約 15 秒才出現，且期間沒有開頁，
	// 開頁寬限期到期會誤判「頁面關閉」而暫停綁定、跳出提醒。
	bound, err := enr.IsBound()
	if err != nil {
		log.Warn("could not check binding state", "err", err)
	}

	agentCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	agentDone := make(chan struct{})
	go func() {
		defer close(agentDone)
		logAgentExit(log, ag.Run(agentCtx), reason)
	}()
	watchSession(agentCtx, cancel, agentDone, ag, reason, log)

	if srv == nil {
		log.Info("eco-agent started (headless)")
		<-agentDone
		return
	}

	go func() {
		if err := srv.Serve(agentCtx); err != nil {
			log.Warn("status page stopped", "err", err)
		}
	}()
	log.Info("status page", "url", srv.URL())
	if err := platform.SetupNotifications(tray.IconPNG()); err != nil {
		log.Warn("notification setup failed; binding reminders may show a generic app name", "err", err)
	}

	log.Info("eco-agent started (system tray)")
	// 系統匣在 main goroutine 阻塞到結束（選單「結束」、關機訊號、Windows 登出／關機）；
	// 結束時會取消 agent 並等待其完成關機前搶送。
	tray.Run(ctx, tray.Options{
		Agent:         ag,
		PageURL:       srv.URL(),
		AutoOpenPage:  !bound, // 首次啟動（尚未綁定）自動開啟綁定頁
		Cancel:        cancel,
		AgentDone:     agentDone,
		ShutdownWait:  shutdownWait,
		SetStopReason: reason.Set,
		Autostart:     platform.NewAutostart(),
		OpenBrowser:   platform.OpenBrowser,
		Confirm:       platform.Confirm,
		Log:           log,
	})
}

// newReminder 建立綁定提醒排程（U4.7）：頁面關閉時立即提醒 1 次、BindingReminderDelay 後
// 再 1 次；距上次提醒未滿 BindingReminderCooldown 則略過立即那次。
func newReminder(ag *agent.Agent, cfg config.Config, log *slog.Logger, pageURL func() string) *remind.Reminder {
	return remind.New(
		func() error {
			log.Info("sending binding reminder notification")
			return platform.Notify("Eco-Agent 尚未綁定",
				"這台電腦還沒有綁定員工帳號，能耗紀錄尚未開始。點這裡開啟綁定 QR Code，用 Eco-Sensing App 掃描即可完成。",
				pageURL())
		},
		func() bool { return ag.State() == agent.StateIdle },
		cfg.BindingReminderDelay, cfg.BindingReminderCooldown,
		remind.WithErrorHandler(func(err error) { log.Warn("binding reminder failed", "err", err) }),
	)
}

// watchSession 接上 Windows 登出／關機與喚醒事件（U4.1、U4.2）；其他平台為 no-op。
func watchSession(ctx context.Context, cancel context.CancelFunc, agentDone <-chan struct{}, ag *agent.Agent, reason *stopReason, log *slog.Logger) {
	err := platform.WatchSession(ctx, platform.SessionHandlers{
		ShutdownReason: "Eco-Agent 正在上傳尚未送出的能耗紀錄…",
		OnEndSession: func() {
			log.Info("session ending (logoff/shutdown), flushing queue")
			reason.Set(stopReasonSessionEnd)
			cancel()
			waitAgent(agentDone, log)
		},
		OnResume: func() {
			log.Info("resumed from sleep, flushing queue shortly")
			select {
			case <-ctx.Done():
				return
			case <-time.After(resumeFlushDelay):
			}
			if err := ag.FlushNow(ctx); err != nil && !errors.Is(err, agent.ErrNotBound) {
				log.Warn("flush after resume failed", "err", err)
			}
		},
	})
	if err != nil {
		log.Warn("session watcher unavailable; logoff/shutdown flush relies on the system tray", "err", err)
	}
}

func waitAgent(agentDone <-chan struct{}, log *slog.Logger) {
	select {
	case <-agentDone:
	case <-time.After(shutdownWait):
		log.Warn("agent did not stop in time", "wait", shutdownWait)
	}
}

// setupLogging 讓 log 同時寫入輪替的 log 檔與標準輸出（U4.5）。以 -H windowsgui 建置時沒有
// 主控台，寫入標準輸出會失敗，因此標準輸出的錯誤一律忽略、不影響 log 檔。
func setupLogging() (*slog.Logger, func()) {
	var w io.Writer = os.Stdout
	closeFn := func() {}
	dir, err := platform.LogDir()
	if err == nil {
		err = os.MkdirAll(dir, 0o700)
	}
	if err == nil {
		lj := &lumberjack.Logger{
			Filename:   filepath.Join(dir, "agent.log"),
			MaxSize:    10, // MB
			MaxBackups: 3,
		}
		w = teeWriter{primary: lj, secondary: os.Stdout}
		closeFn = func() { _ = lj.Close() }
	}
	log := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(log)
	stdlog.SetOutput(w) // 第三方套件（如 systray）以標準 log 輸出的訊息也寫進 log 檔
	if err != nil {
		log.Warn("log file unavailable, logging to stdout only", "err", err)
	} else {
		log.Info("logging to file", "path", filepath.Join(dir, "agent.log"))
	}
	return log, closeFn
}

// teeWriter 寫入 primary（回報其錯誤），並盡力寫入 secondary（忽略錯誤）。
type teeWriter struct {
	primary, secondary io.Writer
}

func (t teeWriter) Write(p []byte) (int, error) {
	_, _ = t.secondary.Write(p)
	return t.primary.Write(p)
}

// logAgentExit 記錄 agent 結束與原因（reason 見 stopreason.go 與 tray.StopReason*）。
func logAgentExit(log *slog.Logger, err error, reason *stopReason) {
	if err != nil && !errors.Is(err, context.Canceled) {
		log.Warn("eco-agent stopped", "reason", reason.String(), "err", err)
		return
	}
	log.Info("eco-agent stopped", "reason", reason.String())
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
