//go:build !windows && !darwin

package platform

import "errors"

func setupNotifications(iconPNG []byte) error { return nil }

func notify(title, body, url string) error {
	return errors.New("platform: notifications not supported on this platform")
}
