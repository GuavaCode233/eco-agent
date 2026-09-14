// Package enroll 管理 Eco-Agent 的裝置綁定與身份憑證（v12 §4.4.2）。
//
// 綁定採「一次綁定、長期常駐、雙向可解除」的裝置註冊，歸戶的 employee_id 由此建立。
// Agent 全程只持有不可逆的 ID Token，不直接持有員工 ID（純感測、不碰個資）。
//
// 綁定五端點真串（docs/Eco-Agent_後端串接改動清單.md §3）：Bind()／refreshAccessTokenLocked
// 依是否以 WithBaseURL 設定後端 base URL 分兩條路：
//   - 已設定 base URL：走下方完整綁定規格的真實 HTTP 流程（見 enroll_http.go）。
//   - 未設定（baseURL 為空字串，New() 預設值）：維持 mock 捷徑（bindMockLocked／mock 常數），
//     供無後端環境的獨立 demo／測試沿用，不需另起假伺服器（見 §7 mock 慣例）。
//
// 憑證一律經 platform.Keychain 金鑰庫抽象存取（Refresh Token 不寫純文字檔，§1）。
// device_uuid（DeviceUUID）與後端無關、純本機識別碼產生與持久化，索取 binding_code 時帶上。
//
// ── 完整綁定規格 ────────────────────────────────────────────────────────────
//
// 綁定階段流程（§4.4.2）：
//  1. Agent 向後端索取一次性 binding_code（短效），**帶上本機持久化的 device_uuid**
//     （見 DeviceUUID；供後端 upsert DEVICE 列而非盲插，避免綁定失敗殘留列累積）；
//     後端於 BINDING_CODE 表建立記錄（status=pending、created_at、
//     expires_at = created_at + bindingCodeTTL(5 分)、device_id 指向本裝置）。
//  2. Agent 將 binding_code 編入 QR Code 顯示（內容為全系統統一 custom scheme URI，
//     見 v12 §4.5，例：ecosensing://bind?code=<binding_code>）；本階段先以 log 印出 URI，
//     不做 QR 圖檔/ASCII 渲染。
//  3. 員工以已登入的 Eco-Sensing App 掃碼；App 依 URI host/path 判定為綁定動作。
//  4. App 把「已驗證身份 + binding_code」送後端。
//  5. 後端核對 binding_code（status=pending 且 expires_at > now()）→ 建立 device_binding
//     → 回填 BINDING_CODE.employee_id、consumed_at、status=consumed；發放
//     Access Token（短期 1h）+ Refresh Token（長期 90 天）。過期或已消費的碼一律拒絕（防重放）。
//  6. Agent 取得 token，Refresh Token 存入系統金鑰庫（DPAPI／Keychain，不寫純文字檔）→ 轉背景常駐。
//
// 雙 token 與撤銷（§4.4.2）：
//   - Access Token：短期（1h），每次上傳用；後端簽發策略、不落庫；由 Refresh Token 隨時換發，使用者無感。
//   - Refresh Token：長期（90 天），存金鑰庫；後端僅存 refresh_token_hash；不輪換，到期重走綁定。
//   - 撤銷：採「每次上傳夾帶」（不另做心跳）；後端於上傳回應夾帶有效性狀態，若已撤銷回 401/403，
//     Agent 收到即自我清除憑證（含金鑰庫 Refresh Token）、停止上傳（見 ClearCredentials）。
//     refreshAccessTokenLocked 換發時若後端回 401/403（refresh token 已失效/裝置已撤銷），語意
//     不同：僅清本機憑證、不設終止態，讓上層下次 EnsureBound 自動重新 Bind()（見該函式註解）。
//
// ─────────────────────────────────────────────────────────────────────────────
package enroll

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"

	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
)

// 金鑰庫鍵名（綁定產物持久化於金鑰庫）。
const (
	keyIDToken      = "eco-agent.id_token"
	keyRefreshToken = "eco-agent.refresh_token"
)

// stateKeyDeviceUUID 為 device_uuid 於佇列 state 表的持久化鍵（§4.4.2）。
//
// 存於佇列（與 4.4.3 的 SQLite 同檔）而非金鑰庫：device_uuid 不是機密——它只是供後端
// upsert DEVICE 列用的本機識別碼，外洩無安全影響，不需金鑰庫等級的保護。
const stateKeyDeviceUUID = "deviceUUID"

