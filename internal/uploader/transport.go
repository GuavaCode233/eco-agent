package uploader

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"eco-agent/internal/queue"
)

// Protocol 標示一批資料的傳輸協定（§1 混合協定：電腦/印表機走 MQTT、雲端走 HTTPS）。
type Protocol string

const (
	// ProtocolMQTT：路徑 A（電腦）、路徑 B（印表機）。
	ProtocolMQTT Protocol = "mqtt"
	// ProtocolHTTPS：路徑 C（雲端儲存），直進後端 REST，不經 MQTT Broker。
	ProtocolHTTPS Protocol = "https"
)

// protocolOrder 為分組送出的固定順序（決定性，便於測試與 log）。
var protocolOrder = []Protocol{ProtocolMQTT, ProtocolHTTPS}

// ProtocolFor 依路徑類型決定傳輸協定（協定分流，§1）。
func ProtocolFor(p queue.PathType) Protocol {
	switch p {
	case queue.PathDrive:
		return ProtocolHTTPS
	default: // PathComputer、PathPrinter
		return ProtocolMQTT
	}
}

// 上傳端點設定（§7）。
const (
	// EnvUploadURL 覆寫 mock 上傳端點的環境變數。
	EnvUploadURL = "ECO_AGENT_UPLOAD_URL"
	// DefaultUploadURL 為預設 mock 端點（本機 mock server，見 cmd/mock-ingest）。
	DefaultUploadURL = "http://localhost:8080/mock/ingest"
)

// Batch 是分流後、單一協定的一批待送資料（已去識別化：僅帶 ID Token 與量值 payload）。
type Batch struct {
	IDToken     string
	AccessToken string
	Protocol    Protocol
	Events      []queue.Event
}

// Response 是上傳回應（現僅需狀態碼供 at-least-once 與撤銷夾帶檢查）。
type Response struct {
	StatusCode int
}

// Sender 送出一批資料。依協定有不同實作；現階段皆為 mock（§7）。
type Sender interface {
	Send(ctx context.Context, b Batch) (Response, error)
}

// wire 格式（去識別化：不含姓名/Email，只有 id_token 與量值 payload）。
type wireEvent struct {
	EventID  string         `json:"event_id"`
	PathType string         `json:"path_type"`
	Payload  map[string]any `json:"payload"`
}

type wireBody struct {
	IDToken  string      `json:"id_token"`
	Protocol string      `json:"protocol"`
	Events   []wireEvent `json:"events"`
}

// MockHTTPSender 打 mock HTTP 端點（§7）。現同時作為 MQTT 與 HTTPS 兩協定的 mock 送出，
// 以保留協定分流的程式結構、便於日後替換為真實傳輸。
//
// TODO(backend): 真實 MQTT 送出改用 paho.mqtt.golang 發佈至 digital/agent/{id_token}；
// 真實 HTTPS 送出改打後端 REST ingest 端點。屆時依協定各自替換此 Sender，Uploader 不動。
type MockHTTPSender struct {
	protocol Protocol
	url      string
	client   *http.Client
}

// NewMockHTTPSender 建立指向 url 的 mock HTTP 送出器。
func NewMockHTTPSender(p Protocol, url string) *MockHTTPSender {
	return &MockHTTPSender{
		protocol: p,
		url:      url,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

// Send 實作 Sender：POST 去識別化 JSON 至 mock 端點，Authorization 夾帶 Access Token。
func (s *MockHTTPSender) Send(ctx context.Context, b Batch) (Response, error) {
	body := wireBody{IDToken: b.IDToken, Protocol: string(b.Protocol)}
	for _, e := range b.Events {
		body.Events = append(body.Events, wireEvent{
			EventID:  e.ID,
			PathType: string(e.PathType),
			Payload:  e.Payload,
		})
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return Response{}, fmt.Errorf("uploader: marshal batch: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(buf))
	if err != nil {
		return Response{}, fmt.Errorf("uploader: build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	// MOCK: token 為 mock 常數（§7）；真實流程帶後端簽發的短期 Access Token。
	req.Header.Set("Authorization", "Bearer "+b.AccessToken)

	resp, err := s.client.Do(req)
	if err != nil {
		return Response{}, fmt.Errorf("uploader: send (%s): %w", s.protocol, err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return Response{StatusCode: resp.StatusCode}, nil
}
