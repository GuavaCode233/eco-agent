// Package tray 是 Eco-Agent 的系統匣（docs/Eco-Agent_系統匣UI規劃.md U3），使用 fyne.io/systray。
//
// Run 必須在 main goroutine 呼叫（macOS 規定；systray 套件於 init 時 LockOSThread），會阻塞到
// 系統匣結束。Agent 本體（agent.Run）與本機網頁另在其他 goroutine 執行。
//
// 結束路徑（皆經 onExit）：
//   - 選單「結束」或收到關機訊號 → systray.Quit() → onExit
//   - Windows 登出／關機：systray 的隱藏視窗收到 WM_ENDSESSION 時，於視窗程序內同步呼叫 onExit；
//     onExit 取消 agent ctx 並等待 agent 結束（uploader 關機前搶送零頭）才返回，因此登出／
//     關機時同樣會先送出佇列零頭（§3 關機／登出前 hook）。
package tray

import (
	"context"
	_ "embed"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sync/atomic"
	"time"

	"fyne.io/systray"

	"eco-agent/internal/agent"
)

//go:embed icons/icon_ok.ico
var iconOKICO []byte

//go:embed icons/icon_warn.ico
var iconWarnICO []byte

//go:embed icons/icon_ok.png
var iconOKPNG []byte

//go:embed icons/icon_warn.png
var iconWarnPNG []byte

// refreshInterval 是系統匣狀態列的更新間隔。
const refreshInterval = 2 * time.Second

// Agent 是系統匣依賴的 agent 介面（*agent.Agent 滿足）。
type Agent interface {
	Status(ctx context.Context) agent.Status
	StartBinding()
	Rebind()
	FlushNow(ctx context.Context) error
	Unbind(ctx context.Context, confirm func(leftover int) bool) error
}

// Options 設定系統匣。
type Options struct {
	Agent   Agent
	PageURL string // 本機狀態與綁定頁網址（web.Server.URL()）

	// AutoOpenPage 為 true 時，系統匣就緒後立即開啟綁定頁（首次啟動尚未綁定，U3.2）。
	AutoOpenPage bool

	// Cancel 取消 agent ctx；AgentDone 於 agent.Run 返回後關閉。
	Cancel    context.CancelFunc
	AgentDone <-chan struct{}
	// ShutdownWait 為結束時等待 agent 搶送零頭的上限（預設 10 秒）。
	ShutdownWait time.Duration

	OpenBrowser func(url string) error
	Confirm     func(title, message string) bool
	Log         *slog.Logger
}

// Run 建立系統匣並阻塞到結束。ctx 取消（關機訊號）或 agent 結束時系統匣也會結束。
func Run(ctx context.Context, o Options) {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.ShutdownWait == 0 {
		o.ShutdownWait = 10 * time.Second
	}
	t := &tray{o: o, ctx: ctx}
	systray.Run(t.onReady, t.onExit)
}

type tray struct {
	o   Options
	ctx context.Context

	status, open, flush, unbind, rebind, quit *systray.MenuItem

	busy     atomic.Bool // 解綁等長動作進行中，避免重複觸發
	lastWarn atomic.Int32
}

func (t *tray) onReady() {
	systray.SetTitle("") // macOS 選單列只顯示圖示
	t.status = systray.AddMenuItem("啟動中…", "")
	t.status.Disable()
	systray.AddSeparator()
	t.open = systray.AddMenuItem("開啟狀態頁", "以瀏覽器開啟 Eco-Agent 狀態頁")
	t.flush = systray.AddMenuItem("立即上傳", "立即上傳尚未送出的紀錄")
	t.unbind = systray.AddMenuItem("解除綁定…", "解除這台電腦與目前員工帳號的綁定")
	t.rebind = systray.AddMenuItem("重新綁定", "綁定已撤銷，重新掃碼綁定這台電腦")
	t.rebind.Hide()
	systray.AddSeparator()
	t.quit = systray.AddMenuItem("結束 Eco-Agent", "結束程式（會先上傳尚未送出的紀錄）")

	t.lastWarn.Store(-1)
	t.refresh()

	if t.o.AutoOpenPage {
		t.openPage()
	}

	go t.loop()
}

