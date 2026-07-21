package drive

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// newFakeDrive 起一個假 Drive API 伺服器，對 /about 回傳給定的 storageQuota JSON。
func newFakeDrive(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/about" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("fields"); got != "storageQuota" {
			t.Errorf("fields = %q, want storageQuota", got)
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestStorageQuota_Parse(t *testing.T) {
	const body = `{"storageQuota":{"limit":"16106127360","usage":"3221225472","usageInDrive":"2147483648","usageInDriveTrash":"1073741824"}}`
	srv := newFakeDrive(t, http.StatusOK, body)

	c := NewAPIClient(srv.Client(), WithBaseURL(srv.URL))
	q, err := c.StorageQuota(context.Background())
	if err != nil {
		t.Fatalf("StorageQuota: %v", err)
	}

	if q.Limit != 16106127360 {
		t.Errorf("Limit = %d, want 16106127360", q.Limit)
	}
	if q.Usage != 3221225472 {
		t.Errorf("Usage = %d, want 3221225472", q.Usage)
	}
	if q.UsageInDrive != 2147483648 {
		t.Errorf("UsageInDrive = %d, want 2147483648", q.UsageInDrive)
	}
	if q.UsageInDriveTrash != 1073741824 {
		t.Errorf("UsageInDriveTrash = %d, want 1073741824", q.UsageInDriveTrash)
	}
	// 3221225472 位元組 = 3.221225472 GB（10^9）。
	if got := q.UsageGB(); got < 3.221 || got > 3.222 {
		t.Errorf("UsageGB = %v, want ~3.2212", got)
	}
	if got := q.UsageInDriveGB(); got < 2.147 || got > 2.148 {
		t.Errorf("UsageInDriveGB = %v, want ~2.1475", got)
	}
}

func TestStorageQuota_UnlimitedNoLimitField(t *testing.T) {
	// 部分 Workspace 帳號無 limit 欄位（無上限）→ Limit 應解析為 0，不報錯。
	const body = `{"storageQuota":{"usage":"1000000000","usageInDrive":"500000000"}}`
	srv := newFakeDrive(t, http.StatusOK, body)

	c := NewAPIClient(srv.Client(), WithBaseURL(srv.URL))
	q, err := c.StorageQuota(context.Background())
	if err != nil {
		t.Fatalf("StorageQuota: %v", err)
	}
	if q.Limit != 0 {
		t.Errorf("Limit = %d, want 0 (unlimited)", q.Limit)
	}
	if q.Usage != 1000000000 {
		t.Errorf("Usage = %d, want 1000000000", q.Usage)
	}
}

func TestStorageQuota_NonOKStatus(t *testing.T) {
	srv := newFakeDrive(t, http.StatusForbidden, `{"error":{"message":"insufficient permissions"}}`)

	c := NewAPIClient(srv.Client(), WithBaseURL(srv.URL))
	_, err := c.StorageQuota(context.Background())
	if err == nil {
		t.Fatal("expected error on HTTP 403, got nil")
	}
}
