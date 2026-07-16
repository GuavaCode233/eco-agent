// Package enroll 管理 Eco-Agent 的裝置綁定與身份憑證（v12 §4.4.2）。
//
// 綁定採「一次綁定、長期常駐、雙向可解除」的裝置註冊，歸戶的 employee_id 由此建立。
// Agent 全程只持有不可逆的 ID Token，不直接持有員工 ID（純感測、不碰個資）。
//
// 現階段後端不存在，故本套件以 mock 實作（§7）：
//   - IDToken()／AccessToken()／RefreshToken() 回傳 mock 常數；
//   - 但憑證一律經 platform.Keychain 金鑰庫抽象存取（Refresh Token 不寫純文字檔，§1），
//     日後換真值只改 keychain 實作與 Bind() 內容，不動本套件對外結構。
//
// ── 完整綁定規格（供日後實作對照，現不落地後端側）──────────────────────────────
//
// 綁定階段流程（§4.4.2）：
//  1. Agent 向後端索取一次性 binding_code（短效）；後端於 BINDING_CODE 表建立記錄
//     （status=pending、created_at、expires_at = created_at + bindingCodeTTL(5 分)、
//     device_id 指向本裝置）。
//  2. Agent 將 binding_code 編入 QR Code 顯示（內容為全系統統一 custom scheme URI，
//     見 v12 §4.5，例：ecosensing://bind?code=<binding_code>）。
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
//
// ─────────────────────────────────────────────────────────────────────────────
package enroll

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"eco-agent/internal/platform"
)

// 金鑰庫鍵名（綁定產物持久化於金鑰庫）。
const (
	keyIDToken      = "eco-agent.id_token"
	keyRefreshToken = "eco-agent.refresh_token"
)

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

// Enroller 提供身份憑證存取，並封裝綁定／撤銷生命週期。並行安全。
type Enroller struct {
	mu sync.Mutex
	kc platform.Keychain

	// Access Token 於記憶體快取（不落金鑰庫；短期、可隨時由 Refresh Token 換發）。
	accessToken       string
	accessTokenExpiry time.Time

	revoked bool

	// now 供測試注入時間；預設 time.Now。
	now func() time.Time
}

// New 建立 Enroller，憑證經指定金鑰庫存取。
func New(kc platform.Keychain) *Enroller {
	return &Enroller{kc: kc, now: time.Now}
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
// TODO(backend): 目前為 mock——直接把 mock 雙 token 與 ID Token 寫入金鑰庫，模擬綁定完成。
// 後端就緒後，改為實作本檔頂部註解的完整流程：索取 binding_code → 顯示 custom scheme
// URI QR（§4.5）→ 等 App 掃碼 → 後端發雙 token → 存金鑰庫。介面（本方法簽章）維持不變。
func (e *Enroller) Bind(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.revoked {
		return ErrRevoked
	}
	return e.bindLocked(ctx)
}

func (e *Enroller) bindLocked(_ context.Context) error {
	// MOCK: 略過 binding_code 索取／QR／App 掃碼／後端換 token，直接落地 mock 憑證。
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

// refreshAccessTokenLocked 以金鑰庫中的 Refresh Token 換發新的 Access Token。
//
// TODO(backend): 目前為 mock——確認 Refresh Token 存在後直接回 mock Access Token。
// 後端就緒後改為 HTTP 呼叫換發端點（帶 Refresh Token），效期由後端回應決定。
func (e *Enroller) refreshAccessTokenLocked(_ context.Context) error {
	rt, err := e.kc.Get(keyRefreshToken)
	if errors.Is(err, platform.ErrKeychainNotFound) {
		return ErrNotBound
	}
	if err != nil {
		return fmt.Errorf("enroll: refresh access token: %w", err)
	}
	_ = rt // MOCK: 真實流程會帶 rt 呼叫後端換發端點。
	e.accessToken = mockAccessToken
	e.accessTokenExpiry = e.now().Add(mockAccessTokenTTL)
	return nil
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
// TODO(backend): 真實流程尚須通知後端標記解綁（device_binding → revoked）。現僅清本機。
func (e *Enroller) Unbind(ctx context.Context) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	// TODO(backend): 呼叫後端解綁端點通知標記 revoked。
	return e.clearLocked()
}
