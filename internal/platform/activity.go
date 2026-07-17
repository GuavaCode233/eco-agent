// 本檔提供跨平台「使用者活動偵測」抽象（CLAUDE.md Step 1.1）。
//
// 職責：回傳「距上次使用者輸入的間隔」（idle interval）——鍵盤/滑鼠等輸入自上次
// 發生至今經過多久。路徑 A（電腦使用，Step 1.3）據此把每個輪詢區間判為 active／idle
// 兩態；本層只提供原始間隔，不做 active/idle 分類、不算能耗（純感測，v13 [D7]）。
//
// 平台實作（build tag 分檔）：
//   - Windows：user32!GetLastInputInfo() + kernel32!GetTickCount()（activity_windows.go）。
//   - macOS：ioreg 讀 IOHIDSystem 的 HIDIdleTime（activity_darwin.go）。
//   - 其他：不支援，回 ErrActivityUnsupported（activity_other.go），確保各平台皆可編譯。
//
// 各平台實作皆提供 NewActivityDetector() 建構子，回傳滿足 ActivityDetector 的值；
// 上層（sensors/computer）僅依賴本介面，換平台不改上層結構。
package platform

import (
	"errors"
	"time"
)

// ErrActivityUnsupported 表示目前作業系統未提供活動偵測實作。
// 於不支援平台，Available() 回此錯誤、IdleTime() 亦回此錯誤。
var ErrActivityUnsupported = errors.New("platform: activity detection unsupported on this OS")

// ActivityDetector 抽象「距上次使用者輸入的間隔」查詢。並行安全由各實作保證
// （現有實作皆為無狀態、可並行呼叫）。
type ActivityDetector interface {
	// IdleTime 回傳距上次使用者輸入（鍵盤/滑鼠等）的間隔。
	// 平台不支援時回 ErrActivityUnsupported。
	IdleTime() (time.Duration, error)

	// Available 於啟動時檢查平台支援與必要授權；可用回 nil，否則回導引用錯誤。
	// 供 main 在啟動路徑 A 前先行檢查並給使用者引導訊息（如 macOS 授權提示）。
	Available() error
}
