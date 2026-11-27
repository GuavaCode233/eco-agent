package web

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

	"eco-agent/internal/agent"
	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/uploader"
)

// TestIntegrationPageDrivesBinding 以真實 agent／enroll／網頁對假後端驗證 U2 流程：
// 頁面顯示 QR → 關頁（關閉訊號）→ 進入閒置、後端不再收到索取綁定碼請求、觸發提醒回呼
// → 重新開頁 → 恢復換碼 → 掃碼 → 頁面顯示已綁定。
func TestIntegrationPageDrivesBinding(t *testing.T) {
	var codeRequests, codeSeq int32
	var scanned atomic.Bool
	mux := http.NewServeMux()
	writeJSON := func(w http.ResponseWriter, v any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST "+config.PathBindingCode, func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&codeRequests, 1)
		n := atomic.AddInt32(&codeSeq, 1)
		writeJSON(w, map[string]any{
			"code": "C" + string(rune('0'+n)), "device_secret": "s",
			"expires_at": time.Now().Add(time.Minute).UTC().Format(time.RFC3339),
		})
	})
	mux.HandleFunc("GET "+config.PathBindingCode+"/{code}/token", func(w http.ResponseWriter, r *http.Request) {
		if !scanned.Load() {
			writeJSON(w, map[string]any{"status": "pending"})
			return
		}
		writeJSON(w, map[string]any{
			"status": "consumed", "access_token": "at", "refresh_token": "rt",
			"token_type": "bearer", "expires_in": 3600, "id_token": "idt",
		})
	})
	backend := httptest.NewServer(mux)
	defer backend.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q, err := queue.Open(ctx, filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer q.Close()

	quiet := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := config.LoadProfile(config.ProfileTesting)
	cfg.BaseURL = backend.URL
	var ag *agent.Agent
	enr := enroll.New(platform.NewMemoryKeychain(), q,
		enroll.WithBaseURL(backend.URL),
		enroll.WithBindingCodeTTL(time.Minute),
		enroll.WithBindingCodePollInterval(5*time.Millisecond),
		enroll.WithLogger(quiet),
		enroll.WithBindingCodeHandler(func(uri string, exp time.Time) { ag.OnBindingCode(uri, exp) }),
	)
	noSensors := func(context.Context, *sync.WaitGroup) map[queue.PathType]bool { return nil }
	ag = agent.New(enr, q,
		func() agent.Uploader { return uploader.New(q, enr, cfg, uploader.WithLogger(quiet)) },
		noSensors, agent.WithLogger(quiet))

	var reminders int32
	srv, err := New(ag, WithLogger(quiet),
		WithHeartbeatTimeout(time.Hour), WithCloseGrace(30*time.Millisecond), WithWatchInterval(5*time.Millisecond),
		WithOnPageClosed(func() { atomic.AddInt32(&reminders, 1) }))
	if err != nil {
		t.Fatal(err)
	}
	agDone := make(chan struct{})
	go func() { _ = ag.Run(ctx); close(agDone) }()
	srvDone := make(chan struct{})
	go func() { _ = srv.Serve(ctx); close(srvDone) }()
	defer func() { cancel(); <-agDone; <-srvDone }()

	status := func() statusJSON {
		_, body := get(t, srv.URL()+"status.json") // 每次讀取同時是一次心跳
		var s statusJSON
		if err := json.Unmarshal(body, &s); err != nil {
			t.Fatal(err)
		}
		return s
	}

	// 頁面開著：顯示 QR。
	eventually(t, "QR shown", func() bool { s := status(); return s.State == "binding" && s.BindingCode != nil })
	if res, _ := get(t, srv.URL()+"qr.png"); res.StatusCode != http.StatusOK {
		t.Fatalf("qr.png = %d", res.StatusCode)
	}

	// 關頁（頁面送出關閉訊號）→ 閒置、提醒回呼、不再打後端。
	postClosed(t, srv)
	eventually(t, "idle after page closed", func() bool { return ag.State() == agent.StateIdle })
	eventually(t, "reminder callback", func() bool { return atomic.LoadInt32(&reminders) == 1 })
	before := atomic.LoadInt32(&codeRequests)
	time.Sleep(100 * time.Millisecond)
	if after := atomic.LoadInt32(&codeRequests); after != before {
		t.Fatalf("binding-code requests while idle: %d → %d, want none", before, after)
	}

	// 重新開頁 → 恢復換碼。
	status()
	eventually(t, "new binding code after reopen", func() bool { return atomic.LoadInt32(&codeRequests) > before })

	// 掃碼 → 頁面顯示已綁定。
	scanned.Store(true)
	eventually(t, "bound shown on page", func() bool { return status().State == "bound" })
}
