package enroll

// 本檔實作 §3 綁定五端點真串的 HTTP 往返（docs/Eco-Agent_後端串接改動清單.md §3）：
// 索取 binding_code（端點①）→ 顯示 QR URI（②，純 log）→ 輪詢核銷（③）→ 寫入憑證；
// 以及 refreshAccessTokenLocked 呼叫的換發端點（④）。端點⑤（撤銷）由管理端觸發，
// Agent 不呼叫，見 Unbind 註解。
//
// 僅在 e.baseURL 非空時被呼叫（見 enroll.go 的 bindLocked／refreshAccessTokenLocked）；
// baseURL 為空即代表未設定後端，維持 mock 捷徑，不會走到本檔任何函式。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"eco-agent/internal/config"
)

// --- wire 格式（欄位命名依進度表端點①③④回應）---

// bindingCodeRequest 是端點①（POST binding-code）的請求 body。
type bindingCodeRequest struct {
	DeviceUUID string `json:"device_uuid"`
}

// bindingCodeResponse 是端點①的回應。ExpiresAt 為 RFC3339；解析失敗時僅記警告，不影響
// 輪詢（仍有 e.bindingCodeTTL 兜底，見 pollBindingCodeToken）。
type bindingCodeResponse struct {
	Code         string `json:"code"`
	DeviceSecret string `json:"device_secret"`
	ExpiresAt    string `json:"expires_at"`
}

// bindingTokenPollResponse 是端點③（GET binding-code/{code}/token）的回應。pending 時
// 除 Status 外皆為 null；consumed 時四個 token 欄位皆非 null（§6.1 已確認 id_token 直接
// 隨 consumed 回應下發，不需 JWT 解碼繞路）。
type bindingTokenPollResponse struct {
	Status       string  `json:"status"` // "pending" | "consumed"
	AccessToken  *string `json:"access_token"`
	RefreshToken *string `json:"refresh_token"`
	ExpiresIn    *int    `json:"expires_in"`
	IDToken      *string `json:"id_token"`
}

// tokenRefreshRequest 是端點④（POST token/refresh）的請求 body；無需 Bearer header
// （與其餘端點不同，見 §3.2）。
type tokenRefreshRequest struct {
	RefreshToken string `json:"refresh_token"`
}

// tokenRefreshResponse 是端點④成功時（200）的回應。
type tokenRefreshResponse struct {
	AccessToken string `json:"access_token"`
	ExpiresIn   int    `json:"expires_in"`
}

// apiURL 組出 e.baseURL 下的完整端點 URL（見 config.Config.APIURL）。
func (e *Enroller) apiURL(path string) string {
	return config.Config{BaseURL: e.baseURL}.APIURL(path)
}

