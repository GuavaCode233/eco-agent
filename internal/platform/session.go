package platform

import "context"

// SessionHandlers 是作業系統工作階段事件的回呼（docs/Eco-Agent_系統匣UI規劃.md U4.1、U4.2）。
type SessionHandlers struct {
	// ShutdownReason 為登出／關機期間顯示在 Windows 關機畫面的原因文字。
	ShutdownReason string
	// OnEndSession 於登出／關機確定進行時同步呼叫；應取消 agent 並等待關機前搶送完成才返回
	// （返回後行程即可能被終止）。
	OnEndSession func()
	// OnResume 於睡眠／休眠喚醒後呼叫（另開 goroutine，不阻塞事件處理）。
	OnResume func()
}

// WatchSession 開始監看登出／關機與喚醒事件，直到 ctx 取消。啟動失敗回傳錯誤（呼叫端應僅
// 記 log：系統匣模式下 systray 本身也會在 WM_ENDSESSION 時觸發關機前搶送）。
// 不支援的平台為 no-op。
func WatchSession(ctx context.Context, h SessionHandlers) error { return watchSession(ctx, h) }
