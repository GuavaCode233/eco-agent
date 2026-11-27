package web

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eco-agent/internal/agent"
	"eco-agent/internal/queue"
	"eco-agent/internal/uploader"
)

type fakeAgent struct {
	mu      sync.Mutex
	st      agent.Status
	pauseOK bool
	pauses  int32
	starts  int32
}

func (f *fakeAgent) Status(context.Context) agent.Status {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st
}

func (f *fakeAgent) PauseBinding() bool {
	atomic.AddInt32(&f.pauses, 1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.pauseOK
}

func (f *fakeAgent) StartBinding() { atomic.AddInt32(&f.starts, 1) }

func (f *fakeAgent) set(st agent.Status) {
	f.mu.Lock()
	f.st = st
	f.mu.Unlock()
}

func startServer(t *testing.T, ag Agent, opts ...Option) *Server {
	t.Helper()
	base := []Option{WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil)))}
	s, err := New(ag, append(base, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = s.Serve(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })
	return s
}

func get(t *testing.T, url string) (*http.Response, []byte) {
	t.Helper()
	res, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return res, body
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

const testURI = "ecosensing://bind?code=ABC123"

func TestStatusJSONBound(t *testing.T) {
	at := time.Date(2026, 11, 27, 10, 32, 0, 0, time.UTC)
	ag := &fakeAgent{st: agent.Status{
		State:      agent.StateBound,
		QueueCount: 12,
		LastUpload: &uploader.Result{At: at, Reason: uploader.ReasonThreshold, Sent: 3, Uploaded: 3},
		Paths:      map[queue.PathType]bool{queue.PathPrinter: false, queue.PathComputer: true, queue.PathDrive: true},
	}}
	s := startServer(t, ag)

	res, body := get(t, s.URL()+"status.json")
	if res.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", res.StatusCode)
	}
	var got statusJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "bound" || got.QueueCount != 12 || got.BindingCode != nil {
		t.Fatalf("status = %+v", got)
	}
	if got.LastUpload == nil || !got.LastUpload.OK || got.LastUpload.Reason != "threshold" || !got.LastUpload.At.Equal(at) {
		t.Fatalf("last_upload = %+v", got.LastUpload)
	}
	wantOrder := []string{"computer", "drive", "printer"}
	for i, p := range got.Paths {
		if p.Path != wantOrder[i] {
			t.Fatalf("paths order = %+v, want computer/drive/printer", got.Paths)
		}
	}
	if got.Paths[2].Enabled {
		t.Fatal("printer should be reported disabled")
	}
}

// 綁定中：status.json 只給綁定碼的指紋與到期時間，不輸出綁定碼本身；qr.png 為 PNG。
func TestBindingQRAndNoSecretsInStatus(t *testing.T) {
	exp := time.Now().Add(5 * time.Minute).UTC().Truncate(time.Second)
	ag := &fakeAgent{st: agent.Status{State: agent.StateBinding, BindingURI: testURI, BindingExpiresAt: exp}}
	s := startServer(t, ag)

	_, body := get(t, s.URL()+"status.json")
	if bytes.Contains(body, []byte("ABC123")) {
		t.Fatalf("status.json leaks binding code: %s", body)
	}
	for _, bad := range []string{"secret", "token", "refresh", "access"} {
		if bytes.Contains(bytes.ToLower(body), []byte(bad)) {
			t.Fatalf("status.json contains %q: %s", bad, body)
		}
	}
	var got statusJSON
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.BindingCode == nil || got.BindingCode.Version != qrVersion(testURI) || !got.BindingCode.ExpiresAt.Equal(exp) {
		t.Fatalf("binding = %+v", got.BindingCode)
	}

	res, png := get(t, s.URL()+"qr.png")
	if res.StatusCode != http.StatusOK || res.Header.Get("Content-Type") != "image/png" ||
		!bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatalf("qr.png: status %d, type %q, %d bytes", res.StatusCode, res.Header.Get("Content-Type"), len(png))
	}

	// 綁定碼更換 → 指紋改變（頁面據此重新載入 QR）。
	ag.set(agent.Status{State: agent.StateBinding, BindingURI: testURI + "X", BindingExpiresAt: exp})
	_, body = get(t, s.URL()+"status.json")
	_ = json.Unmarshal(body, &got)
	if got.BindingCode.Version == qrVersion(testURI) {
		t.Fatal("QR version did not change with a new binding code")
	}

	// 非綁定中 → qr.png 404。
	ag.set(agent.Status{State: agent.StateBound})
	if res, _ := get(t, s.URL()+"qr.png"); res.StatusCode != http.StatusNotFound {
		t.Fatalf("qr.png when bound = %d, want 404", res.StatusCode)
	}
}

