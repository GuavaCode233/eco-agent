// Package drive 實作路徑 C：雲端儲存感測（CLAUDE.md Step 2）。
//
// 本階段（Step 2.1）落地「真串 Google Drive API v3」：以 about?fields=storageQuota
// 取得帳號儲存用量。憑證（OAuth Client ID/Secret、refresh token 落地路徑）由環境變數
// 提供，見 oauth.go 與 §6；未提供時優雅降級（記 log、跳過），不使整個 Agent 崩潰。
//
// 尚未落地（後續子項，見 CLAUDE.md §2 Step 2 表）：
//   - 2.2 觸發模型：以持久化時間戳 lastDriveQuotaCheckAt + checkInterval 到期判斷輪詢
//     （不可用 sleep(24h) 絕對計時器）；
//   - 2.5 能耗換算與送出：能耗 = 儲存量(GB) × PUE × 電力係數，payload {date, drive_usage_gb}，
//     走 HTTPS 直進後端（現階段 mock 送出）。
//
// 本檔僅提供「取用量」的 API 客戶端與其抽象介面，供 2.2 感測器注入。
package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

// DriveAPIBaseURL 為 Google Drive API v3 的基底位址。測試以 WithBaseURL 覆寫指向假伺服器。
const DriveAPIBaseURL = "https://www.googleapis.com/drive/v3"

const bytesPerGB = 1e9 // 用 10^9（GB，非 GiB）與後端能耗換算係數一致。

// Quota 表示 Google Drive 帳號的儲存用量（取自 about.storageQuota）。
// 所有量值為位元組；Drive API 以字串回傳 int64，已於此解析為數值。
type Quota struct {
	// Usage 為整個帳號的總用量（Drive + Gmail + Photos 等），對應 storageQuota.usage。
	Usage int64
	// UsageInDrive 為僅 Drive 內容的用量，對應 storageQuota.usageInDrive。
	UsageInDrive int64
	// UsageInDriveTrash 為 Drive 垃圾桶用量，對應 storageQuota.usageInDriveTrash。
	UsageInDriveTrash int64
	// Limit 為配額上限；0 表示未設上限（部分 Workspace 帳號無 limit 欄位）。
	Limit int64
}

// UsageGB 回傳帳號總用量（GB，10^9 位元組）。供 2.5 能耗換算取「儲存量(GB)」。
func (q Quota) UsageGB() float64 { return float64(q.Usage) / bytesPerGB }

// UsageInDriveGB 回傳僅 Drive 內容用量（GB）。
func (q Quota) UsageInDriveGB() float64 { return float64(q.UsageInDrive) / bytesPerGB }

// QuotaSampler 抽象「取 Google Drive 儲存用量」，供路徑 C 感測器（2.2）以介面注入，
// 便於測試（fake 滿足）並維持感測器對 Drive API 的最小依賴面。真實實作為 *APIClient。
type QuotaSampler interface {
	StorageQuota(ctx context.Context) (Quota, error)
}

// APIClient 以真實 Google Drive API v3 取得儲存用量。
type APIClient struct {
	httpClient *http.Client
	baseURL    string
}

// Option 以函式選項調整 APIClient（對齊 sensors/computer 的 WithX 慣例）。
type Option func(*APIClient)

// WithBaseURL 覆寫 API 基底位址（測試指向 httptest 假伺服器）。
func WithBaseURL(u string) Option {
	return func(c *APIClient) {
		if u != "" {
			c.baseURL = u
		}
	}
}

// NewAPIClient 以既有 *http.Client 建立 APIClient。
//
// httpClient 應為 OAuth 授權過的 client（見 oauth.go NewClient）：會自動帶 Bearer token
// 並在 access token 過期時以 refresh token 換發，故本客戶端本身不碰 OAuth 細節。
func NewAPIClient(httpClient *http.Client, opts ...Option) *APIClient {
	c := &APIClient{httpClient: httpClient, baseURL: DriveAPIBaseURL}
	for _, o := range opts {
		o(c)
	}
	return c
}

// storageQuotaResponse 對應 about?fields=storageQuota 的回應結構。
// Drive API 以字串回傳 int64 量值（避免 JSON number 精度問題），故此處為 string。
type storageQuotaResponse struct {
	StorageQuota struct {
		Limit             string `json:"limit"`
		Usage             string `json:"usage"`
		UsageInDrive      string `json:"usageInDrive"`
		UsageInDriveTrash string `json:"usageInDriveTrash"`
	} `json:"storageQuota"`
}

// StorageQuota 呼叫 GET /about?fields=storageQuota 取得目前儲存用量。
//
// 以 fields 遮罩只取 storageQuota，最小化回應體與所需權限（只讀 metadata scope，見 §6）。
func (c *APIClient) StorageQuota(ctx context.Context) (Quota, error) {
	u := c.baseURL + "/about?fields=storageQuota"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Quota{}, fmt.Errorf("drive: build about request: %w", err)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Quota{}, fmt.Errorf("drive: about request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return Quota{}, fmt.Errorf("drive: about returned HTTP %d: %s",
			resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var r storageQuotaResponse
	if err := json.NewDecoder(resp.Body).Decode(&r); err != nil {
		return Quota{}, fmt.Errorf("drive: decode about response: %w", err)
	}
	return Quota{
		Usage:             parseInt64(r.StorageQuota.Usage),
		UsageInDrive:      parseInt64(r.StorageQuota.UsageInDrive),
		UsageInDriveTrash: parseInt64(r.StorageQuota.UsageInDriveTrash),
		Limit:             parseInt64(r.StorageQuota.Limit),
	}, nil
}

// parseInt64 容錯解析 Drive API 的字串量值；空字串或不可解析（如 unlimited 帳號缺 limit）
// 一律視為 0。
func parseInt64(s string) int64 {
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// 確保 APIClient 滿足 QuotaSampler 介面。
var _ QuotaSampler = (*APIClient)(nil)
