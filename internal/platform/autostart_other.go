//go:build !windows

package platform

import "errors"

// TODO(platform): macOS 以 ~/Library/LaunchAgents/<label>.plist（RunAtLoad）實作開機自啟。
type unsupportedAutostart struct{}

func newAutostart() Autostart { return unsupportedAutostart{} }

func (unsupportedAutostart) Supported() bool        { return false }
func (unsupportedAutostart) Enabled() (bool, error) { return false, nil }
func (unsupportedAutostart) SetEnabled(bool) error {
	return errors.New("platform: autostart not supported on this platform")
}
