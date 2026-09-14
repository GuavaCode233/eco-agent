package config

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// init 讓本套件所有測試預設「網路已停用」：sensorConfigHTTPClient 指向一個立即失敗的
// Transport，避免任何呼叫 Load() 的測試意外對 prodAPIBaseURL／testAPIBaseURL 發出真實
// 連線（例如 TestLoadReadsEnvProfile 這類與 sensor_config 拉取無關的測試）。
// 想測試「拉取成功覆蓋數值」的測試改用 withSensorConfigServer 明確指向 httptest.Server。
func init() {
	sensorConfigHTTPClient = &http.Client{Transport: disabledNetworkTransport{}}
}

type disabledNetworkTransport struct{}

func (disabledNetworkTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("config test: network disabled by default; use withSensorConfigServer to opt in")
}

// withSensorConfigServer 啟動一個 httptest.Server 並讓 sensorConfigHTTPClient 指向它，
// 測試結束後自動還原（避免污染其他測試）。
func withSensorConfigServer(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	prev := sensorConfigHTTPClient
	sensorConfigHTTPClient = srv.Client()
	t.Cleanup(func() { sensorConfigHTTPClient = prev })

	return srv
}

// validSensorConfigResponse 回傳一組與正式值刻意不同的數值，供測試辨識「確實來自回應」
// 而非巧合等於 profile 本機常數。
func validSensorConfigResponse() sensorConfigResponse {
	return sensorConfigResponse{
		Version:                        7,
		BindingCodeTTLSec:              42,
		AccessTokenTTLSec:              43,
		RefreshTokenTTLSec:             44,
		ComputerUsageRecordIntervalSec: 11,
		DriveQuotaIntervalSec:          12,
		CheckIntervalSec:               13,
		ThresholdCount:                 14,
		MaxAgeSec:                      15,
		PrinterPollIntervalSec:         16,
		UploadBatchMax:                 17,
	}
}

// TestLoadFetchesSensorConfigOverridesLocalConstants 驗證 Load() 拉取成功時，回應數值
// 覆蓋 profile 本機常數（docs/Eco-Agent_後端串接改動清單.md §2）。
func TestLoadFetchesSensorConfigOverridesLocalConstants(t *testing.T) {
	resp := validSensorConfigResponse()
	srv := withSensorConfigServer(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != PathSensorConfig {
			t.Errorf("request path = %q, want %q", r.URL.Path, PathSensorConfig)
		}
		if r.Method != http.MethodGet {
			t.Errorf("request method = %q, want GET", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})

	t.Setenv(EnvAPIBaseURL, srv.URL)
	got := Load()

	want := Config{
		Profile:                     ProfileProduction,
		BaseURL:                     srv.URL,
		BindingCodeTTL:              42 * time.Second,
		AccessTokenExp:              43 * time.Second,
		RefreshTokenExp:             44 * time.Second,
		ComputerUsageRecordInterval: 11 * time.Second,
		DriveQuotaInterval:          12 * time.Second,
		CheckInterval:               13 * time.Second,
		ThresholdCount:              14,
		MaxAge:                      15 * time.Second,
		PrinterPollInterval:         16 * time.Second,
		UploadBatchMax:              17,
	}
	if got != want {
		t.Errorf("Load() = %+v, want %+v", got, want)
	}
}

// TestLoadFallsBackWhenFetchFails 驗證連線失敗（無 httptest.Server 可連）時，Load() 回傳
// profile 本機常數，不讓 Agent 卡住或崩潰。
func TestLoadFallsBackWhenFetchFails(t *testing.T) {
	t.Setenv(EnvAPIBaseURL, "")
	t.Setenv(EnvProfile, string(ProfileTesting))

	got := Load()
	want := LoadProfile(ProfileTesting)
	if got != want {
		t.Errorf("Load() on fetch failure = %+v, want profile defaults %+v", got, want)
	}
}

// TestLoadFallsBackOnNon200Status 驗證後端回非 200 時同樣 fallback。
func TestLoadFallsBackOnNon200Status(t *testing.T) {
	srv := withSensorConfigServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	t.Setenv(EnvAPIBaseURL, srv.URL)

	got := Load()
	want := LoadProfile(ProfileProduction)
	want.BaseURL = srv.URL
	if got != want {
		t.Errorf("Load() on 500 = %+v, want %+v", got, want)
	}
}

// TestLoadFallsBackOnMalformedJSON 驗證回應 200 但 body 不是合法 JSON 時同樣 fallback。
func TestLoadFallsBackOnMalformedJSON(t *testing.T) {
	srv := withSensorConfigServer(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{not valid json"))
	})
	t.Setenv(EnvAPIBaseURL, srv.URL)

	got := Load()
	want := LoadProfile(ProfileProduction)
	want.BaseURL = srv.URL
	if got != want {
		t.Errorf("Load() on malformed JSON = %+v, want %+v", got, want)
	}
}
