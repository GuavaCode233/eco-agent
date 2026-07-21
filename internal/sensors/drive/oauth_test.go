package drive

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/oauth2"
)

func TestOAuthConfig_MissingCredentials(t *testing.T) {
	t.Setenv(EnvClientID, "")
	t.Setenv(EnvClientSecret, "")

	_, err := OAuthConfig("")
	if !errors.Is(err, ErrCredentialsNotConfigured) {
		t.Fatalf("err = %v, want ErrCredentialsNotConfigured", err)
	}
}

func TestOAuthConfig_Present(t *testing.T) {
	t.Setenv(EnvClientID, "test-client-id")
	t.Setenv(EnvClientSecret, "test-secret")

	conf, err := OAuthConfig("http://127.0.0.1:1234/callback")
	if err != nil {
		t.Fatalf("OAuthConfig: %v", err)
	}
	if conf.ClientID != "test-client-id" {
		t.Errorf("ClientID = %q", conf.ClientID)
	}
	if conf.RedirectURL != "http://127.0.0.1:1234/callback" {
		t.Errorf("RedirectURL = %q", conf.RedirectURL)
	}
	if len(conf.Scopes) != 1 || conf.Scopes[0] != Scope {
		t.Errorf("Scopes = %v, want [%s]", conf.Scopes, Scope)
	}
	if conf.Endpoint.TokenURL != googleTokenURL {
		t.Errorf("TokenURL = %q, want %q", conf.Endpoint.TokenURL, googleTokenURL)
	}
}

func TestTokenPath_EnvOverride(t *testing.T) {
	want := filepath.Join(t.TempDir(), "my-token.json")
	t.Setenv(EnvTokenPath, want)

	got, err := TokenPath()
	if err != nil {
		t.Fatalf("TokenPath: %v", err)
	}
	if got != want {
		t.Errorf("TokenPath = %q, want %q", got, want)
	}
}

func TestSaveLoadToken_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "token.json") // 子目錄不存在，saveToken 應建立
	want := &oauth2.Token{
		AccessToken:  "access-abc",
		RefreshToken: "refresh-xyz",
		TokenType:    "Bearer",
		Expiry:       time.Now().Add(time.Hour).Round(time.Second),
	}
	if err := saveToken(path, want); err != nil {
		t.Fatalf("saveToken: %v", err)
	}

	got, err := loadToken(path)
	if err != nil {
		t.Fatalf("loadToken: %v", err)
	}
	if got.AccessToken != want.AccessToken || got.RefreshToken != want.RefreshToken {
		t.Errorf("loaded token = %+v, want access=%s refresh=%s", got, want.AccessToken, want.RefreshToken)
	}
}

func TestLoadToken_MissingFile(t *testing.T) {
	_, err := loadToken(filepath.Join(t.TempDir(), "nonexistent.json"))
	if !errors.Is(err, ErrNotAuthorized) {
		t.Fatalf("err = %v, want ErrNotAuthorized", err)
	}
}
