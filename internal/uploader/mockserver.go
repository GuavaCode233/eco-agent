package uploader

import (
	"encoding/json"
	"net/http"
	"sync"
)

// MockIngestServer 是極簡的 mock 後端 ingest 端點（§7）。
//
// 預設回 200；可用 SetStatus 切換為 401/403 以測試撤銷自清路徑。記錄收到的批次供斷言。
// 供單元測試（httptest）與 cmd/mock-ingest 獨立執行共用。
type MockIngestServer struct {
	mu       sync.Mutex
	status   int
	received []ReceivedBatch
}

// ReceivedBatch 是 mock 端點收到的一批（去識別化後）資料。
type ReceivedBatch struct {
	IDToken  string
	Protocol string
	EventIDs []string
}

// NewMockIngestServer 建立回應指定狀態碼的 mock ingest server。status <= 0 視為 200。
func NewMockIngestServer(status int) *MockIngestServer {
	if status <= 0 {
		status = http.StatusOK
	}
	return &MockIngestServer{status: status}
}

// SetStatus 切換回應狀態碼（如 401/403 測撤銷）。
func (m *MockIngestServer) SetStatus(status int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.status = status
}

// Handler 回傳掛載 ingest 端點的 http.Handler（路徑對齊 DefaultUploadURL）。
func (m *MockIngestServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/mock/ingest", m.handleIngest)
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return mux
}

func (m *MockIngestServer) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var body wireBody
	if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
		rb := ReceivedBatch{IDToken: body.IDToken, Protocol: body.Protocol}
		for _, e := range body.Events {
			rb.EventIDs = append(rb.EventIDs, e.EventID)
		}
		m.mu.Lock()
		m.received = append(m.received, rb)
		status := m.status
		m.mu.Unlock()
		w.WriteHeader(status)
		return
	}
	m.mu.Lock()
	status := m.status
	m.mu.Unlock()
	w.WriteHeader(status)
}

// Received 回傳目前收到的所有批次（複本）。
func (m *MockIngestServer) Received() []ReceivedBatch {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ReceivedBatch, len(m.received))
	copy(out, m.received)
	return out
}

// EventCount 回傳目前收到的事件總數。
func (m *MockIngestServer) EventCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, b := range m.received {
		n += len(b.EventIDs)
	}
	return n
}
