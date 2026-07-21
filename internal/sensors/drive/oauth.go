// 本檔提供路徑 C 的 Google OAuth 2.0 憑證載入與 token 落地（§6）。
//
// 設計要點：
//   - Client ID/Secret 一律由環境變數載入，切勿硬編碼或提交版控（§6、§7 TODO(secrets)）。
//   - 授權範圍最小化：drive.metadata.readonly（只讀 metadata 已足以取 storageQuota）。
//   - 首次授權後的 refresh token 落地為 JSON 檔（路徑可經環境變數覆寫）。§6 明訂 Google
//     OAuth token 存檔即可（此為第三方雲端憑證，與後端簽發的 Refresh Token 不同；後者才
//     須走系統金鑰庫，見 platform.Keychain / enroll）。
//   - conf.Client 會自動以 refresh token 換發過期的 access token，故上層免管 token 生命週期。
package drive

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/oauth2"
)

// 環境變數名稱（§6：需人工填入的憑證位置，勿硬編碼）。
const (
	// EnvClientID：OAuth 2.0 用戶端 ID。TODO(secrets): 由部署者提供。
	EnvClientID = "GOOGLE_OAUTH_CLIENT_ID"
	// EnvClientSecret：OAuth 2.0 用戶端密鑰。TODO(secrets): 由部署者提供。
	EnvClientSecret = "GOOGLE_OAUTH_CLIENT_SECRET"
	// EnvTokenPath：refresh token 落地路徑（可選；未設用使用者設定目錄下預設）。
	EnvTokenPath = "GOOGLE_OAUTH_TOKEN_PATH"
)

// Scope 為只讀 metadata 範圍——取 storageQuota 足矣，符合最小權限（§6）。
const Scope = "https://www.googleapis.com/auth/drive.metadata.readonly"

// Google OAuth 2.0 端點（直接內嵌，避免 import golang.org/x/oauth2/google 拉入
// cloud metadata 等額外相依，維持精簡的單一靜態執行檔定位）。
const (
	googleAuthURL  = "https://accounts.google.com/o/oauth2/auth"
	googleTokenURL = "https://oauth2.googleapis.com/token"
)

var (
	// ErrCredentialsNotConfigured 表示未設定 GOOGLE_OAUTH_CLIENT_ID/SECRET。
	// 路徑 C 應優雅降級（記 log、跳過），不使整個 Agent 崩潰（§6）。
	ErrCredentialsNotConfigured = errors.New("drive: google oauth credentials not configured (set GOOGLE_OAUTH_CLIENT_ID/SECRET)")
	// ErrNotAuthorized 表示尚未完成首次授權（token 檔不存在）。需先跑一次授權流程（見 Authorize）。
	ErrNotAuthorized = errors.New("drive: not authorized yet (run the one-time authorization first)")
)

// OAuthConfig 由環境變數組出 OAuth2 設定。未設 client id/secret 回 ErrCredentialsNotConfigured。
// redirectURL 供授權流程（Authorize）帶入 loopback 位址；純取用量時可傳空字串。
//
// TODO(secrets): 由部署者以環境變數提供 GOOGLE_OAUTH_CLIENT_ID/SECRET，勿硬編碼、勿提交版控。
func OAuthConfig(redirectURL string) (*oauth2.Config, error) {
	id := os.Getenv(EnvClientID)
	secret := os.Getenv(EnvClientSecret)
	if id == "" || secret == "" {
		return nil, ErrCredentialsNotConfigured
	}
	return &oauth2.Config{
		ClientID:     id,
		ClientSecret: secret,
		Scopes:       []string{Scope},
		RedirectURL:  redirectURL,
		Endpoint: oauth2.Endpoint{
			AuthURL:  googleAuthURL,
			TokenURL: googleTokenURL,
		},
	}, nil
}

// TokenPath 回傳 refresh token 落地路徑：優先環境變數 GOOGLE_OAUTH_TOKEN_PATH，
// 否則使用者設定目錄下 eco-agent/google_oauth_token.json。
func TokenPath() (string, error) {
	if p := os.Getenv(EnvTokenPath); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("drive: resolve default token path: %w", err)
	}
	return filepath.Join(dir, "eco-agent", "google_oauth_token.json"), nil
}

// loadToken 由 JSON 檔讀回 oauth2 token（含 refresh token）。檔不存在回 ErrNotAuthorized。
func loadToken(path string) (*oauth2.Token, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrNotAuthorized
	}
	if err != nil {
		return nil, fmt.Errorf("drive: read token %q: %w", path, err)
	}
	var tok oauth2.Token
	if err := json.Unmarshal(b, &tok); err != nil {
		return nil, fmt.Errorf("drive: parse token %q: %w", path, err)
	}
	return &tok, nil
}

// saveToken 將 oauth2 token 以 JSON 落地（0600 權限），供首次授權後保存 refresh token。
func saveToken(path string, tok *oauth2.Token) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("drive: create token dir: %w", err)
	}
	b, err := json.MarshalIndent(tok, "", "  ")
	if err != nil {
		return fmt.Errorf("drive: marshal token: %w", err)
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		return fmt.Errorf("drive: write token %q: %w", path, err)
	}
	return nil
}

// NewClient 建立授權過的 *http.Client：讀憑證與 token，回傳的 client 會自動帶 Bearer token
// 並在 access token 過期時以 refresh token 換發（使用者無感）。
//
// 未設憑證回 ErrCredentialsNotConfigured；尚未授權回 ErrNotAuthorized——兩者皆供上層
// （2.2 感測器）優雅降級判斷（記 log、跳過路徑 C，其餘路徑照常）。
func NewClient(ctx context.Context) (*http.Client, error) {
	conf, err := OAuthConfig("") // 取用量不需 redirect
	if err != nil {
		return nil, err
	}
	path, err := TokenPath()
	if err != nil {
		return nil, err
	}
	tok, err := loadToken(path)
	if err != nil {
		return nil, err
	}

	// conf.TokenSource 內含 ReuseTokenSource＋自動換發；外層以 persistTokenSource 觀察，
	// 於 refresh token 變動時寫回檔案（Google 預設不輪換 refresh token，但保守持久化以防輪換）。
	src := &persistTokenSource{
		src:  conf.TokenSource(ctx, tok),
		path: path,
		last: tok.RefreshToken,
	}
	hc := oauth2.NewClient(ctx, src)
	hc.Timeout = 30 * time.Second
	return hc, nil
}

// persistTokenSource 包裝 oauth2.TokenSource，於 refresh token 更新時寫回檔案。
// 內層 conf.TokenSource 已快取 access token，故本層的 Token() 多數呼叫僅為廉價比較。
type persistTokenSource struct {
	src  oauth2.TokenSource
	path string
	last string // 上次持久化的 refresh token，避免無變化時重複寫檔
}

func (p *persistTokenSource) Token() (*oauth2.Token, error) {
	tok, err := p.src.Token()
	if err != nil {
		return nil, err
	}
	if tok.RefreshToken != "" && tok.RefreshToken != p.last {
		if err := saveToken(p.path, tok); err == nil {
			p.last = tok.RefreshToken
		}
	}
	return tok, nil
}

var _ oauth2.TokenSource = (*persistTokenSource)(nil)
