//go:build darwin

package platform

import (
	"os/exec"
	"strings"
)

// confirm 以 osascript 顯示確認框；使用者按「取消」時 osascript 以非 0 結束。
func confirm(title, message string) bool {
	script := `display dialog "` + appleScriptEscape(message) + `" with title "` + appleScriptEscape(title) +
		`" buttons {"取消", "確定"} default button "取消" cancel button "取消" with icon caution`
	return exec.Command("osascript", "-e", script).Run() == nil
}

// appleScriptEscape 跳脫 AppleScript 字串字面值中的反斜線與雙引號。
func appleScriptEscape(s string) string {
	return strings.NewReplacer(`\`, `\`, `"`, `\"`).Replace(s)
}
