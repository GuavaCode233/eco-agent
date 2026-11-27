package platform

import (
	"fmt"
	"net/url"
	"os/exec"
	"runtime"
)

// OpenBrowser 以系統預設瀏覽器開啟 http(s) 網址（本機綁定頁，docs/Eco-Agent_系統匣UI規劃.md U2）。
// 只接受 http/https，避免把任意字串交給系統的 URL 處理程式。
func OpenBrowser(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("platform: open browser: refusing non-http URL %q", rawURL)
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// url.dll 的 FileProtocolHandler 交給預設瀏覽器，且不經 cmd.exe 解析（網址中的 & 等字元安全）。
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u.String())
	case "darwin":
		cmd = exec.Command("open", u.String())
	default:
		cmd = exec.Command("xdg-open", u.String())
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("platform: open browser: %w", err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}
