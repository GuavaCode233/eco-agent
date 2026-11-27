package platform

import (
	"os"
	"path/filepath"
	"runtime"
)

// LogDir 回傳 Eco-Agent log 檔目錄（docs/Eco-Agent_系統匣UI規劃.md U4.5）：
// Windows %AppData%\eco-agent\logs、macOS ~/Library/Logs/eco-agent、其他為使用者設定目錄下
// eco-agent/logs。不建立目錄。
func LogDir() (string, error) {
	if runtime.GOOS == "darwin" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "Library", "Logs", "eco-agent"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "eco-agent", "logs"), nil
}