func TestWrongTokenAndHostRejected(t *testing.T) {
	s := startServer(t, &fakeAgent{st: agent.Status{State: agent.StateBound}})
	base := strings.TrimSuffix(s.URL(), s.token+"/")

	// 註：/<正確token>（無結尾斜線）由 ServeMux 轉址到 /<token>/，屬正常行為，不列入。
	for _, p := range []string{"", "wrong", "wrong/", "wrong/status.json", "wrong/qr.png", s.token + "x/status.json"} {
		if res, _ := get(t, base+p); res.StatusCode != http.StatusNotFound {
			t.Errorf("GET /%s = %d, want 404", p, res.StatusCode)
		}
	}

	// DNS rebinding：Host 不是 127.0.0.1:<port> 一律 404。
	req, _ := http.NewRequest(http.MethodGet, s.URL()+"status.json", nil)
	req.Host = "evil.example:80"
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign Host = %d, want 404", res.StatusCode)
	}
}

func TestSecurityHeaders(t *testing.T) {
	s := startServer(t, &fakeAgent{st: agent.Status{State: agent.StateBound}})
	for _, p := range []string{"", "app.js", "status.json"} {
		res, _ := get(t, s.URL()+p)
		if res.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d", p, res.StatusCode)
		}
		h := res.Header
		if h.Get("Cache-Control") != "no-store" || h.Get("Referrer-Policy") != "no-referrer" ||
			!strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") {
			t.Fatalf("GET %s headers = %v", p, h)
		}
	}
}

// 心跳逾時 → PauseBinding + onPageClosed；重新開頁 → StartBinding。
func TestHeartbeatTimeoutPausesAndReopenResumes(t *testing.T) {
	ag := &fakeAgent{st: agent.Status{State: agent.StateBinding}, pauseOK: true}
	var closedCalls int32
	s := startServer(t, ag,
		WithHeartbeatTimeout(40*time.Millisecond),
		WithWatchInterval(5*time.Millisecond),
		WithOnPageClosed(func() { atomic.AddInt32(&closedCalls, 1) }),
	)

	// 啟動即視為頁面即將開啟；沒有任何心跳 → 逾時後暫停（瀏覽器沒開成也會進閒置）。
	eventually(t, "pause after timeout", func() bool { return atomic.LoadInt32(&ag.pauses) == 1 })
	eventually(t, "onPageClosed", func() bool { return atomic.LoadInt32(&closedCalls) == 1 })
	time.Sleep(60 * time.Millisecond)
	if n := atomic.LoadInt32(&ag.pauses); n != 1 {
		t.Fatalf("pauses while closed = %d, want 1 (no repeat)", n)
	}

	get(t, s.URL()+"status.json")
	if n := atomic.LoadInt32(&ag.starts); n != 1 {
		t.Fatalf("StartBinding calls after reopen = %d, want 1", n)
	}
	// 持續心跳期間不再 StartBinding、也不暫停。
	for i := 0; i < 5; i++ {
		get(t, s.URL()+"status.json")
		time.Sleep(10 * time.Millisecond)
	}
	if atomic.LoadInt32(&ag.starts) != 1 || atomic.LoadInt32(&ag.pauses) != 1 {
		t.Fatalf("starts=%d pauses=%d during heartbeats, want 1/1", ag.starts, ag.pauses)
	}
	eventually(t, "pause after second close", func() bool { return atomic.LoadInt32(&ag.pauses) == 2 })
	eventually(t, "second onPageClosed", func() bool { return atomic.LoadInt32(&closedCalls) == 2 })
}

// 已綁定時關頁：PauseBinding 為 no-op（回 false）→ 不觸發提醒回呼。
func TestPageClosedWhileBoundNoReminder(t *testing.T) {
	ag := &fakeAgent{st: agent.Status{State: agent.StateBound}, pauseOK: false}
	var closedCalls int32
	startServer(t, ag,
		WithHeartbeatTimeout(20*time.Millisecond),
		WithWatchInterval(5*time.Millisecond),
		WithOnPageClosed(func() { atomic.AddInt32(&closedCalls, 1) }),
	)
	eventually(t, "pause attempted", func() bool { return atomic.LoadInt32(&ag.pauses) == 1 })
	time.Sleep(20 * time.Millisecond)
	if n := atomic.LoadInt32(&closedCalls); n != 0 {
		t.Fatalf("onPageClosed calls while bound = %d, want 0", n)
	}
}
