// Package platform 封裝 OS 差異（金鑰庫、活動偵測、關機事件等）。
//
// 本檔提供系統金鑰庫抽象。依 v12 §1 / §4.4.2：Refresh Token 必須存系統金鑰庫
// （Windows DPAPI／macOS Keychain），不寫純文字檔。現階段以 MemoryKeychain（記憶體
// mock）滿足介面，真實 OS 實作日後補上，屆時只換實作、不動 enroll 等上層結構。
package platform

import (
	"errors"
	"sync"
)

// ErrKeychainNotFound 表示金鑰庫中不存在指定的鍵。
var ErrKeychainNotFound = errors.New("platform: keychain key not found")

// Keychain 抽象系統金鑰庫的讀寫刪。
//
// 真實實作（日後補）：
//   - Windows：DPAPI（golang.org/x/sys/windows，CryptProtectData/CryptUnprotectData）。
//   - macOS：Keychain Services（cgo 呼叫 Security.framework）。
//
// TODO(platform): 補上 keychain_windows.go / keychain_darwin.go（build tag 分平台），
// 以真實金鑰庫取代 MemoryKeychain；介面不變，上層無需修改。
type Keychain interface {
	// Get 讀取指定鍵的值；不存在時回傳 ErrKeychainNotFound。
	Get(key string) (string, error)
	// Set 寫入（或覆寫）指定鍵的值。
	Set(key, value string) error
	// Delete 刪除指定鍵；鍵不存在視為成功（冪等）。
	Delete(key string) error
}

// MemoryKeychain 為記憶體版金鑰庫，供現階段 mock 與單元測試使用。
//
// MOCK: 不落磁碟、不加密，重啟即清空——僅用於後端與真實金鑰庫就緒前。真實金鑰庫
// 會跨重啟持久保存 Refresh Token；本 mock 的「重啟清空」由 enroll 於下次啟動重新
// mock 綁定補回，不影響現階段流程驗證。
type MemoryKeychain struct {
	mu    sync.RWMutex
	items map[string]string
}

// NewMemoryKeychain 建立空的記憶體金鑰庫。
func NewMemoryKeychain() *MemoryKeychain {
	return &MemoryKeychain{items: make(map[string]string)}
}

// Get 實作 Keychain。
func (m *MemoryKeychain) Get(key string) (string, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.items[key]
	if !ok {
		return "", ErrKeychainNotFound
	}
	return v, nil
}

// Set 實作 Keychain。
func (m *MemoryKeychain) Set(key, value string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[key] = value
	return nil
}

// Delete 實作 Keychain（冪等）。
func (m *MemoryKeychain) Delete(key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.items, key)
	return nil
}

// 確保 MemoryKeychain 滿足 Keychain 介面。
var _ Keychain = (*MemoryKeychain)(nil)
