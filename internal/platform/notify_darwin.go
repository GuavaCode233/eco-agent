//go:build darwin

package platform

import "os/exec"

func setupNotifications(iconPNG []byte) error { return nil }

// notify 以 osascript 送出純提示通知。
//
// TODO(platform): 正式通知 API（UNUserNotificationCenter）需經簽章的 .app 程式包；osascript
// 的通知點擊後開啟的是「指令碼編輯程式」而非綁定頁，因此 url 無法使用，內文改為引導員工點選
// 選單列圖示（docs/Eco-Agent_系統匣UI規劃.md U4.9）。
func notify(title, body, url string) error {
	script := `display notification "` + appleScriptEscape(body) + `" with title "` + appleScriptEscape(title) + `"`
	return exec.Command("osascript", "-e", script).Run()
}