// loop 處理選單點擊與定時更新，直到 ctx 取消或 agent 結束。
func (t *tray) loop() {
	tick := time.NewTicker(refreshInterval)
	defer tick.Stop()
	for {
		select {
		case <-t.ctx.Done():
			systray.Quit()
			return
		case <-t.o.AgentDone:
			systray.Quit()
			return
		case <-tick.C:
			t.refresh()
		case <-t.open.ClickedCh:
			t.openPage()
		case <-t.flush.ClickedCh:
			go t.flushNow()
		case <-t.unbind.ClickedCh:
			go t.doUnbind()
		case <-t.rebind.ClickedCh:
			t.o.Agent.Rebind()
			t.refresh()
			t.openPage()
		case <-t.quit.ClickedCh:
			systray.Quit()
			return
		}
	}
}

// refresh 依 agent 狀態更新狀態列、圖示與各項目可用狀態。
func (t *tray) refresh() {
	v := viewOf(t.o.Agent.Status(t.ctx), time.Now())
	t.status.SetTitle(v.Status)
	systray.SetTooltip(v.Tooltip)
	t.open.SetTitle(v.OpenTitle)
	setEnabled(t.flush, v.CanFlush)
	setEnabled(t.unbind, v.CanUnbind && !t.busy.Load())
	if v.ShowRebind {
		t.rebind.Show()
	} else {
		t.rebind.Hide()
	}

	warn := int32(0)
	if v.Warn {
		warn = 1
	}
	if t.lastWarn.Swap(warn) != warn { // 圖示只在狀態改變時更新（Windows 每次都會寫暫存檔）
		setIcon(v.Warn)
	}
}

// openPage 開啟本機狀態頁；閒置（綁定頁已關閉）時先恢復綁定，讓頁面一開就有 QR。
func (t *tray) openPage() {
	if t.o.Agent.Status(t.ctx).State == agent.StateIdle {
		t.o.Agent.StartBinding()
	}
	if err := t.o.OpenBrowser(t.o.PageURL); err != nil {
		t.o.Log.Warn("tray: open status page failed", "err", err, "url", t.o.PageURL)
	}
}

func (t *tray) flushNow() {
	if err := t.o.Agent.FlushNow(t.ctx); err != nil {
		t.o.Log.Warn("tray: manual upload failed", "err", err)
	}
	t.refresh()
}

// doUnbind 解綁流程（「先送再丟」，§2）：先確認意圖，有送不出去的紀錄時再確認一次捨棄。
func (t *tray) doUnbind() {
	if !t.busy.CompareAndSwap(false, true) {
		return
	}
	defer func() {
		t.busy.Store(false)
		t.refresh()
	}()
	t.unbind.Disable()

	if !t.o.Confirm("解除綁定",
		"確定要解除這台電腦與目前員工帳號的綁定嗎？\n\n解除前會先上傳尚未送出的紀錄；解除後需重新掃碼綁定才能繼續記錄。") {
		return
	}
	err := t.o.Agent.Unbind(t.ctx, func(leftover int) bool {
		return t.o.Confirm("解除綁定",
			fmt.Sprintf("有 %d 筆紀錄目前無法上傳（可能是網路或伺服器問題）。\n\n繼續解除綁定將捨棄這些紀錄，確定要繼續嗎？", leftover))
	})
	switch {
	case err == nil:
		t.o.Log.Info("tray: device unbound")
		t.refresh()
		t.openPage() // 解綁後回到綁定中：直接顯示新的綁定 QR
	case errors.Is(err, agent.ErrUnbindCancelled):
		t.o.Log.Info("tray: unbind cancelled")
	default:
		t.o.Log.Warn("tray: unbind failed", "err", err)
	}
}

// onExit 於系統匣結束時（含 Windows WM_ENDSESSION）在事件迴圈內同步執行：取消 agent 並等待
// 其完成關機前搶送，上限 ShutdownWait。
func (t *tray) onExit() {
	t.o.Cancel()
	select {
	case <-t.o.AgentDone:
	case <-time.After(t.o.ShutdownWait):
		t.o.Log.Warn("tray: agent did not stop in time", "wait", t.o.ShutdownWait)
	}
}

func setEnabled(m *systray.MenuItem, on bool) {
	if on {
		m.Enable()
	} else {
		m.Disable()
	}
}

// setIcon 依平台選擇圖示格式：Windows 需 .ico，其他平台用 .png。
func setIcon(warn bool) {
	if runtime.GOOS == "windows" {
		if warn {
			systray.SetIcon(iconWarnICO)
		} else {
			systray.SetIcon(iconOKICO)
		}
		return
	}
	if warn {
		systray.SetIcon(iconWarnPNG)
	} else {
		systray.SetIcon(iconOKPNG)
	}
}
