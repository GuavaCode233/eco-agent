//go:build unix

package platform

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// acquireSingleInstance 以使用者設定目錄下的 lock 檔加 flock（非阻塞）實作；行程結束時
// 作業系統會自動釋放鎖，崩潰後不會殘留。
func acquireSingleInstance() (func(), error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return nil, fmt.Errorf("platform: single instance: %w", err)
	}
	dir = filepath.Join(dir, "eco-agent")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("platform: single instance: %w", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "agent.lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("platform: single instance: %w", err)
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, fmt.Errorf("platform: single instance: flock: %w", err)
	}
	return func() { f.Close() }, nil
}