// mock 憑證常數（§7）。日後由真實綁定流程自後端取得。
const (
	// MOCK: 固定 mock 員工 ID Token；即去識別化打包保留的不可逆身份標識（§4.4.2）。
	mockIDToken = "mock-emp-idtoken-eco-0001"
	// MOCK: 固定 mock Refresh Token；真實值 90 天效期、存金鑰庫。
	mockRefreshToken = "mock-refresh-token-eco-0001"
	// MOCK: 固定 mock Access Token；真實值由後端以 Refresh Token 換發、短期。
	mockAccessToken = "mock-access-token-eco-0001"
	// MOCK: Access Token mock 效期；真實值為後端簽發策略（正式 1h，見 config.AccessTokenExp）。
	mockAccessTokenTTL = time.Hour
)

// 綁定/憑證狀態錯誤。
var (
	// ErrNotBound 表示裝置尚未綁定（無可用身份憑證）。呼叫 EnsureBound 後可用。
	ErrNotBound = errors.New("enroll: device not bound")
	// ErrRevoked 表示憑證已被撤銷並自清，Agent 應停止上傳、等待重新綁定。
	ErrRevoked = errors.New("enroll: credentials revoked")
)

// BindingClient 是 enroll 依賴的 HTTP 介面，供綁定流程（索取 binding_code／輪詢核銷／
// 換發 Access Token，見 enroll_http.go）呼叫後端；*http.Client 天然滿足此介面。測試可注入
// httptest.Server 的 client 取代直接打外部服務。
type BindingClient interface {
	Do(req *http.Request) (*http.Response, error)
}

// Enroller 提供身份憑證存取，並封裝綁定／撤銷生命週期。並行安全。
type Enroller struct {
	mu  sync.Mutex
	kc  platform.Keychain
	q   *queue.Queue
	log *slog.Logger

	// httpClient／baseURL：綁定五端點真串所需的 HTTP 存取（見 enroll_http.go）。baseURL 為空
	// （未呼叫 WithBaseURL）時 bindLocked／refreshAccessTokenLocked 走 mock 捷徑，不發任何請求
	// ——供無後端環境的獨立 demo／測試沿用（見套件頂部註解）。
	httpClient BindingClient
	baseURL    string

	// bindingCodeTTL：輪詢核銷 binding_code 的逾時上限（§3.1 步驟 3）；預設對齊
	// config.Config 正式值，呼叫端應以 WithBindingCodeTTL(cfg.BindingCodeTTL) 帶入實際配置。
	bindingCodeTTL time.Duration
	// bindingCodePollInterval：輪詢間隔；測試可縮短以加速。
	bindingCodePollInterval time.Duration

	// Access Token 於記憶體快取（不落金鑰庫；短期、可隨時由 Refresh Token 換發）。
	accessToken       string
	accessTokenExpiry time.Time

	revoked bool

	// now 供測試注入時間；預設 time.Now。
	now func() time.Time
}

// Option 客製化 Enroller。
type Option func(*Enroller)

// WithBindingClient 覆寫綁定流程用的 HTTP client（測試注入 httptest.Server 或假實作）。
func WithBindingClient(c BindingClient) Option {
	return func(e *Enroller) { e.httpClient = c }
}

// WithBaseURL 設定後端 base URL（索取/查詢/換發 token 的端點組裝依據；見 config.Config.BaseURL
// 與 config.Config.APIURL）。留空（不呼叫本選項）則 Bind()／refreshAccessTokenLocked 走 mock
// 捷徑，見套件頂部註解。
func WithBaseURL(base string) Option {
	return func(e *Enroller) { e.baseURL = base }
}

// WithBindingCodeTTL 覆寫輪詢核銷 binding_code 的逾時上限；應帶入 config.Config.BindingCodeTTL。
func WithBindingCodeTTL(d time.Duration) Option {
	return func(e *Enroller) { e.bindingCodeTTL = d }
}

// WithBindingCodePollInterval 覆寫輪詢核銷 binding_code 的間隔（測試用，縮短以加速）。
func WithBindingCodePollInterval(d time.Duration) Option {
	return func(e *Enroller) { e.bindingCodePollInterval = d }
}

// WithLogger 設定日誌器（QR URI 等綁定流程訊息輸出至此）。
func WithLogger(l *slog.Logger) Option {
	return func(e *Enroller) { e.log = l }
}

// New 建立 Enroller，憑證經指定金鑰庫存取；device_uuid（§4.4.2）與持久化佇列同檔存放，
// 故需傳入該佇列（呼叫端應先 queue.Open 再建立 Enroller）。
func New(kc platform.Keychain, q *queue.Queue, opts ...Option) *Enroller {
	e := &Enroller{
		kc:                      kc,
		q:                       q,
		log:                     slog.Default(),
		now:                     time.Now,
		httpClient:              &http.Client{Timeout: 10 * time.Second},
		bindingCodeTTL:          5 * time.Minute, // 對齊 config 正式值；建議以 WithBindingCodeTTL 覆寫
		bindingCodePollInterval: 2 * time.Second,
	}
	for _, o := range opts {
		o(e)
	}
	return e
}

