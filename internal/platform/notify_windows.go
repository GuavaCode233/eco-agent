//go:build windows

package platform

import (
	"fmt"
	"os"
	"path/filepath"

	toast "git.sr.ht/~jackmordaunt/go-toast/v2"
)

func setupNotifications(iconPNG []byte) error {
	data := toast.AppData{AppID: NotificationAppName}
	if len(iconPNG) > 0 {
		if dir, err := os.UserConfigDir(); err == nil {
			p := filepath.Join(dir, "eco-agent", "notification-icon.png")
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err == nil && os.WriteFile(p, iconPNG, 0o600) == nil {
				data.IconPath = p
			}
		}
	}
	if err := toast.SetAppData(data); err != nil {
		return fmt.Errorf("platform: register notification app: %w", err)
	}
	return nil
}

func notify(title, body, url string) error {
	n := toast.Notification{
		AppID: NotificationAppName,
		Title: title,
		Body:  body,
		Audio: toast.Default,
	}
	if url != "" {
		// protocol 啟用：點擊時由 Windows 直接以預設瀏覽器開啟網址，不需 COM 回呼。
		n.ActivationType = toast.Protocol
		n.ActivationArguments = url
	}
	if err := n.Push(); err != nil {
		return fmt.Errorf("platform: push notification: %w", err)
	}
	return nil
}
