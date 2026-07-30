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

// 傳輸協定（v20 §4.4 [D13]）：三條路徑**一律走 HTTPS 進後端 REST API**，Eco-Agent 不再連線
// MQTT Broker。原「A/B 走 MQTT、C 走 HTTPS」的協定分流於 v0.20 廢止，故此處無協定分流結構。
//
// 理由（[D13]）：
//   - 4.4.3 的「後端回 200 才清佇列」在 MQTT 上不成立——QoS 1 的 PUBACK 由 Broker 而非後端
//     發出，Agent 清佇列時資料可能仍在後端記憶體未落地。走 HTTPS 後 200 由後端於 commit
//     之後發出，「收到 200」與「已落地」等價。
//   - 4.4.2 撤銷（401/403 自清憑證）與 5.2 配置版本號夾帶皆需 HTTP 回應語意，續走 MQTT 反須
//     另開一條 HTTPS，協定數量不減反增。
//   - Agent 為「一路徑一天 1 筆」的極低頻上傳、跑在桌機而非受限硬體，MQTT 的輕量優勢用不上。
//
// 專案層級的混合協定架構仍成立（廢棄物樹莓派續走 MQTT），只是分流判準由「是不是 IoT 裝置」
// 修正為「需不需要後端的回應」——Eco-Agent 需要回程資訊，故全走 HTTPS。

// 上傳端點設定（§7）。
const (
	// EnvUploadURL 覆寫 mock 上傳端點的環境變數。
	EnvUploadURL = "ECO_AGENT_UPLOAD_URL"
	// DefaultUploadURL 為預設 mock 端點（本機 mock server，見 cmd/mock-ingest）。
	DefaultUploadURL = "http://localhost:8080/mock/ingest"
)

// Batch 是一批待送資料（已去識別化：僅帶 ID Token 與量值 payload）。三路徑共用同一批次。
type Batch struct {
	IDToken     string
	AccessToken string
	Events      []queue.Event
}

// Response 是上傳回應（現僅需狀態碼供 at-least-once 與撤銷夾帶檢查）。
type Response struct {
	StatusCode int
}

// Sender 送出一批資料（HTTPS POST）；現階段打 mock 端點（§7）。
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
	IDToken string      `json:"id_token"`
	Events  []wireEvent `json:"events"`
}

// MockHTTPSender 打 mock HTTP 端點（§7）。傳輸形狀與真實 HTTPS 上傳一致（POST JSON、
// Authorization: Bearer、以狀態碼表達結果），僅端點為本機 mock。
//
// TODO(backend): 改打後端 REST ingest 端點 POST {base_url}/digital-usage/batch（[D13]）；
// 屆時只換此 Sender 的 url 與 TLS 設定，Uploader 不動。
type MockHTTPSender struct {
	url    string
	client *http.Client
}

// NewMockHTTPSender 建立指向 url 的 mock HTTP 送出器。
func NewMockHTTPSender(url string) *MockHTTPSender {
	return &MockHTTPSender{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// Send 實作 Sender：POST 去識別化 JSON 至 mock 端點，Authorization 夾帶 Access Token。
func (s *MockHTTPSender) Send(ctx context.Context, b Batch) (Response, error) {
	body := wireBody{IDToken: b.IDToken}
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
		return Response{}, fmt.Errorf("uploader: send: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return Response{StatusCode: resp.StatusCode}, nil
}
