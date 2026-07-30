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
	IDToken string
	Events  []ReceivedEvent
}

// ReceivedEvent 是批次中的一筆事件。共同欄位單獨列出，其餘量值留在 Fields 內
// （Fields 為完整的扁平記錄，含共同欄位本身）。
type ReceivedEvent struct {
	EventID     string
	PathType    string
	UsageDate   string
	CollectedAt string         // RFC3339Nano（UTC），供驗證 [D14] 的亂序勝出時間戳確實上送
	Fields      map[string]any // 收到的原始扁平記錄
}

// EventIDs 取出本批所有事件 ID。
func (b ReceivedBatch) EventIDs() []string {
	ids := make([]string, len(b.Events))
	for i, e := range b.Events {
		ids[i] = e.EventID
	}
	return ids
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
		rb := ReceivedBatch{IDToken: body.IDToken}
		for _, e := range body.Events {
			str := func(k string) string { s, _ := e[k].(string); return s }
			rb.Events = append(rb.Events, ReceivedEvent{
				EventID:     str(fieldEventID),
				PathType:    str(fieldPathType),
				UsageDate:   str(fieldUsageDate),
				CollectedAt: str(fieldCollectedAt),
				Fields:      e,
			})
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
		n += len(b.Events)
	}
	return n
}
