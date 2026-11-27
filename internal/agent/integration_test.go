package agent

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/uploader"
)

// fakeBackend 模擬綁定①③與批次上傳端點。scanned 為 true 前③回 pending。
type fakeBackend struct {
	codes       int32
	scanned     atomic.Bool
	ingestCode  atomic.Int32
	mu          sync.Mutex
	gotIDTokens []string
}

func (b *fakeBackend) handler(t *testing.T) http.Handler {
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST "+config.PathBindingCode, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&b.codes, 1)
		writeJSON(w, map[string]any{
			"code": "C1", "device_secret": "s",
			"expires_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("GET "+config.PathBindingCode+"/{code}/token", func(w http.ResponseWriter, r *http.Request) {
		if !b.scanned.Load() {
			writeJSON(w, map[string]any{"status": "pending"})
			return
		}
		writeJSON(w, map[string]any{
			"status": "consumed", "access_token": "at", "refresh_token": "rt",
			"token_type": "bearer", "expires_in": 3600, "id_token": "real-id-token",
		})
	})
	mux.HandleFunc("POST "+config.PathDigitalUsageBatch, func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			IDToken string           `json:"id_token"`
			Events  []map[string]any `json:"events"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		b.mu.Lock()
		b.gotIDTokens = append(b.gotIDTokens, body.IDToken)
		b.mu.Unlock()
		code := int(b.ingestCode.Load())
		w.WriteHeader(code)
		if code == http.StatusOK {
			_ = json.NewEncoder(w).Encode(map[string]int{"accepted": len(body.Events)})
		}
	})
	return mux
}

// TestIntegrationBindUploadRevokeRebind 以真實 enroll／uploader 對假後端跑完整生命週期：
// 綁定中（QR 出現）→ 掃碼 → 已綁定 → 上傳 → 後端回 403 → 已撤銷 → Rebind → 再次綁定中。
func TestIntegrationBindUploadRevokeRebind(t *testing.T) {
	be := &fakeBackend{}
	be.ingestCode.Store(http.StatusOK)
	srv := httptest.NewServer(be.handler(t))
	defer srv.Close()

	ctx := context.Background()
	q, err := queue.Open(ctx, filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.LoadProfile(config.ProfileTesting)
	cfg.BaseURL = srv.URL

	var ag *Agent
	enr := enroll.New(platform.NewMemoryKeychain(), q,
		enroll.WithBaseURL(srv.URL),
		enroll.WithBindingCodeTTL(time.Minute),
		enroll.WithBindingCodePollInterval(5*time.Millisecond),
		enroll.WithLogger(quiet),
		enroll.WithBindingCodeHandler(func(uri string, exp time.Time) { ag.OnBindingCode(uri, exp) }),
	)
	newUp := func() Uploader { return uploader.New(q, enr, cfg, uploader.WithLogger(quiet)) }
	sensors := &fakeSensors{}
	ag = New(enr, q, newUp, sensors.start, WithLogger(quiet), WithSessionCheckInterval(10*time.Millisecond))

	runCtx, cancel := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- ag.Run(runCtx) }()
	defer func() {
		cancel()
		<-done
	}()

	eventually(t, "binding URI shown", func() bool {
		return ag.Status(ctx).BindingURI == "ecosensing://bind?code=C1"
	})
	if s := ag.State(); s != StateBinding {
		t.Fatalf("state = %s, want binding", s)
	}

	be.scanned.Store(true)
	eventually(t, "bound", func() bool { return ag.State() == StateBound })
	if ag.Status(ctx).BindingURI != "" {
		t.Fatal("BindingURI should be cleared once bound")
	}

	if err := q.Enqueue(ctx, queue.Event{
		ID: queue.EventID("real-id-token", "2026-11-27", queue.PathComputer), PathType: queue.PathComputer,
		UsageDate: "2026-11-27", Payload: map[string]any{"pc_active_hours": 1.0}, CollectedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := ag.FlushNow(ctx); err != nil {
		t.Fatalf("FlushNow: %v", err)
	}
	st := ag.Status(ctx)
	if st.QueueCount != 0 || st.LastUpload == nil || !st.LastUpload.OK() {
		t.Fatalf("status after upload = %+v (last %+v), want empty queue and OK upload", st, st.LastUpload)
	}
	be.mu.Lock()
	if len(be.gotIDTokens) != 1 || be.gotIDTokens[0] != "real-id-token" {
		t.Fatalf("backend id_tokens = %v, want [real-id-token]", be.gotIDTokens)
	}
	be.mu.Unlock()

	// 後端撤銷：下一次上傳回 403 → 已撤銷，程式不結束。
	be.ingestCode.Store(http.StatusForbidden)
	if err := q.Enqueue(ctx, queue.Event{
		ID: queue.EventID("real-id-token", "2026-11-28", queue.PathComputer), PathType: queue.PathComputer,
		UsageDate: "2026-11-28", Payload: map[string]any{"pc_active_hours": 2.0}, CollectedAt: time.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	_ = ag.FlushNow(ctx)
	eventually(t, "revoked", func() bool { return ag.State() == StateRevoked })

	// 重新綁定：應再次索取綁定碼。
	be.scanned.Store(false)
	codesBefore := atomic.LoadInt32(&be.codes)
	ag.Rebind()
	eventually(t, "new binding code requested", func() bool { return atomic.LoadInt32(&be.codes) > codesBefore })
	if s := ag.State(); s != StateBinding {
		t.Fatalf("state after Rebind = %s, want binding", s)
	}
}
