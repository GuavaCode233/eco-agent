//go:build !windows && !darwin

package platform

// confirm 在不支援的平台一律回 false（見 Confirm）。
func confirm(title, message string) bool { return false }
