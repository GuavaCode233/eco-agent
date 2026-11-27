package uploader

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"eco-agent/internal/config"
	"eco-agent/internal/queue"
)

// senderURL 取出 New() 未被 WithUploadURL/WithSender 覆寫時，內部預設建立的
// HTTPSender 目標 URL，供以下測試斷言 §5 的優先順序邏輯。
func senderURL(t *testing.T, u *Uploader) string {
	t.Helper()
	s, ok := u.sender.(*HTTPSender)
	if !ok {
		t.Fatalf("sender type = %T, want *HTTPSender", u.sender)
	}
	return s.url
}

// TestNewDefaultUploadURLFromBaseURL 驗證未設 ECO_AGENT_UPLOAD_URL 時，預設端點依
// cfg.BaseURL 組出 digital-usage/batch（docs/Eco-Agent_後端串接改動清單.md §5）。
func TestNewDefaultUploadURLFromBaseURL(t *testing.T) {
	t.Setenv(EnvUploadURL, "")
	cfg := config.Config{BaseURL: "https://api.example.com"}

	u := New(nil, nil, cfg)
	want := "https://api.example.com/api/agent/digital-usage/batch"
	if got := senderURL(t, u); got != want {
		t.Errorf("default upload URL = %q, want %q", got, want)
	}
}

// TestNewEnvUploadURLTakesPriorityOverBaseURL 驗證 ECO_AGENT_UPLOAD_URL 優先權高於
// cfg.BaseURL 組出的端點（供本機 mock server／測試沿用）。
func TestNewEnvUploadURLTakesPriorityOverBaseURL(t *testing.T) {
	t.Setenv(EnvUploadURL, "http://127.0.0.1:9999/override")
	cfg := config.Config{BaseURL: "https://api.example.com"}

	u := New(nil, nil, cfg)
	want := "http://127.0.0.1:9999/override"
	if got := senderURL(t, u); got != want {
		t.Errorf("upload URL = %q, want env override %q", got, want)
	}
}

// TestNewFallsBackToDefaultUploadURLWhenBaseURLEmpty 驗證 cfg.BaseURL 也為空（異常/零值
// Config）時退到 DefaultUploadURL，而非組出一個殘缺的 URL。
func TestNewFallsBackToDefaultUploadURLWhenBaseURLEmpty(t *testing.T) {
	t.Setenv(EnvUploadURL, "")
	u := New(nil, nil, config.Config{})

	if got := senderURL(t, u); got != DefaultUploadURL {
		t.Errorf("upload URL = %q, want DefaultUploadURL %q", got, DefaultUploadURL)
	}
}

// TestHTTPSenderParsesAccepted 驗證 HTTPSender 解析 200 回應的 {"accepted": N}；
// 非 200 或無 body 時 Accepted 為 nil。
func TestHTTPSenderParsesAccepted(t *testing.T) {
	m := NewMockIngestServer(http.StatusOK)
	srv := httptest.NewServer(m.Handler())
	defer srv.Close()

	s := NewHTTPSender(srv.URL + "/mock/ingest")
	evs := []queue.Event{{ID: "e1", PathType: queue.PathComputer, UsageDate: "2026-11-27"}, {ID: "e2", PathType: queue.PathComputer, UsageDate: "2026-11-27"}}
	resp, err := s.Send(context.Background(), Batch{IDToken: "idt", AccessToken: "at", Events: evs})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK || resp.Accepted == nil || *resp.Accepted != 2 {
		t.Fatalf("resp = %+v (accepted %v), want 200 accepted=2", resp, resp.Accepted)
	}

	m.SetStatus(http.StatusForbidden)
	resp, err = s.Send(context.Background(), Batch{IDToken: "idt", AccessToken: "at", Events: evs})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusForbidden || resp.Accepted != nil {
		t.Fatalf("resp = %+v, want 403 with nil Accepted", resp)
	}
}
