//go:build darwin

package platform

import (
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"time"
)

// macOS 活動偵測：以 ioreg 讀 IOHIDSystem 的 HIDIdleTime（奈秒），取距上次輸入的間隔。
//
// 純 Go、免 cgo，可自 Windows 交叉編譯 GOOS=darwin（符合 §8 交付：三平台編譯皆過）。
//
// 註（與 CLAUDE.md §1.1 用語校正）：原步驟表列 IOHIDGetModifierLockState()，該 API 實為
// 「修飾鍵鎖定狀態」查詢，非 idle 間隔；正確取 idle 間隔為 IOHIDSystem HIDIdleTime，或
// cgo 呼叫 CGEventSourceSecondsSinceLastEventType()。此處採 ioreg HIDIdleTime：語意正確、
// 免 cgo、免特殊授權（HIDIdleTime 不需 Accessibility 權限即可讀取）。
//
// TODO(platform): 若日後需更高精度或避免每次 fork ioreg，可改 cgo 直呼
// CGEventSourceSecondsSinceLastEventType（屆時 Accessibility 授權檢查與引導訊息放 Available）。
var hidIdleRe = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

type darwinActivityDetector struct{}

// NewActivityDetector 回傳 macOS 活動偵測實作。
func NewActivityDetector() ActivityDetector { return darwinActivityDetector{} }

// Available 檢查 ioreg 可用且能解析出 HIDIdleTime。
//
// HIDIdleTime 路徑不需 Accessibility 授權；此處僅驗證讀取管道通暢。若日後改採 cgo
// CGEventSource 路徑（需授權），把授權檢查與使用者引導訊息補於此。
func (d darwinActivityDetector) Available() error {
	if _, err := exec.LookPath("ioreg"); err != nil {
		return fmt.Errorf("platform: ioreg unavailable: %w", err)
	}
	if _, err := d.IdleTime(); err != nil {
		return fmt.Errorf("platform: cannot read HIDIdleTime: %w", err)
	}
	return nil
}

// IdleTime 回傳距上次輸入的間隔。
func (darwinActivityDetector) IdleTime() (time.Duration, error) {
	out, err := exec.Command("ioreg", "-c", "IOHIDSystem", "-d", "4").Output()
	if err != nil {
		return 0, fmt.Errorf("platform: ioreg exec: %w", err)
	}
	m := hidIdleRe.FindSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("platform: HIDIdleTime not found in ioreg output")
	}
	ns, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("platform: parse HIDIdleTime %q: %w", m[1], err)
	}
	// HIDIdleTime 單位為奈秒。
	return time.Duration(ns) * time.Nanosecond, nil
}
