//go:build !windows && !darwin

package platform

import (
	"log/slog"
	"runtime"
)

// NewOSKeychain 在無原生金鑰庫實作的平台（Windows／macOS 以外）fallback 到 MemoryKeychain，
// 並記 log 警告——不落磁碟，Refresh Token 無法跨重啟持久保存（docs/Eco-Agent_後端串接改動
// 清單.md §4）。CLAUDE.md 僅要求 Windows／macOS 兩平台，此為超出範圍平台的安全退路，
// 避免 Agent 在其上直接崩潰或誤以為有金鑰庫保護。
func NewOSKeychain() Keychain {
	slog.Default().Warn(
		"platform: no native keychain implementation for this OS, falling back to in-memory "+
			"(refresh token will NOT persist across restarts)",
		"goos", runtime.GOOS,
	)
	return NewMemoryKeychain()
}
