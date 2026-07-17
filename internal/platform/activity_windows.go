//go:build windows

package platform

import (
	"fmt"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Windows 活動偵測：user32!GetLastInputInfo() 取「最後輸入的 tick 值」，
// 再與 kernel32!GetTickCount() 現值相減得 idle 間隔（毫秒）。
//
// 免特殊權限、無頭背景可用（CLAUDE.md §1）。dwTime / GetTickCount 皆為 32-bit
// 無號毫秒（開機起算，約 49.7 天迴繞）；以無號減法相減，迴繞時自然得正確增量。
var (
	user32               = windows.NewLazySystemDLL("user32.dll")
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procGetLastInputInfo = user32.NewProc("GetLastInputInfo")
	procGetTickCount     = kernel32.NewProc("GetTickCount")
)

// lastInputInfo 對應 Win32 LASTINPUTINFO 結構。
type lastInputInfo struct {
	cbSize uint32
	dwTime uint32
}

type windowsActivityDetector struct{}

// NewActivityDetector 回傳 Windows 活動偵測實作。
func NewActivityDetector() ActivityDetector { return windowsActivityDetector{} }

// Available 檢查所需系統呼叫可解析（正常 Windows 桌面必然可用）。
func (windowsActivityDetector) Available() error {
	if err := procGetLastInputInfo.Find(); err != nil {
		return fmt.Errorf("platform: GetLastInputInfo unavailable: %w", err)
	}
	if err := procGetTickCount.Find(); err != nil {
		return fmt.Errorf("platform: GetTickCount unavailable: %w", err)
	}
	return nil
}

// IdleTime 回傳距上次輸入的間隔。
func (windowsActivityDetector) IdleTime() (time.Duration, error) {
	info := lastInputInfo{}
	info.cbSize = uint32(unsafe.Sizeof(info))
	r, _, callErr := procGetLastInputInfo.Call(uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0, fmt.Errorf("platform: GetLastInputInfo failed: %w", callErr)
	}
	tick, _, _ := procGetTickCount.Call()
	now := uint32(tick)
	// 無號減法：now < dwTime（tick 迴繞）時仍得正確的區間增量。
	idleMs := now - info.dwTime
	return time.Duration(idleMs) * time.Millisecond, nil
}