// doJSON 送一次 JSON 請求；200 時解析 JSON 回應至 out（out 為 nil 則略過解析）。非 200
// 一律不解析 body（只丟棄），由呼叫端依自身端點的狀態碼語意處理（五端點對非 200 的反應
// 各不相同：索取 binding_code 視為失敗、換發 token 的 401/403 有特殊語意）。
func (e *Enroller) doJSON(ctx context.Context, method, url string, headers map[string]string, reqBody, out any) (status int, err error) {
	var body io.Reader
	if reqBody != nil {
		b, err := json.Marshal(reqBody)
		if err != nil {
			return 0, fmt.Errorf("marshal request: %w", err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return 0, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK && out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return resp.StatusCode, fmt.Errorf("decode response: %w", err)
		}
	} else {
		_, _ = io.Copy(io.Discard, resp.Body)
	}
	return resp.StatusCode, nil
}

// requestBindingCode 呼叫端點①索取一次性綁定碼（§3.1 步驟 1）。
func (e *Enroller) requestBindingCode(ctx context.Context, deviceUUID string) (bindingCodeResponse, error) {
	var out bindingCodeResponse
	status, err := e.doJSON(ctx, http.MethodPost, e.apiURL(config.PathBindingCode), nil,
		bindingCodeRequest{DeviceUUID: deviceUUID}, &out)
	if err != nil {
		return bindingCodeResponse{}, err
	}
	if status != http.StatusOK && status != http.StatusCreated {
		return bindingCodeResponse{}, fmt.Errorf("binding-code request returned status %d", status)
	}
	if out.Code == "" || out.DeviceSecret == "" {
		return bindingCodeResponse{}, fmt.Errorf("binding-code response missing code/device_secret")
	}
	return out, nil
}

// pollBindingCodeToken 輪詢端點③直到 status=consumed 或逾時（§3.1 步驟 3）。逾時上限為
// e.bindingCodeTTL；若伺服器回的 expires_at 解析成功且更早，則以其為準（更保守）。
func (e *Enroller) pollBindingCodeToken(ctx context.Context, code, deviceSecret string, expiresAt time.Time) (bindingTokenPollResponse, error) {
	deadline := e.now().Add(e.bindingCodeTTL)
	if !expiresAt.IsZero() && expiresAt.Before(deadline) {
		deadline = expiresAt
	}
	url := e.apiURL(config.PathBindingCodeToken(code))
	headers := map[string]string{"X-Device-Secret": deviceSecret}

	for {
		var out bindingTokenPollResponse
		status, err := e.doJSON(ctx, http.MethodGet, url, headers, nil, &out)
		if err != nil {
			return bindingTokenPollResponse{}, err
		}
		if status != http.StatusOK {
			return bindingTokenPollResponse{}, fmt.Errorf("binding-code token poll returned status %d", status)
		}
		if out.Status == "consumed" {
			if out.AccessToken == nil || out.RefreshToken == nil || out.IDToken == nil || out.ExpiresIn == nil {
				return bindingTokenPollResponse{}, fmt.Errorf("consumed response missing token fields")
			}
			return out, nil
		}
		if e.now().After(deadline) {
			return bindingTokenPollResponse{}, fmt.Errorf("binding code polling timed out after %v", e.bindingCodeTTL)
		}
		select {
		case <-ctx.Done():
			return bindingTokenPollResponse{}, ctx.Err()
		case <-time.After(e.bindingCodePollInterval):
		}
	}
}

// bindRealLocked 走完整綁定流程（§3.1；套件頂部註解步驟 1–6）：索取 binding_code →
// log 印出 QR URI → 輪詢核銷 → 寫入金鑰庫／記憶體快取。呼叫端（bindLocked）已確保
// deviceUUID 存在並持有鎖。
func (e *Enroller) bindRealLocked(ctx context.Context, deviceUUID string) error {
	bc, err := e.requestBindingCode(ctx, deviceUUID)
	if err != nil {
		return fmt.Errorf("enroll: request binding code: %w", err)
	}

	e.log.Info("enroll: scan to bind device", "uri", "ecosensing://bind?code="+bc.Code)

	var expiresAt time.Time
	if bc.ExpiresAt != "" {
		if t, perr := time.Parse(time.RFC3339, bc.ExpiresAt); perr == nil {
			expiresAt = t
		} else {
			e.log.Warn("enroll: could not parse binding code expires_at, falling back to bindingCodeTTL",
				"expires_at", bc.ExpiresAt, "err", perr)
		}
	}

	tok, err := e.pollBindingCodeToken(ctx, bc.Code, bc.DeviceSecret, expiresAt)
	if err != nil {
		return fmt.Errorf("enroll: poll binding code: %w", err)
	}

	if err := e.kc.Set(keyIDToken, *tok.IDToken); err != nil {
		return fmt.Errorf("enroll: store id token: %w", err)
	}
	if err := e.kc.Set(keyRefreshToken, *tok.RefreshToken); err != nil {
		return fmt.Errorf("enroll: store refresh token: %w", err)
	}
	e.accessToken = *tok.AccessToken
	e.accessTokenExpiry = e.now().Add(time.Duration(*tok.ExpiresIn) * time.Second)
	return nil
}

// refreshAccessTokenHTTP 呼叫端點④以 Refresh Token 換發新的 Access Token（§3.2）。
func (e *Enroller) refreshAccessTokenHTTP(ctx context.Context, refreshToken string) (tokenRefreshResponse, int, error) {
	var out tokenRefreshResponse
	status, err := e.doJSON(ctx, http.MethodPost, e.apiURL(config.PathTokenRefresh), nil,
		tokenRefreshRequest{RefreshToken: refreshToken}, &out)
	return out, status, err
}