// DeviceUUID 回傳本機持久化的裝置識別碼；不存在則產生一枚（UUID v4）並寫入佇列 state 表。
//
// §4.4.2：索取 binding_code 的端點①（POST /api/agent/binding-code）應帶上此值，供後端
// 據以 upsert DEVICE 列而非盲插——否則每次啟動都盲插一筆，綁定失敗（員工未掃碼／逾時）
// 殘留的 DEVICE 列會持續累積。同一裝置重複呼叫恆得同一值（先讀後端未有才寫入，非每次重產）。
func (e *Enroller) DeviceUUID(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.deviceUUIDLocked(ctx)
}

func (e *Enroller) deviceUUIDLocked(ctx context.Context) (string, error) {
	v, ok, err := e.q.GetState(ctx, stateKeyDeviceUUID)
	if err != nil {
		return "", fmt.Errorf("enroll: read device uuid: %w", err)
	}
	if ok && v != "" {
		return v, nil
	}
	id := uuid.NewString()
	if err := e.q.SetState(ctx, stateKeyDeviceUUID, id); err != nil {
		return "", fmt.Errorf("enroll: persist device uuid: %w", err)
	}
	return id, nil
}

// IsBound 回報裝置是否已綁定（金鑰庫是否存在 Refresh Token）。
func (e *Enroller) IsBound() (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.isBoundLocked()
}

func (e *Enroller) isBoundLocked() (bool, error) {
	_, err := e.kc.Get(keyRefreshToken)
	if errors.Is(err, platform.ErrKeychainNotFound) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("enroll: check bound: %w", err)
	}
	return true, nil
}

// EnsureBound 確保裝置已綁定；未綁定則執行綁定流程。開機時呼叫一次。
func (e *Enroller) EnsureBound(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revoked {
		return ErrRevoked
	}
	bound, err := e.isBoundLocked()
	if err != nil {
		return err
	}
	if bound {
		return nil
	}
	return e.bindLocked(ctx)
}

// Bind 執行裝置綁定流程並將憑證存入金鑰庫。
//
// 若已用 WithBaseURL 設定後端 base URL，走本檔頂部註解的完整流程（索取 binding_code →
// log 印出 custom scheme URI QR → 輪詢等 App 掃碼核銷 → 存金鑰庫，見 enroll_http.go）；
// 否則維持 mock 捷徑（供無後端環境的獨立 demo／測試沿用）。介面（本方法簽章）不受影響。
func (e *Enroller) Bind(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revoked {
		return ErrRevoked
	}
	return e.bindLocked(ctx)
}

func (e *Enroller) bindLocked(ctx context.Context) error {
	// 真實與 mock 流程皆須先確保 device_uuid 已產生並落地（§4.4.2：索取 binding_code
	// 端點①應帶上此值）。
	deviceUUID, err := e.deviceUUIDLocked(ctx)
	if err != nil {
		return err
	}
	if e.baseURL == "" {
		// MOCK: 未設定後端 base URL，略過 binding_code 索取／QR／App 掃碼／後端換 token，
		// 直接落地 mock 憑證（見套件頂部註解）。
		return e.bindMockLocked()
	}
	return e.bindRealLocked(ctx, deviceUUID)
}

// bindMockLocked 是無後端 base URL 時的綁定捷徑：直接寫入固定 mock 憑證。
func (e *Enroller) bindMockLocked() error {
	if err := e.kc.Set(keyIDToken, mockIDToken); err != nil {
		return fmt.Errorf("enroll: store id token: %w", err)
	}
	if err := e.kc.Set(keyRefreshToken, mockRefreshToken); err != nil {
		return fmt.Errorf("enroll: store refresh token: %w", err)
	}
	// 綁定後清掉舊的記憶體 Access Token，強制下次以 Refresh Token 換發。
	e.accessToken = ""
	e.accessTokenExpiry = time.Time{}
	return nil
}

// IDToken 回傳去識別化打包保留的員工 ID Token（§4.4.2）。裝置未綁定回 ErrNotBound。
func (e *Enroller) IDToken() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revoked {
		return "", ErrRevoked
	}
	tok, err := e.kc.Get(keyIDToken)
	if errors.Is(err, platform.ErrKeychainNotFound) {
		return "", ErrNotBound
	}
	if err != nil {
		return "", fmt.Errorf("enroll: id token: %w", err)
	}
	return tok, nil
}

