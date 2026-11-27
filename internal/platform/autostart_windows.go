//go:build windows

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/registry"
)

const runValueName = "EcoAgent"

// runKeyPath 為 HKCU 下的開機自啟登錄機碼；測試時改指向暫用機碼，避免動到真正的 Run。
var runKeyPath = `Software\Microsoft\Windows\CurrentVersion\Run`

// windowsAutostart 寫入 HKCU\...\Run（目前使用者、不需系統管理員權限）。
type windowsAutostart struct {
	exe string // 目前執行檔絕對路徑；無法取得或為暫存建置（go run）時為空
}

func newAutostart() Autostart {
	exe, err := os.Executable()
	if err != nil {
		return windowsAutostart{}
	}
	if p, err := filepath.EvalSymlinks(exe); err == nil {
		exe = p
	}
	// go run 的執行檔在暫存目錄、每次都不同，設成開機自啟沒有意義。
	if isTempBuild(exe) {
		return windowsAutostart{}
	}
	return windowsAutostart{exe: exe}
}

func isTempBuild(exe string) bool {
	tmp, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		tmp = os.TempDir()
	}
	lower := strings.ToLower(exe)
	return strings.HasPrefix(lower, strings.ToLower(tmp)) || strings.Contains(lower, `\go-build`)
}

func (a windowsAutostart) Supported() bool { return a.exe != "" }

func (a windowsAutostart) command() string { return `"` + a.exe + `"` }

func (a windowsAutostart) Enabled() (bool, error) {
	if !a.Supported() {
		return false, nil
	}
	k, err := registry.OpenKey(registry.CURRENT_USER, runKeyPath, registry.QUERY_VALUE)
	if err != nil {
		return false, fmt.Errorf("platform: autostart: open run key: %w", err)
	}
	defer k.Close()
	v, _, err := k.GetStringValue(runValueName)
	if errors.Is(err, registry.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("platform: autostart: read: %w", err)
	}
	// 指向其他位置（例如舊版安裝路徑）視為未啟用，勾選時會改寫為目前路徑。
	return strings.EqualFold(v, a.command()), nil
}

func (a windowsAutostart) SetEnabled(on bool) error {
	if !a.Supported() {
		return errors.New("platform: autostart not supported for this executable")
	}
	k, _, err := registry.CreateKey(registry.CURRENT_USER, runKeyPath, registry.SET_VALUE)
	if err != nil {
		return fmt.Errorf("platform: autostart: open run key: %w", err)
	}
	defer k.Close()
	if on {
		if err := k.SetStringValue(runValueName, a.command()); err != nil {
			return fmt.Errorf("platform: autostart: write: %w", err)
		}
		return nil
	}
	if err := k.DeleteValue(runValueName); err != nil && !errors.Is(err, registry.ErrNotExist) {
		return fmt.Errorf("platform: autostart: delete: %w", err)
	}
	return nil
}
