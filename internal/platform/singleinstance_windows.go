//go:build windows

package platform

import (
	"errors"
	"fmt"

	"golang.org/x/sys/windows"
)

// singleInstanceMutex 為具名 mutex；Local\ 前綴限定於目前登入工作階段（每位使用者各自一份）。
const singleInstanceMutex = `Local\EcoSensing.EcoAgent`

func acquireSingleInstance() (func(), error) {
	name, err := windows.UTF16PtrFromString(singleInstanceMutex)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateMutex(nil, false, name)
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		if h != 0 {
			windows.CloseHandle(h)
		}
		return nil, ErrAlreadyRunning
	}
	if err != nil {
		return nil, fmt.Errorf("platform: single instance mutex: %w", err)
	}
	return func() { windows.CloseHandle(h) }, nil
}
