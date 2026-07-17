//go:build !windows && !darwin

package platform

import "time"

// 非 Windows／macOS 平台（如 Linux 開發機）：活動偵測未實作，一律回 ErrActivityUnsupported。
// 用途是讓程式在所有平台皆可編譯與執行；路徑 A 於此類平台應優雅降級（記 log 並跳過）。
type unsupportedActivityDetector struct{}

// NewActivityDetector 回傳「不支援」實作。
func NewActivityDetector() ActivityDetector { return unsupportedActivityDetector{} }

// Available 回 ErrActivityUnsupported。
func (unsupportedActivityDetector) Available() error { return ErrActivityUnsupported }

// IdleTime 回 ErrActivityUnsupported。
func (unsupportedActivityDetector) IdleTime() (time.Duration, error) {
	return 0, ErrActivityUnsupported
}