// RefreshToken 自金鑰庫回傳 Refresh Token。裝置未綁定回 ErrNotBound。
func (e *Enroller) RefreshToken() (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revoked {
		return "", ErrRevoked
	}
	tok, err := e.kc.Get(keyRefreshToken)
	if errors.Is(err, platform.ErrKeychainNotFound) {
		return "", ErrNotBound
	}
	if err != nil {
		return "", fmt.Errorf("enroll: refresh token: %w", err)
	}
	return tok, nil
}

// AccessToken 回傳有效的短期上傳憑證；過期時自動以 Refresh Token 換發（使用者無感）。
// 裝置未綁定回 ErrNotBound；已撤銷回 ErrRevoked。
func (e *Enroller) AccessToken(ctx context.Context) (string, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revoked {
		return "", ErrRevoked
	}
	if e.accessToken != "" && e.now().Before(e.accessTokenExpiry) {
		return e.accessToken, nil
	}
	if err := e.refreshAccessTokenLocked(ctx); err != nil {
		return "", err
	}
	return e.accessToken, nil
}

// refreshAccessTokenLocked 以金鑰庫中的 Refresh Token 換發新的 Access Token（§3.2）。
//
// 若已設定後端 base URL，呼叫 POST token/refresh（enroll_http.go）；後端回 401/403
// （refresh token 已失效／裝置已撤銷）時清本機憑證並回 ErrNotBound，讓上層下次
// EnsureBound 自動重新 Bind()——不設 ClearCredentials 的終止態，因為這不是上傳路徑
// 偵測到的撤銷，僅是「這份 refresh token 死了、需要重綁」。
// 未設定 base URL 時維持 mock 捷徑（回 mock Access Token），供無後端環境沿用。
func (e *Enroller) refreshAccessTokenLocked(ctx context.Context) error {
	rt, err := e.kc.Get(keyRefreshToken)
	if errors.Is(err, platform.ErrKeychainNotFound) {
		return ErrNotBound
	}
	if err != nil {
		return fmt.Errorf("enroll: refresh access token: %w", err)
	}

	if e.baseURL == "" {
		// MOCK: 未設定後端 base URL，直接回 mock Access Token。
		e.accessToken = mockAccessToken
		e.accessTokenExpiry = e.now().Add(mockAccessTokenTTL)
		return nil
	}

	out, status, err := e.refreshAccessTokenHTTP(ctx, rt)
	if err != nil {
		return fmt.Errorf("enroll: refresh access token: %w", err)
	}
	switch status {
	case http.StatusOK:
		e.accessToken = out.AccessToken
		e.accessTokenExpiry = e.now().Add(time.Duration(out.ExpiresIn) * time.Second)
		return nil
	case http.StatusUnauthorized, http.StatusForbidden:
		if cerr := e.clearLocked(); cerr != nil {
			return fmt.Errorf("enroll: refresh access token: revoked (status %d), clear credentials: %w", status, cerr)
		}
		return ErrNotBound
	default:
		return fmt.Errorf("enroll: refresh access token: unexpected status %d", status)
	}
}

// ClearCredentials 撤銷自清：清除金鑰庫憑證與記憶體 Access Token，並標記為已撤銷（終止態）。
// 於上傳收到後端 401/403（撤銷夾帶檢查，§4.4.2）時由 uploader 呼叫；之後所有憑證存取回
// ErrRevoked，Agent 應停止上傳，直到重新綁定。
func (e *Enroller) ClearCredentials() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	err := e.clearLocked()
	e.revoked = true // 終止態：與 Unbind 區別，撤銷後不自動重綁。
	return err
}

// clearLocked 清除金鑰庫憑證與記憶體 Access Token（不設 revoked 終止態）。
func (e *Enroller) clearLocked() error {
	e.accessToken = ""
	e.accessTokenExpiry = time.Time{}
	var errs []error
	if err := e.kc.Delete(keyRefreshToken); err != nil {
		errs = append(errs, err)
	}
	if err := e.kc.Delete(keyIDToken); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("enroll: clear credentials: %w", errors.Join(errs...))
	}
	return nil
}

// Unbind 員工端主動解除綁定（換機）：清本機憑證，但不設終止態——之後可再次綁定。
//
// 不呼叫後端解綁端點（POST device-bindings/{id}/revoke）：該端點是管理端觸發的動作，
// 不是 Agent 呼叫的——Agent 側撤銷偵測已正確掛在上傳回應 401/403（見 ClearCredentials）。
// 本函式僅清本機憑證即符合設計（docs/Eco-Agent_後端串接改動清單.md §3.3）。
func (e *Enroller) Unbind(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.clearLocked()
}
