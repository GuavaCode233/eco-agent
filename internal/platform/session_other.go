//go:build !windows

package platform

import "context"

// watchSession 在非 Windows 平台為 no-op：macOS／Linux 登出、關機時行程會收到 SIGTERM，
// 已由 cmd/eco-agent 的 signal.NotifyContext 觸發關機前搶送。
//
// TODO(platform): macOS 以 NSWorkspaceWillPowerOffNotification／NSWorkspaceDidWakeNotification
// 補上關機前 hook 與喚醒後補送（需 cgo，與 keychain_darwin.go 相同方式）。
func watchSession(ctx context.Context, h SessionHandlers) error { return nil }
