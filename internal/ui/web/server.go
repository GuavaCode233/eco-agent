// Package web 提供 Eco-Agent 的本機狀態與綁定網頁（docs/Eco-Agent_系統匣UI規劃.md U2）。
//
// 員工以預設瀏覽器開啟此頁：綁定中顯示 QR（App 掃碼核銷），已綁定顯示佇列與上傳狀態，
// 已撤銷顯示提示。頁面每 2 秒拉一次 status.json，同時作為心跳：
//   - 約 15 秒未收到心跳 → 判定頁面已關閉 → agent.PauseBinding()（進入閒置、停止打後端），
//     並呼叫 WithOnPageClosed 的回呼（U4.7 綁定提醒通知接在這裡）。
//   - 頁面（重新）開啟、收到心跳 → agent.StartBinding()（從閒置恢復換碼）。
//
// 安全（U2.5）：
//   - 只監聽 127.0.0.1（隨機埠），並檢查 Host header，擋 DNS rebinding。
//   - 所有路徑都在一段隨機 token 之下（/<token>/...），本機其他程式猜不到網址；token 錯誤一律 404。
//   - 頁面只露出綁定碼（5 分鐘過期、需 App 登入才能核銷）；device_secret 與所有憑證 token
//     本就不在 agent.Status 內，此處也不輸出。
//   - 回應一律 Cache-Control: no-store；CSP 只允許同源資源；Referrer-Policy: no-referrer
//     避免網址中的 token 經 Referer 外洩。
package web

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"sync"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"eco-agent/internal/agent"
	"eco-agent/internal/queue"
)

//go:embed assets/index.html
var indexHTML []byte

//go:embed assets/app.js
var appJS []byte

// 預設心跳參數。頁面每 2 秒拉一次 status.json（assets/app.js）。
const (
	DefaultHeartbeatTimeout = 15 * time.Second
	defaultWatchInterval    = time.Second
)

// Agent 是網頁依賴的 agent 介面（*agent.Agent 滿足）。
type Agent interface {
	Status(ctx context.Context) agent.Status
	PauseBinding() bool
	StartBinding()
}

// Server 是本機狀態與綁定網頁。
type Server struct {
	ag               Agent
	log              *slog.Logger
	token            string
	heartbeatTimeout time.Duration
	watchInterval    time.Duration
	onPageClosed     func()
	now              func() time.Time

	ln   net.Listener
	srv  *http.Server
	host string // 預期的 Host header（127.0.0.1:<port>）

	mu       sync.Mutex
	pageOpen bool
	lastSeen time.Time
}

// Option 客製化 Server。
type Option func(*Server)

// WithLogger 設定日誌器。
func WithLogger(l *slog.Logger) Option { return func(s *Server) { s.log = l } }

// WithHeartbeatTimeout 覆寫判定頁面關閉的心跳逾時（測試用）。
func WithHeartbeatTimeout(d time.Duration) Option { return func(s *Server) { s.heartbeatTimeout = d } }

// WithWatchInterval 覆寫檢查心跳的間隔（測試用）。
func WithWatchInterval(d time.Duration) Option { return func(s *Server) { s.watchInterval = d } }

// WithOnPageClosed 設定「頁面關閉導致進入閒置」時的回呼（U4.7 綁定提醒通知）。只有在
// PauseBinding 真的讓 agent 從綁定中進入閒置時才呼叫；已綁定時關頁不觸發。
func WithOnPageClosed(f func()) Option { return func(s *Server) { s.onPageClosed = f } }

// New 在 127.0.0.1 的隨機埠建立網頁（尚未開始服務，見 Serve）。
func New(ag Agent, opts ...Option) (*Server, error) {
	tok, err := randomToken()
	if err != nil {
		return nil, err
	}
	s := &Server{
		ag:               ag,
		log:              slog.Default(),
		token:            tok,
		heartbeatTimeout: DefaultHeartbeatTimeout,
		watchInterval:    defaultWatchInterval,
		now:              time.Now,
	}
	for _, o := range opts {
		o(s)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, fmt.Errorf("web: listen: %w", err)
	}
	s.ln = ln
	s.host = ln.Addr().String()
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	return s, nil
}

// URL 回傳頁面網址（含 token），供系統匣／提醒通知開啟。
func (s *Server) URL() string { return "http://" + s.host + "/" + s.token + "/" }

// Serve 開始服務並執行心跳監看，直到 ctx 取消。
//
// 啟動時視為頁面「即將開啟」：以現在作為最後心跳時間。若呼叫端隨後開啟瀏覽器卻沒成功
// （或員工立刻關掉），逾時後同樣會進入閒置並觸發提醒，而不是無限換碼。
func (s *Server) Serve(ctx context.Context) error {
	s.mu.Lock()
	s.pageOpen = true
	s.lastSeen = s.now()
	s.mu.Unlock()

	go s.watch(ctx)
	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = s.srv.Shutdown(shutdownCtx)
	}()
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("web: serve: %w", err)
	}
	return nil
}

// watch 每 watchInterval 檢查心跳；逾時即判定頁面關閉。
func (s *Server) watch(ctx context.Context) {
	t := time.NewTicker(s.watchInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.checkHeartbeat()
		}
	}
}

