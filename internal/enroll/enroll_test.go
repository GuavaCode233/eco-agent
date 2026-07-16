package enroll

import (
	"context"
	"errors"
	"testing"
	"time"

	"eco-agent/internal/platform"
)

func newTestEnroller() (*Enroller, *platform.MemoryKeychain) {
	kc := platform.NewMemoryKeychain()
	return New(kc), kc
}

func TestNotBoundBeforeEnroll(t *testing.T) {
	e, _ := newTestEnroller()
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
	e, kc := newTestEnroller()

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
	e, _ := newTestEnroller()

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
	e, kc := newTestEnroller()
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
	e, _ := newTestEnroller()
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
