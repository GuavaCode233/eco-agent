package platform

// 系統通知（docs/Eco-Agent_系統匣UI規劃.md U4.7–U4.9）。

// NotificationAppName 是通知上顯示的程式名稱（Windows 亦作為 AppUserModelID）。
const NotificationAppName = "Eco-Agent"

// SetupNotifications 於啟動時呼叫一次：Windows 會在 HKCU\Software\Classes\AppUserModelId
// 登記程式名稱與圖示（不需系統管理員權限），通知才會顯示「Eco-Agent」而非 PowerShell。
// iconPNG 會寫到使用者設定目錄供通知引用。其他平台為 no-op。
func SetupNotifications(iconPNG []byte) error { return setupNotifications(iconPNG) }

// Notify 送出一則系統通知；url 非空且平台支援時，點擊通知即以預設瀏覽器開啟該網址。
func Notify(title, body, url string) error { return notify(title, body, url) }
