package enroll

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"eco-agent/internal/config"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
)

// newRealFlowEnroller 建立一個指向 srv 的 Enroller，短化輪詢間隔/逾時供測試快速跑完
// （真實流程只在 WithBaseURL 有設定時啟用，見 enroll.go 頂部註解）。
func newRealFlowEnroller(t *testing.T, srv *httptest.Server, opts ...Option) (*Enroller, *platform.MemoryKeychain) {
	t.Helper()
	kc := platform.NewMemoryKeychain()
	q, err := queue.Open(context.Background(), filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { q.Close() })

	base := []Option{
		WithBaseURL(srv.URL),
		WithBindingCodeTTL(500 * time.Millisecond),
		WithBindingCodePollInterval(5 * time.Millisecond),
	}
	return New(kc, q, append(base, opts...)...), kc
}

func writeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Fatalf("encode response: %v", err)
	}
}

// TestBindRealFlowSuccessAfterPending 驗證真實綁定流程（§3.1）：索取 binding_code →
// 輪詢核銷（先 pending 數次再 consumed）→ 四個憑證正確落地（金鑰庫 + 記憶體 Access Token）。
func TestBindRealFlowSuccessAfterPending(t *testing.T) {
	ctx := context.Background()
	var pollCount int32
	const code = "ABC123"
	const deviceSecret = "shh-secret"

	mux := http.NewServeMux()
	mux.HandleFunc("POST "+config.PathBindingCode, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			DeviceUUID string `json:"device_uuid"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode binding-code request: %v", err)
		}
		if req.DeviceUUID == "" {
			t.Fatal("binding-code request missing device_uuid")
		}
		writeJSON(t, w, map[string]any{
			"code":          code,
			"device_secret": deviceSecret,
			"expires_at":    time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("GET "+config.PathBindingCode+"/{code}/token", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("code") != code {
			t.Fatalf("poll code = %q, want %q", r.PathValue("code"), code)
		}
		if got := r.Header.Get("X-Device-Secret"); got != deviceSecret {
			t.Fatalf("X-Device-Secret = %q, want %q", got, deviceSecret)
		}
		n := atomic.AddInt32(&pollCount, 1)
		if n < 3 {
			writeJSON(t, w, map[string]any{
				"status": "pending", "access_token": nil, "refresh_token": nil,
				"expires_in": nil, "id_token": nil,
			})
			return
		}
		writeJSON(t, w, map[string]any{
			"status":        "consumed",
			"access_token":  "real-access-tok",
			"refresh_token": "real-refresh-tok",
			"expires_in":    3600,
			"id_token":      "real-id-tok",
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e, kc := newRealFlowEnroller(t, srv)

	if err := e.EnsureBound(ctx); err != nil {
		t.Fatalf("EnsureBound: %v", err)
	}
	if n := atomic.LoadInt32(&pollCount); n < 3 {
		t.Fatalf("poll count = %d, want >= 3 (expected pending responses before consumed)", n)
	}

	id, err := e.IDToken()
	if err != nil || id != "real-id-tok" {
		t.Fatalf("IDToken = %q, %v; want %q", id, err, "real-id-tok")
	}
	rt, err := e.RefreshToken()
	if err != nil || rt != "real-refresh-tok" {
		t.Fatalf("RefreshToken = %q, %v; want %q", rt, err, "real-refresh-tok")
	}
	at, err := e.AccessToken(ctx)
	if err != nil || at != "real-access-tok" {
		t.Fatalf("AccessToken = %q, %v; want %q", at, err, "real-access-tok")
	}
	if v, _ := kc.Get(keyRefreshToken); v != "real-refresh-tok" {
		t.Fatalf("keychain refresh token = %q, want %q", v, "real-refresh-tok")
	}
}

// TestBindRealFlowTimesOutWhenAlwaysPending 驗證輪詢逾時（bindingCodeTTL 到期仍
// pending）會回錯誤，且不落地任何憑證（§3.1 步驟 3：逾時回錯誤讓上層重新走 Bind）。
func TestBindRealFlowTimesOutWhenAlwaysPending(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+config.PathBindingCode, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"code": "NEVER", "device_secret": "s",
			"expires_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("GET "+config.PathBindingCode+"/{code}/token", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, map[string]any{
			"status": "pending", "access_token": nil, "refresh_token": nil,
			"expires_in": nil, "id_token": nil,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e, kc := newRealFlowEnroller(t, srv, WithBindingCodeTTL(30*time.Millisecond), WithBindingCodePollInterval(5*time.Millisecond))

	if err := e.EnsureBound(ctx); err == nil {
		t.Fatal("EnsureBound = nil, want timeout error")
	}
	if bound, _ := e.IsBound(); bound {
		t.Fatal("IsBound = true after timed-out bind, want false")
	}
	if _, err := kc.Get(keyRefreshToken); !errors.Is(err, platform.ErrKeychainNotFound) {
		t.Fatalf("refresh token present after timed-out bind: %v", err)
	}
}

// TestBindRealFlowBindingCodeRequestFails 驗證端點①非 200 時 Bind 直接回錯誤、不進入輪詢。
func TestBindRealFlowBindingCodeRequestFails(t *testing.T) {
	ctx := context.Background()
	var pollHit bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+config.PathBindingCode, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	mux.HandleFunc("GET "+config.PathBindingCode+"/{code}/token", func(w http.ResponseWriter, r *http.Request) {
		pollHit = true
		writeJSON(t, w, map[string]any{"status": "pending"})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e, _ := newRealFlowEnroller(t, srv)
	if err := e.EnsureBound(ctx); err == nil {
		t.Fatal("EnsureBound = nil, want error from failed binding-code request")
	}
	if pollHit {
		t.Fatal("polling endpoint was called despite binding-code request failing")
	}
}

// TestRefreshAccessTokenHTTPSuccess 驗證端點④成功換發時，AccessToken() 回傳後端給的值
// （§3.2）。
func TestRefreshAccessTokenHTTPSuccess(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+config.PathTokenRefresh, func(w http.ResponseWriter, r *http.Request) {
		var req tokenRefreshRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode refresh request: %v", err)
		}
		if req.RefreshToken != "seed-refresh-token" {
			t.Fatalf("refresh_token = %q, want %q", req.RefreshToken, "seed-refresh-token")
		}
		writeJSON(t, w, map[string]any{
			"access_token": "fresh-access-tok", "token_type": "bearer", "expires_in": 3600,
		})
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e, kc := newRealFlowEnroller(t, srv)
	// 略過真實 Bind()：直接在金鑰庫種下既有憑證，聚焦驗證換發邏輯本身。
	if err := kc.Set(keyIDToken, "seed-id-token"); err != nil {
		t.Fatal(err)
	}
	if err := kc.Set(keyRefreshToken, "seed-refresh-token"); err != nil {
		t.Fatal(err)
	}

	at, err := e.AccessToken(ctx)
	if err != nil {
		t.Fatalf("AccessToken: %v", err)
	}
	if at != "fresh-access-tok" {
		t.Fatalf("AccessToken = %q, want %q", at, "fresh-access-tok")
	}
}

// TestRefreshAccessTokenHTTP401ClearsCredentialsNotRevoked 驗證端點④回 401 時：清本機憑證、
// 回 ErrNotBound（非 ErrRevoked，不是終止態）——讓上層下次 EnsureBound 能重新 Bind()（§3.2）。
func TestRefreshAccessTokenHTTP401ClearsCredentialsNotRevoked(t *testing.T) {
	ctx := context.Background()
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+config.PathTokenRefresh, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	e, kc := newRealFlowEnroller(t, srv)
	if err := kc.Set(keyIDToken, "seed-id-token"); err != nil {
		t.Fatal(err)
	}
	if err := kc.Set(keyRefreshToken, "seed-refresh-token"); err != nil {
		t.Fatal(err)
	}

	if _, err := e.AccessToken(ctx); !errors.Is(err, ErrNotBound) {
		t.Fatalf("AccessToken after 401 refresh = %v, want ErrNotBound", err)
	}
	if _, err := kc.Get(keyRefreshToken); !errors.Is(err, platform.ErrKeychainNotFound) {
		t.Fatalf("refresh token still present after 401 refresh: %v", err)
	}
	if _, err := kc.Get(keyIDToken); !errors.Is(err, platform.ErrKeychainNotFound) {
		t.Fatalf("id token still present after 401 refresh: %v", err)
	}
	if e.revoked {
		t.Fatal("revoked = true after refresh 401; want false (not a terminal state, see refreshAccessTokenLocked)")
	}
	if bound, _ := e.IsBound(); bound {
		t.Fatal("IsBound = true after 401 refresh cleared credentials")
	}
}
