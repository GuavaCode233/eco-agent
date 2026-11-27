//go:build windows

package platform

import "golang.org/x/sys/windows"

const idYes = 6 // MessageBox 回傳值 IDYES

func confirm(title, message string) bool {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return false
	}
	m, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return false
	}
	// MB_TOPMOST／MB_SETFOREGROUND：由系統匣觸發時沒有前景視窗，確認框需主動浮到最上層。
	ret, _ := windows.MessageBox(0, m, t,
		windows.MB_YESNO|windows.MB_ICONWARNING|windows.MB_DEFBUTTON2|windows.MB_TOPMOST|windows.MB_SETFOREGROUND)
	return ret == idYes
}

func alert(title, message string) {
	t, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	m, err := windows.UTF16PtrFromString(message)
	if err != nil {
		return
	}
	windows.MessageBox(0, m, t, windows.MB_OK|windows.MB_ICONINFORMATION|windows.MB_TOPMOST|windows.MB_SETFOREGROUND)
}
