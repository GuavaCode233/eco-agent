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

// wire 格式（去識別化：不含姓名/Email，只有 id_token 與量值）。
//
// 每筆為**扁平記錄**（v20 §4.4）：共同欄位 usage_date／path_type／collected_at 與該路徑量值
// 同層，不另包一層 payload 物件。共同欄位屬冪等唯一鍵與勝出判定所需，與量值同層可讓後端
// 無須先進入巢狀結構即可分派與去重。
//
//   - usage_date：該筆用量所屬日期（YYYY-MM-DD），唯一鍵組成。
//   - path_type：由 Agent 明送、不由後端從欄位樣態推斷（[D12]）；值域與後端
//     DIGITAL_USAGE.path_type 一致，兩端無翻譯層。
//   - collected_at：Agent 端採集時間戳（UTC，RFC3339Nano），依 [D14] 上送。路徑 A／C 送的是
//     當日累計值、後到覆蓋先到，重送的舊封包若晚於新封包抵達會把較新的值蓋回舊值；後端據此欄
//     以 `EXCLUDED.collected_at > digital_usage.collected_at` 判定勝出。用 Agent 端時間戳而非
//     後端接收時間，因為要比較的是「哪一次採集較新」而非「哪一個封包先到」。
//
// employee_id 與 device_id 皆不上送（[D14]）——Agent 只持有 id_token，後端以其查
// DEVICE_BINDING 即同時解出兩者，故 [D14] 將 device_id 納入唯一鍵一事對 Agent payload 零改動。

// 共同欄位的 JSON 鍵。攤平時最後寫入，確保其值恆取自 Event 的專屬欄位，
// 不會被 payload 內的同名鍵蓋掉（唯一鍵欄位不可由量值 map 決定）。
const (
	fieldEventID     = "event_id"
	fieldPathType    = "path_type"
	fieldUsageDate   = "usage_date"
	fieldCollectedAt = "collected_at"
)

type wireBody struct {
	IDToken string           `json:"id_token"`
	Events  []map[string]any `json:"events"`
}

// wireEventOf 把一筆佇列事件攤平為單層 JSON 物件。
func wireEventOf(e queue.Event) map[string]any {
	m := make(map[string]any, len(e.Payload)+4)
	for k, v := range e.Payload {
		m[k] = v
	}
	m[fieldEventID] = e.ID
	m[fieldPathType] = string(e.PathType)
	m[fieldUsageDate] = e.UsageDate
	m[fieldCollectedAt] = e.CollectedAt.UTC().Format(time.RFC3339Nano)
	return m
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
		body.Events = append(body.Events, wireEventOf(e))
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
