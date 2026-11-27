package main

import (
	"context"
	"os"
	"os/signal"
	"sync"
	"syscall"
)

// 結束原因（記入「eco-agent stopped」log 的 reason 欄位）。系統匣回報的原因見 tray.StopReason*。
const (
	stopReasonInterrupt  = "signal_interrupt" // Ctrl+C
	stopReasonTerminate  = "signal_terminate" // SIGTERM；Windows 主控台程式的關閉視窗／登出／關機也會轉成此訊號
	stopReasonSessionEnd = "session_end"      // Windows 登出／關機（工作階段視窗收到 WM_ENDSESSION）
	stopReasonUnknown    = "unknown"
)

// stopReason 記錄程式結束的原因，先記先贏：多個結束路徑可能接連觸發（例如登出時工作階段
// 視窗與系統匣視窗都會收到 WM_ENDSESSION），只保留最先發生、也就是真正的起因。
type stopReason struct {
	mu     sync.Mutex
	reason string
}

func (r *stopReason) Set(reason string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reason == "" {
		r.reason = reason
	}
}

func (r *stopReason) String() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.reason == "" {
		return stopReasonUnknown
	}
	return r.reason
}

// signalContext 與 signal.NotifyContext 相同，但會把收到的訊號記成結束原因。
func signalContext(reason *stopReason) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case sig := <-ch:
			if sig == os.Interrupt {
				reason.Set(stopReasonInterrupt)
			} else {
				reason.Set(stopReasonTerminate)
			}
			cancel()
		case <-ctx.Done():
		}
	}()
	return ctx, func() {
		signal.Stop(ch)
		cancel()
	}
}
