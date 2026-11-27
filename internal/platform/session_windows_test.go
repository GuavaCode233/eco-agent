//go:build windows

package platform

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	procFindWindowW  = user32.NewProc("FindWindowW")
	procSendMessageW = user32.NewProc("SendMessageW")
)

// 只對本測試建立的隱藏視窗送訊息，不影響系統。
func TestSessionWindowHandlesEndSessionAndResume(t *testing.T) {
	var ended, resumed int32
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := WatchSession(ctx, SessionHandlers{
		ShutdownReason: "test",
		OnEndSession:   func() { atomic.AddInt32(&ended, 1) },
		OnResume:       func() { atomic.AddInt32(&resumed, 1) },
	}); err != nil {
		t.Fatalf("WatchSession: %v", err)
	}

	cls, _ := windows.UTF16PtrFromString("EcoAgentSessionWindow")
	hwnd, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0)
	if hwnd == 0 {
		t.Fatal("session window not found")
	}

	if r, _, _ := procSendMessageW.Call(hwnd, wmQueryEndSession, 0, 0); r != 1 {
		t.Fatalf("WM_QUERYENDSESSION returned %d, want 1 (allow shutdown)", r)
	}
	procSendMessageW.Call(hwnd, wmEndSession, 0, 0) // wParam=FALSE：關機被取消，不應觸發
	if n := atomic.LoadInt32(&ended); n != 0 {
		t.Fatalf("OnEndSession called %d times for cancelled shutdown, want 0", n)
	}
	procSendMessageW.Call(hwnd, wmEndSession, 1, 0) // SendMessage 同步：返回時回呼已執行完
	if n := atomic.LoadInt32(&ended); n != 1 {
		t.Fatalf("OnEndSession calls = %d, want 1", n)
	}

	procSendMessageW.Call(hwnd, wmPowerBroadcast, pbtAPMResumeAutomatic, 0)
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&resumed) == 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if n := atomic.LoadInt32(&resumed); n != 1 {
		t.Fatalf("OnResume calls = %d, want 1", n)
	}

	cancel()
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if h, _, _ := procFindWindowW.Call(uintptr(unsafe.Pointer(cls)), 0); h == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("session window not destroyed after ctx cancel")
}
