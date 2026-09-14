//go:build windows

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newTestWindowsKeychain(t *testing.T) *windowsKeychain {
	t.Helper()
	return newWindowsKeychain(filepath.Join(t.TempDir(), "credentials.dat"))
}

// TestWindowsKeychainRoundTrip 驗證 DPAPI 加密/解密往返正確：Set 後 Get 應取回原值，
// 且落地檔案內容不是明文（DPAPI 密文，§4.4.2 不寫純文字檔）。
func TestWindowsKeychainRoundTrip(t *testing.T) {
	k := newTestWindowsKeychain(t)

	if err := k.Set("eco-agent.refresh_token", "super-secret-refresh-token"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	got, err := k.Get("eco-agent.refresh_token")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got != "super-secret-refresh-token" {
		t.Fatalf("Get = %q, want original value", got)
	}

	raw, err := os.ReadFile(k.path)
	if err != nil {
		t.Fatalf("read keychain file: %v", err)
	}
	if string(raw) == "" {
		t.Fatal("keychain file is empty after Set")
	}
	if strings.Contains(string(raw), "super-secret-refresh-token") {
		t.Fatal("keychain file on disk contains the plaintext value; must be DPAPI-encrypted")
	}
}

// TestWindowsKeychainGetMissingKey 驗證不存在的鍵回 ErrKeychainNotFound。
func TestWindowsKeychainGetMissingKey(t *testing.T) {
	k := newTestWindowsKeychain(t)
	if _, err := k.Get("nope"); !errors.Is(err, ErrKeychainNotFound) {
		t.Fatalf("Get missing key = %v, want ErrKeychainNotFound", err)
	}
}

// TestWindowsKeychainDeleteIdempotent 驗證 Delete 冪等：鍵不存在時 Delete 不報錯。
func TestWindowsKeychainDeleteIdempotent(t *testing.T) {
	k := newTestWindowsKeychain(t)
	if err := k.Delete("never-set"); err != nil {
		t.Fatalf("Delete missing key: %v", err)
	}

	if err := k.Set("k", "v"); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if err := k.Delete("k"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := k.Get("k"); !errors.Is(err, ErrKeychainNotFound) {
		t.Fatalf("Get after Delete = %v, want ErrKeychainNotFound", err)
	}
}

// TestWindowsKeychainPersistsAcrossInstances 驗證跨程序重啟仍讀得到（模擬重啟：同一路徑、
// 全新 windowsKeychain 實例），對應 §4.4.2「Refresh Token 90 天效期須真跨重啟持久保存」。
func TestWindowsKeychainPersistsAcrossInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credentials.dat")
	k1 := newWindowsKeychain(path)
	if err := k1.Set("eco-agent.id_token", "id-123"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	k2 := newWindowsKeychain(path)
	got, err := k2.Get("eco-agent.id_token")
	if err != nil {
		t.Fatalf("Get (new instance): %v", err)
	}
	if got != "id-123" {
		t.Fatalf("Get (new instance) = %q, want %q", got, "id-123")
	}
}

// TestWindowsKeychainMultipleKeysIndependent 驗證多筆鍵各自獨立存取，互不干擾。
func TestWindowsKeychainMultipleKeysIndependent(t *testing.T) {
	k := newTestWindowsKeychain(t)
	if err := k.Set("a", "va"); err != nil {
		t.Fatalf("Set a: %v", err)
	}
	if err := k.Set("b", "vb"); err != nil {
		t.Fatalf("Set b: %v", err)
	}
	if err := k.Delete("a"); err != nil {
		t.Fatalf("Delete a: %v", err)
	}
	if _, err := k.Get("a"); !errors.Is(err, ErrKeychainNotFound) {
		t.Fatalf("Get a after delete = %v, want ErrKeychainNotFound", err)
	}
	if got, err := k.Get("b"); err != nil || got != "vb" {
		t.Fatalf("Get b = %q, %v; want %q, nil", got, err, "vb")
	}
}
