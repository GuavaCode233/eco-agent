package enroll

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
)

func newTestEnroller(t *testing.T) (*Enroller, *platform.MemoryKeychain) {
	t.Helper()
	kc := platform.NewMemoryKeychain()
	q, err := queue.Open(context.Background(), filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return New(kc, q), kc
}

func TestNotBoundBeforeEnroll(t *testing.T) {
	e, _ := newTestEnroller(t)
	if bound, err := e.IsBound(); err != nil || bound {
		t.Fatalf("IsBound = %v, %v; want false", bound, err)
	}
	if _, err := e.IDToken(); !errors.Is(err, ErrNotBound) {
		t.Fatalf("IDToken err = %v; want ErrNotBound", err)
	}
	if _, err := e.AccessToken(context.Background()); !errors.Is(err, ErrNotBound) {
		t.Fatalf("AccessToken err = %v; want ErrNotBound", err)
	}
}

func TestEnsureBoundThenTokens(t *testing.T) {
	ctx := context.Background()
	e, kc := newTestEnroller(t)

	if err := e.EnsureBound(ctx); err != nil {
		t.Fatalf("EnsureBound: %v", err)
	}
	if bound, _ := e.IsBound(); !bound {
		t.Fatal("IsBound = false after EnsureBound")
	}

	id, err := e.IDToken()
	if err != nil || id != mockIDToken {
		t.Fatalf("IDToken = %q, %v; want %q", id, err, mockIDToken)
	}
	at, err := e.AccessToken(ctx)
	if err != nil || at != mockAccessToken {
		t.Fatalf("AccessToken = %q, %v; want %q", at, err, mockAccessToken)
	}
	rt, err := e.RefreshToken()
	if err != nil || rt != mockRefreshToken {
		t.Fatalf("RefreshToken = %q, %v; want %q", rt, err, mockRefreshToken)
	}

	// Refresh Token 存於金鑰庫抽象（不寫純文字檔，§1）。
	if v, err := kc.Get(keyRefreshToken); err != nil || v != mockRefreshToken {
		t.Fatalf("keychain refresh token = %q, %v; want %q", v, err, mockRefreshToken)
	}
	// EnsureBound 冪等：已綁定再呼叫不報錯。
	if err := e.EnsureBound(ctx); err != nil {
		t.Fatalf("EnsureBound (2nd) : %v", err)
	}
}

func TestAccessTokenRefreshOnExpiry(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEnroller(t)

	// 注入可控時鐘。
	base := time.Date(2026, 7, 16, 12, 0, 0, 0, time.UTC)
	current := base
	e.now = func() time.Time { return current }

	if err := e.EnsureBound(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := e.AccessToken(ctx); err != nil {
		t.Fatal(err)
	}
	expiry := e.accessTokenExpiry
	if !expiry.Equal(base.Add(mockAccessTokenTTL)) {
		t.Fatalf("expiry = %v, want %v", expiry, base.Add(mockAccessTokenTTL))
	}

	// 未過期：不重新換發（expiry 不變）。
	current = base.Add(mockAccessTokenTTL / 2)
	if _, err := e.AccessToken(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.accessTokenExpiry.Equal(expiry) {
		t.Fatalf("expiry changed before expiry: %v", e.accessTokenExpiry)
	}

	// 過期後：自動換發，expiry 前移。
	current = base.Add(mockAccessTokenTTL + time.Minute)
	if _, err := e.AccessToken(ctx); err != nil {
		t.Fatal(err)
	}
	if !e.accessTokenExpiry.After(expiry) {
		t.Fatalf("expiry not advanced after expiry: %v", e.accessTokenExpiry)
	}
}

func TestRevocationSelfClear(t *testing.T) {
	ctx := context.Background()
	e, kc := newTestEnroller(t)
	if err := e.EnsureBound(ctx); err != nil {
		t.Fatal(err)
	}

	// 模擬上傳收到 401/403 → 撤銷自清。
	if err := e.ClearCredentials(); err != nil {
		t.Fatalf("ClearCredentials: %v", err)
	}

	// 金鑰庫的 Refresh Token 已被清除（§4.4.2 撤銷自清含金鑰庫）。
	if _, err := kc.Get(keyRefreshToken); !errors.Is(err, platform.ErrKeychainNotFound) {
		t.Fatalf("refresh token still present after revoke: %v", err)
	}
	// 撤銷後所有憑證存取回 ErrRevoked（終止態，停止上傳）。
	if _, err := e.IDToken(); !errors.Is(err, ErrRevoked) {
		t.Fatalf("IDToken after revoke = %v; want ErrRevoked", err)
	}
	if _, err := e.AccessToken(ctx); !errors.Is(err, ErrRevoked) {
		t.Fatalf("AccessToken after revoke = %v; want ErrRevoked", err)
	}
	// 撤銷後不自動重綁。
	if err := e.EnsureBound(ctx); !errors.Is(err, ErrRevoked) {
		t.Fatalf("EnsureBound after revoke = %v; want ErrRevoked", err)
	}
}

func TestUnbindAllowsRebind(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEnroller(t)
	if err := e.EnsureBound(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Unbind(ctx); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if bound, _ := e.IsBound(); bound {
		t.Fatal("still bound after Unbind")
	}
	// Unbind 非終止態：可再次綁定（換機情境）。
	if err := e.EnsureBound(ctx); err != nil {
		t.Fatalf("re-EnsureBound after Unbind: %v", err)
	}
	if bound, _ := e.IsBound(); !bound {
		t.Fatal("not bound after re-EnsureBound")
	}
}

// device_uuid（§4.4.2）：首次呼叫產生並持久化，之後恆回同一值——供索取 binding_code
// 時帶上，讓後端 upsert DEVICE 列而非每次啟動都盲插新列。
func TestDeviceUUIDStableAcrossCalls(t *testing.T) {
	ctx := context.Background()
	e, _ := newTestEnroller(t)

	id1, err := e.DeviceUUID(ctx)
	if err != nil {
		t.Fatalf("DeviceUUID: %v", err)
	}
	if id1 == "" {
		t.Fatal("DeviceUUID 回傳空字串")
	}
	id2, err := e.DeviceUUID(ctx)
	if err != nil {
		t.Fatalf("DeviceUUID (2nd): %v", err)
	}
	if id2 != id1 {
		t.Fatalf("DeviceUUID 不穩定：第一次 %q，第二次 %q", id1, id2)
	}
}

// device_uuid 為本機持久化狀態（與佇列同檔），不落金鑰庫——非機密，換一個新的 Enroller
// 實例（模擬重啟）指到同一份佇列仍應讀回同一值；指到不同佇列則各自獨立產生。
func TestDeviceUUIDPersistsAcrossRestartSameQueue(t *testing.T) {
	ctx := context.Background()
	e1, kc := newTestEnroller(t)
	id1, err := e1.DeviceUUID(ctx)
	if err != nil {
		t.Fatalf("DeviceUUID: %v", err)
	}

	// 模擬重啟：同一份佇列（e1.q）、全新 Enroller 實例。
	e2 := New(kc, e1.q)
	id2, err := e2.DeviceUUID(ctx)
	if err != nil {
		t.Fatalf("DeviceUUID (restart): %v", err)
	}
	if id2 != id1 {
		t.Fatalf("重啟後 device_uuid 改變：%q → %q", id1, id2)
	}
}
