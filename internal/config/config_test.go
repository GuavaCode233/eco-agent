package config

import (
	"testing"
	"time"
)

// TestProductionValues 驗證正式值與 v12 §4.4.4 定案一致。
func TestProductionValues(t *testing.T) {
	c := LoadProfile(ProfileProduction)
	checks := []struct {
		name string
		got  time.Duration
		want time.Duration
	}{
		{"BindingCodeTTL", c.BindingCodeTTL, 5 * time.Minute},
		{"AccessTokenExp", c.AccessTokenExp, 1 * time.Hour},
		{"RefreshTokenExp", c.RefreshTokenExp, 90 * 24 * time.Hour},
		{"ComputerUsageRecordInterval", c.ComputerUsageRecordInterval, 60 * time.Second},
		{"DriveQuotaInterval", c.DriveQuotaInterval, 24 * time.Hour},
		{"CheckInterval", c.CheckInterval, 60 * time.Second},
		{"MaxAge", c.MaxAge, 24 * time.Hour},
		{"PrinterPollInterval", c.PrinterPollInterval, 300 * time.Second},
	}
	for _, tc := range checks {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v", tc.name, tc.got, tc.want)
		}
	}
	if c.ThresholdCount != 60 {
		t.Errorf("ThresholdCount = %d, want 60", c.ThresholdCount)
	}
	if c.UploadBatchMax != 720 {
		t.Errorf("UploadBatchMax = %d, want 720", c.UploadBatchMax)
	}
	if c.Profile != ProfileProduction {
		t.Errorf("Profile = %q, want %q", c.Profile, ProfileProduction)
	}
}

// TestTestingValuesAreShorter 驗證測試值確實縮短時間類參數（可在數分鐘內跑完流程）。
func TestTestingValuesAreShorter(t *testing.T) {
	prod := LoadProfile(ProfileProduction)
	test := LoadProfile(ProfileTesting)

	if test.Profile != ProfileTesting {
		t.Errorf("Profile = %q, want %q", test.Profile, ProfileTesting)
	}
	// 逐項確認測試值 <= 正式值（時間類皆縮短，非時間類至多相等）。
	if test.BindingCodeTTL > prod.BindingCodeTTL {
		t.Errorf("testing BindingCodeTTL %v should be <= prod %v", test.BindingCodeTTL, prod.BindingCodeTTL)
	}
	if test.CheckInterval >= prod.CheckInterval {
		t.Errorf("testing CheckInterval %v should be < prod %v", test.CheckInterval, prod.CheckInterval)
	}
	if test.DriveQuotaInterval >= prod.DriveQuotaInterval {
		t.Errorf("testing DriveQuotaInterval %v should be < prod %v", test.DriveQuotaInterval, prod.DriveQuotaInterval)
	}
	if test.MaxAge >= prod.MaxAge {
		t.Errorf("testing MaxAge %v should be < prod %v", test.MaxAge, prod.MaxAge)
	}
	if test.ThresholdCount >= prod.ThresholdCount {
		t.Errorf("testing ThresholdCount %d should be < prod %d", test.ThresholdCount, prod.ThresholdCount)
	}

	// 測試建議值範圍抽查（v12 §4.4.4）。
	if test.ComputerUsageRecordInterval < 5*time.Second || test.ComputerUsageRecordInterval > 10*time.Second {
		t.Errorf("testing ComputerUsageRecordInterval %v out of suggested 5–10s", test.ComputerUsageRecordInterval)
	}
	if test.DriveQuotaInterval < 1*time.Minute || test.DriveQuotaInterval > 2*time.Minute {
		t.Errorf("testing DriveQuotaInterval %v out of suggested 1–2min", test.DriveQuotaInterval)
	}
}

// TestLoadReadsEnvProfile 驗證 Load() 依環境變數切換 profile。
func TestLoadReadsEnvProfile(t *testing.T) {
	t.Setenv(EnvProfile, string(ProfileTesting))
	if got := Load(); got.Profile != ProfileTesting {
		t.Errorf("with %s=testing, Load().Profile = %q, want %q", EnvProfile, got.Profile, ProfileTesting)
	}

	t.Setenv(EnvProfile, "")
	if got := Load(); got.Profile != ProfileProduction {
		t.Errorf("with %s unset, Load().Profile = %q, want %q", EnvProfile, got.Profile, ProfileProduction)
	}

	t.Setenv(EnvProfile, "garbage")
	if got := Load(); got.Profile != ProfileProduction {
		t.Errorf("with %s=garbage, Load().Profile = %q, want %q (default)", EnvProfile, got.Profile, ProfileProduction)
	}
}

// TestBaseURLDefaultsPerProfile 驗證 BaseURL 依 profile 取對應預設值
// （docs/Eco-Agent_後端串接改動清單.md §1：config／enroll／uploader 共用同一 base URL）。
func TestBaseURLDefaultsPerProfile(t *testing.T) {
	if got := LoadProfile(ProfileProduction).BaseURL; got != prodAPIBaseURL {
		t.Errorf("production BaseURL = %q, want %q", got, prodAPIBaseURL)
	}
	if got := LoadProfile(ProfileTesting).BaseURL; got != testAPIBaseURL {
		t.Errorf("testing BaseURL = %q, want %q", got, testAPIBaseURL)
	}
}

// TestLoadReadsEnvAPIBaseURL 驗證 Load() 以 EnvAPIBaseURL 覆寫 profile 預設 BaseURL；
// LoadProfile 本身維持純粹（不讀環境變數），供測試取得可預期的 profile 預設值。
func TestLoadReadsEnvAPIBaseURL(t *testing.T) {
	t.Setenv(EnvAPIBaseURL, "https://override.example.com")
	if got := Load().BaseURL; got != "https://override.example.com" {
		t.Errorf("with %s set, Load().BaseURL = %q, want override", EnvAPIBaseURL, got)
	}

	t.Setenv(EnvAPIBaseURL, "")
	if got := Load().BaseURL; got != prodAPIBaseURL {
		t.Errorf("with %s unset, Load().BaseURL = %q, want profile default %q", EnvAPIBaseURL, got, prodAPIBaseURL)
	}
}

// TestAPIURL 驗證 base URL 與子路徑組合，含尾端多餘 "/" 的去除與 binding code 路徑轉義。
func TestAPIURL(t *testing.T) {
	c := Config{BaseURL: "https://api.example.com/"}
	if got, want := c.APIURL(PathSensorConfig), "https://api.example.com/api/agent/sensor_config"; got != want {
		t.Errorf("APIURL(PathSensorConfig) = %q, want %q", got, want)
	}
	if got, want := c.APIURL(PathDigitalUsageBatch), "https://api.example.com/api/agent/digital-usage/batch"; got != want {
		t.Errorf("APIURL(PathDigitalUsageBatch) = %q, want %q", got, want)
	}
	if got, want := c.APIURL(PathBindingCodeToken("ab c/1")), "https://api.example.com/api/agent/binding-code/ab%20c%2F1/token"; got != want {
		t.Errorf("APIURL(PathBindingCodeToken) = %q, want %q", got, want)
	}
}