func (s *Server) checkHeartbeat() {
	s.mu.Lock()
	closed := s.pageOpen && s.now().Sub(s.lastSeen) >= s.heartbeatTimeout
	if closed {
		s.pageOpen = false
	}
	s.mu.Unlock()
	if !closed {
		return
	}
	if s.ag.PauseBinding() {
		s.log.Info("web: binding page closed, binding paused")
		if s.onPageClosed != nil {
			s.onPageClosed()
		}
	}
}

// heartbeat 記錄一次頁面心跳；頁面先前被判定關閉則視為重新開啟並恢復綁定。
func (s *Server) heartbeat() {
	s.mu.Lock()
	reopened := !s.pageOpen
	s.pageOpen = true
	s.lastSeen = s.now()
	s.mu.Unlock()
	if reopened {
		s.log.Info("web: binding page opened")
		s.ag.StartBinding()
	}
}

// Handler 回傳網頁的 http.Handler（測試可直接以 httptest 掛載）。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{token}/{$}", s.withToken(s.handleIndex))
	mux.HandleFunc("GET /{token}/app.js", s.withToken(s.handleAppJS))
	mux.HandleFunc("GET /{token}/status.json", s.withToken(s.handleStatus))
	mux.HandleFunc("GET /{token}/qr.png", s.withToken(s.handleQR))
	return s.secure(mux)
}

// secure 套用所有回應共通的安全 header，並檢查 Host（擋 DNS rebinding）。
func (s *Server) secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Cache-Control", "no-store")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy", "default-src 'none'; script-src 'self'; img-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		if s.host != "" && r.Host != s.host {
			http.NotFound(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// withToken 以定時比較驗證路徑中的 token；錯誤一律 404（不透露端點存在）。
func (s *Server) withToken(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.PathValue("token")), []byte(s.token)) != 1 {
			http.NotFound(w, r)
			return
		}
		h(w, r)
	}
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	s.heartbeat()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleAppJS(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	_, _ = w.Write(appJS)
}

// statusJSON 是 status.json 的回應形狀（只含 UI 需要的欄位，不含任何憑證）。
type statusJSON struct {
	State             string          `json:"state"`
	BindingCode       *bindingJSON    `json:"binding,omitempty"`
	QueueCount        int             `json:"queue_count"`
	LastUpload        *lastUploadJSON `json:"last_upload,omitempty"`
	Paths             []pathJSON      `json:"paths,omitempty"`
	DiscardedOnRevoke int             `json:"discarded_on_revoke"`
}

type bindingJSON struct {
	// Version 隨綁定碼改變，供頁面替 qr.png 加上 cache-busting 參數。
	Version   string    `json:"version"`
	ExpiresAt time.Time `json:"expires_at"`
}

type lastUploadJSON struct {
	At       time.Time `json:"at"`
	Reason   string    `json:"reason"`
	Sent     int       `json:"sent"`
	Uploaded int       `json:"uploaded"`
	OK       bool      `json:"ok"`
	Revoked  bool      `json:"revoked"`
}

type pathJSON struct {
	Path    string `json:"path"`
	Enabled bool   `json:"enabled"`
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	s.heartbeat()
	st := s.ag.Status(r.Context())
	out := statusJSON{
		State:             string(st.State),
		QueueCount:        st.QueueCount,
		DiscardedOnRevoke: st.DiscardedOnRevoke,
	}
	if st.BindingURI != "" {
		out.BindingCode = &bindingJSON{Version: qrVersion(st.BindingURI), ExpiresAt: st.BindingExpiresAt}
	}
	if r := st.LastUpload; r != nil {
		out.LastUpload = &lastUploadJSON{At: r.At, Reason: string(r.Reason), Sent: r.Sent, Uploaded: r.Uploaded, OK: r.OK(), Revoked: r.Revoked}
	}
	for p, on := range st.Paths {
		out.Paths = append(out.Paths, pathJSON{Path: string(p), Enabled: on})
	}
	sort.Slice(out.Paths, func(i, j int) bool { return pathOrder(out.Paths[i].Path) < pathOrder(out.Paths[j].Path) })

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// handleQR 以目前綁定碼即時產生 QR PNG；非綁定中（無綁定碼）回 404。
func (s *Server) handleQR(w http.ResponseWriter, r *http.Request) {
	st := s.ag.Status(r.Context())
	if st.BindingURI == "" {
		http.NotFound(w, r)
		return
	}
	png, err := qrcode.Encode(st.BindingURI, qrcode.Medium, 320)
	if err != nil {
		s.log.Warn("web: encode QR failed", "err", err)
		http.Error(w, "QR encode failed", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write(png)
}

// qrVersion 回傳綁定碼的短指紋，供頁面判斷 QR 是否需要重新載入（不直接輸出綁定碼本身）。
func qrVersion(uri string) string {
	var h uint32 = 2166136261 // FNV-1a
	for i := 0; i < len(uri); i++ {
		h ^= uint32(uri[i])
		h *= 16777619
	}
	return fmt.Sprintf("%08x", h)
}

func pathOrder(p string) int {
	switch queue.PathType(p) {
	case queue.PathComputer:
		return 0
	case queue.PathDrive:
		return 1
	case queue.PathPrinter:
		return 2
	}
	return 3
}

func randomToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("web: random token: %w", err)
	}
	return hex.EncodeToString(b), nil
}
