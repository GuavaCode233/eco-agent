// 本檔提供路徑 C 的「一次性 OAuth 授權」loopback 流程（§6：首次授權後 refresh token 落地）。
//
// 由人工於首次設定時執行（見 cmd/drive-demo authorize）：啟動本機回呼伺服器 → 引導使用者
// 於瀏覽器同意授權 → 交換授權碼取得含 refresh token 的 token → 落地保存。之後 Agent 常駐期間
// 以該 refresh token 自動換發 access token，無需再互動。
package drive

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"

	"golang.org/x/oauth2"
)

// Authorize 執行一次性 OAuth 授權（127.0.0.1 loopback 流程），取得並保存 refresh token。
//
// 回傳 token 落地路徑。未設憑證回 ErrCredentialsNotConfigured。流程：
//  1. 於 127.0.0.1 起臨時回呼伺服器（隨機埠）；
//  2. 以 access_type=offline + prompt=consent 產生授權網址（確保回傳 refresh token）；
//  3. 引導使用者於瀏覽器開啟該網址並同意；
//  4. 收到回呼授權碼後交換 token 並落地。
//
// 引導文字經 promptWriter 輸出（demo 傳 os.Stdout）；ctx 取消可中止等待。
func Authorize(ctx context.Context, promptWriter interface{ Write([]byte) (int, error) }) (string, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", fmt.Errorf("drive: start callback listener: %w", err)
	}
	defer ln.Close()

	port := ln.Addr().(*net.TCPAddr).Port
	redirectURL := fmt.Sprintf("http://127.0.0.1:%d/callback", port)

	conf, err := OAuthConfig(redirectURL)
	if err != nil {
		return "", err
	}

	state, err := randomState()
	if err != nil {
		return "", err
	}
	authURL := conf.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"), // 強制回傳 refresh token
	)

	codeCh := make(chan string, 1)
	errCh := make(chan error, 1)
	mux := http.NewServeMux()
	mux.HandleFunc("/callback", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("state") != state {
			http.Error(w, "state mismatch", http.StatusBadRequest)
			errCh <- errors.New("drive: oauth state mismatch (可能為 CSRF 或逾時)")
			return
		}
		if e := q.Get("error"); e != "" {
			http.Error(w, "authorization denied: "+e, http.StatusBadRequest)
			errCh <- fmt.Errorf("drive: authorization denied: %s", e)
			return
		}
		fmt.Fprintln(w, "Eco-Agent：Google 授權完成，可關閉此分頁並返回終端機。")
		codeCh <- q.Get("code")
	})

	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Shutdown(context.Background())

	fmt.Fprintln(promptWriter, "請在瀏覽器開啟以下網址完成 Google 授權（授權後會自動導回本機）：")
	fmt.Fprintln(promptWriter, authURL)

	var code string
	select {
	case code = <-codeCh:
	case err := <-errCh:
		return "", err
	case <-ctx.Done():
		return "", ctx.Err()
	}

	tok, err := conf.Exchange(ctx, code)
	if err != nil {
		return "", fmt.Errorf("drive: exchange authorization code: %w", err)
	}
	if tok.RefreshToken == "" {
		// 通常因帳號先前已授權而未帶 prompt=consent；此處已強制 consent，理論上不會發生。
		return "", errors.New("drive: no refresh token returned (需 access_type=offline 與使用者同意)")
	}

	path, err := TokenPath()
	if err != nil {
		return "", err
	}
	if err := saveToken(path, tok); err != nil {
		return "", err
	}
	return path, nil
}

// randomState 產生防 CSRF 的隨機 state 參數。
func randomState() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("drive: generate oauth state: %w", err)
	}
	return hex.EncodeToString(b), nil
}
