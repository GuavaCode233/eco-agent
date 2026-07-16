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
