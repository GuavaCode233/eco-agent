//go:build windows

package platform

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 實作：建立一個不顯示的**一般頂層視窗**接收工作階段訊息。不可用 message-only 視窗
// （HWND_MESSAGE）：它收不到廣播的 WM_QUERYENDSESSION／WM_ENDSESSION／WM_POWERBROADCAST。

var (
	// user32 定義於 activity_windows.go，同套件共用。
	procRegisterClassExW           = user32.NewProc("RegisterClassExW")
	procCreateWindowExW            = user32.NewProc("CreateWindowExW")
	procDefWindowProcW             = user32.NewProc("DefWindowProcW")
	procGetMessageW                = user32.NewProc("GetMessageW")
	procTranslateMessage           = user32.NewProc("TranslateMessage")
	procDispatchMessageW           = user32.NewProc("DispatchMessageW")
	procPostMessageW               = user32.NewProc("PostMessageW")
	procDestroyWindow              = user32.NewProc("DestroyWindow")
	procPostQuitMessage            = user32.NewProc("PostQuitMessage")
	procShutdownBlockReasonCreate  = user32.NewProc("ShutdownBlockReasonCreate")
	procShutdownBlockReasonDestroy = user32.NewProc("ShutdownBlockReasonDestroy")
)

const (
	wmDestroy             = 0x0002
	wmClose               = 0x0010
	wmQueryEndSession     = 0x0011
	wmEndSession          = 0x0016
	wmPowerBroadcast      = 0x0218
	pbtAPMResumeAutomatic = 0x0012
)

type wndClassEx struct {
	Size       uint32
	Style      uint32
	WndProc    uintptr
	ClsExtra   int32
	WndExtra   int32
	Instance   windows.Handle
	Icon       windows.Handle
	Cursor     windows.Handle
	Background windows.Handle
	MenuName   *uint16
	ClassName  *uint16
	IconSm     windows.Handle
}

type msg struct {
	Hwnd    windows.HWND
	Message uint32
	WParam  uintptr
	LParam  uintptr
	Time    uint32
	Pt      struct{ X, Y int32 }
}

// 視窗程序只能是單一全域 callback（windows.NewCallback 數量有限），故以全域變數保存目前的回呼。
var (
	sessionMu       sync.Mutex
	sessionHandlers SessionHandlers
	sessionWndProc  uintptr
	sessionOnce     sync.Once
)

func sessionProc(hwnd windows.HWND, message uint32, wParam, lParam uintptr) uintptr {
	sessionMu.Lock()
	h := sessionHandlers
	sessionMu.Unlock()

	switch message {
	case wmQueryEndSession:
		// 允許關機（回 TRUE），同時登記原因：若搶送拖到關機畫面出現，員工會看到原因而非空白的
		// 「此應用程式正在阻止關機」。
		if h.ShutdownReason != "" {
			if p, err := windows.UTF16PtrFromString(h.ShutdownReason); err == nil {
				procShutdownBlockReasonCreate.Call(uintptr(hwnd), uintptr(unsafe.Pointer(p)))
			}
		}
		return 1
	case wmEndSession:
		if wParam != 0 && h.OnEndSession != nil { // wParam=TRUE：工作階段確定結束
			h.OnEndSession()
		}
		procShutdownBlockReasonDestroy.Call(uintptr(hwnd))
		return 0
	case wmPowerBroadcast:
		if wParam == pbtAPMResumeAutomatic && h.OnResume != nil {
			go h.OnResume()
		}
		return 1
	case wmClose:
		procDestroyWindow.Call(uintptr(hwnd))
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(message), wParam, lParam)
	return r
}

func watchSession(ctx context.Context, h SessionHandlers) error {
	sessionMu.Lock()
	sessionHandlers = h
	sessionMu.Unlock()
	sessionOnce.Do(func() { sessionWndProc = windows.NewCallback(sessionProc) })

	ready := make(chan error, 1)
	go func() {
		// 視窗與其訊息迴圈必須在同一條 OS 執行緒。
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()

		hwnd, err := createSessionWindow()
		if err != nil {
			ready <- err
			return
		}
		ready <- nil

		go func() {
			<-ctx.Done()
			procPostMessageW.Call(uintptr(hwnd), wmClose, 0, 0)
		}()

		var m msg
		for {
			r, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
			if int32(r) <= 0 { // 0 = WM_QUIT、-1 = 錯誤
				return
			}
			procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
			procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
		}
	}()
	return <-ready
}

func createSessionWindow() (windows.HWND, error) {
	var inst windows.Handle
	if err := windows.GetModuleHandleEx(0, nil, &inst); err != nil {
		return 0, fmt.Errorf("platform: session window: module handle: %w", err)
	}
	className, _ := windows.UTF16PtrFromString("EcoAgentSessionWindow")
	wc := wndClassEx{
		WndProc:   sessionWndProc,
		Instance:  inst,
		ClassName: className,
	}
	wc.Size = uint32(unsafe.Sizeof(wc))
	if r, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		// 重複註冊（同一行程第二次 WatchSession）可忽略。
		if err != windows.ERROR_CLASS_ALREADY_EXISTS {
			return 0, fmt.Errorf("platform: session window: register class: %w", err)
		}
	}
	title, _ := windows.UTF16PtrFromString("Eco-Agent")
	hwnd, _, err := procCreateWindowExW.Call(0,
		uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(title)),
		0,          // WS_OVERLAPPED，不呼叫 ShowWindow → 不顯示
		0, 0, 0, 0, // 位置與大小
		0, 0, // 無父視窗（頂層）、無選單
		uintptr(inst), 0)
	if hwnd == 0 {
		return 0, fmt.Errorf("platform: session window: create: %w", err)
	}
	return windows.HWND(hwnd), nil
}
