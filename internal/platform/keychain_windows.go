//go:build windows

package platform

// Windows 金鑰庫實作（v12 §1 / §4.4.2；docs/Eco-Agent_後端串接改動清單.md §4）：以 DPAPI
// （CryptProtectData/CryptUnprotectData）加密每筆值，落地一個本機檔案
// （預設 %LOCALAPPDATA%\eco-agent\credentials.dat）。DPAPI 綁定「目前使用者帳號」：
// 加密輸出離開同一使用者、同一台機器即無法解密，等同金鑰庫等級保護，且不需另外管理密鑰。
//
// 檔案內容為 JSON（key → base64(DPAPI 密文)），逐鍵加密而非整檔加密一次，讓 Get/Set/Delete
// 可獨立操作、不需每次讀寫都解密所有既有項目。

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"golang.org/x/sys/windows"
)

// windowsKeychain 以 DPAPI 加密後存一個本機檔案。並行安全（單一 mutex 序列化讀寫，
// 佇列/金鑰庫存取頻率極低，序列化不構成效能問題）。
type windowsKeychain struct {
	mu   sync.Mutex
	path string
}

// NewOSKeychain 回傳 Windows DPAPI 金鑰庫實作。
func NewOSKeychain() Keychain { return newWindowsKeychain(defaultCredentialsPath()) }

// newWindowsKeychain 建立指向指定檔案路徑的金鑰庫；供測試指向暫存目錄。
func newWindowsKeychain(path string) *windowsKeychain {
	return &windowsKeychain{path: path}
}

// defaultCredentialsPath 回傳預設落地路徑：%LOCALAPPDATA%\eco-agent\credentials.dat。
func defaultCredentialsPath() string {
	dir := os.Getenv("LOCALAPPDATA")
	if dir == "" {
		if d, err := os.UserConfigDir(); err == nil {
			dir = d
		}
	}
	return filepath.Join(dir, "eco-agent", "credentials.dat")
}

// Get 實作 Keychain。
func (k *windowsKeychain) Get(key string) (string, error) {
	k.mu.Lock()
	defer k.mu.Unlock()

	items, err := k.load()
	if err != nil {
		return "", err
	}
	enc, ok := items[key]
	if !ok {
		return "", ErrKeychainNotFound
	}
	cipher, err := base64.StdEncoding.DecodeString(enc)
	if err != nil {
		return "", fmt.Errorf("platform: decode keychain entry %q: %w", key, err)
	}
	plain, err := dpapiUnprotect(cipher)
	if err != nil {
		return "", fmt.Errorf("platform: DPAPI unprotect %q: %w", key, err)
	}
	return string(plain), nil
}

// Set 實作 Keychain。
func (k *windowsKeychain) Set(key, value string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	items, err := k.load()
	if err != nil {
		return err
	}
	cipher, err := dpapiProtect([]byte(value))
	if err != nil {
		return fmt.Errorf("platform: DPAPI protect %q: %w", key, err)
	}
	items[key] = base64.StdEncoding.EncodeToString(cipher)
	return k.save(items)
}

// Delete 實作 Keychain（冪等）。
func (k *windowsKeychain) Delete(key string) error {
	k.mu.Lock()
	defer k.mu.Unlock()

	items, err := k.load()
	if err != nil {
		return err
	}
	if _, ok := items[key]; !ok {
		return nil
	}
	delete(items, key)
	return k.save(items)
}

// load 讀取並解析金鑰庫檔案；檔案不存在視為空金鑰庫（尚未寫入過任何值）。
func (k *windowsKeychain) load() (map[string]string, error) {
	data, err := os.ReadFile(k.path)
	if errors.Is(err, os.ErrNotExist) {
		return map[string]string{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("platform: read keychain file: %w", err)
	}
	items := map[string]string{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &items); err != nil {
			return nil, fmt.Errorf("platform: parse keychain file: %w", err)
		}
	}
	return items, nil
}

// save 以「寫暫存檔 + rename」原子落地，避免寫到一半崩潰造成檔案損毀。
func (k *windowsKeychain) save(items map[string]string) error {
	if err := os.MkdirAll(filepath.Dir(k.path), 0o700); err != nil {
		return fmt.Errorf("platform: create keychain dir: %w", err)
	}
	data, err := json.Marshal(items)
	if err != nil {
		return fmt.Errorf("platform: marshal keychain file: %w", err)
	}
	tmp := k.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Errorf("platform: write keychain file: %w", err)
	}
	if err := os.Rename(tmp, k.path); err != nil {
		return fmt.Errorf("platform: replace keychain file: %w", err)
	}
	return nil
}

// dpapiProtect 以目前使用者的 DPAPI 主金鑰加密 plain。CRYPTPROTECT_UI_FORBIDDEN 確保無頭
// 背景常駐（CLAUDE.md §1）不會因憑證異常跳出系統 UI 提示而卡住。
func dpapiProtect(plain []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(plain))}
	if len(plain) > 0 {
		in.Data = &plain[0]
	}
	var out windows.DataBlob
	if err := windows.CryptProtectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	buf := make([]byte, out.Size)
	copy(buf, unsafe.Slice(out.Data, out.Size))
	return buf, nil
}

// dpapiUnprotect 是 dpapiProtect 的反向操作。
func dpapiUnprotect(cipher []byte) ([]byte, error) {
	in := windows.DataBlob{Size: uint32(len(cipher))}
	if len(cipher) > 0 {
		in.Data = &cipher[0]
	}
	var out windows.DataBlob
	if err := windows.CryptUnprotectData(&in, nil, nil, 0, nil, windows.CRYPTPROTECT_UI_FORBIDDEN, &out); err != nil {
		return nil, err
	}
	defer windows.LocalFree(windows.Handle(unsafe.Pointer(out.Data)))
	buf := make([]byte, out.Size)
	copy(buf, unsafe.Slice(out.Data, out.Size))
	return buf, nil
}

// 確保 windowsKeychain 滿足 Keychain 介面。
var _ Keychain = (*windowsKeychain)(nil)
