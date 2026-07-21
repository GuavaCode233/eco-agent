package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseDotEnvLine(t *testing.T) {
	cases := []struct {
		in       string
		key, val string
		ok       bool
	}{
		{`GOOGLE_OAUTH_CLIENT_ID=abc123`, "GOOGLE_OAUTH_CLIENT_ID", "abc123", true},
		{`  KEY = value with spaces `, "KEY", "value with spaces", true},
		{`export FOO=bar`, "FOO", "bar", true},
		{`QUOTED="quoted value"`, "QUOTED", "quoted value", true},
		{`SINGLE='sv'`, "SINGLE", "sv", true},
		{`# a comment`, "", "", false},
		{``, "", "", false},
		{`   `, "", "", false},
		{`no_equals_here`, "", "", false},
		{`=noKey`, "", "", false},
	}
	for _, c := range cases {
		k, v, ok := parseDotEnvLine(c.in)
		if ok != c.ok || k != c.key || v != c.val {
			t.Errorf("parseDotEnvLine(%q) = (%q,%q,%v), want (%q,%q,%v)",
				c.in, k, v, ok, c.key, c.val, c.ok)
		}
	}
}

func TestLoadDotEnv_SetsUnsetOnly(t *testing.T) {
	dir := t.TempDir()
	content := "FROM_DOTENV=dotenv_value\nALREADY_SET=should_not_override\n"
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}

	// 於 tempdir 執行，讓 findDotEnv 找到此 .env。
	t.Chdir(dir)
	t.Setenv("ALREADY_SET", "real_env_wins")

	if p := LoadDotEnv(); p == "" {
		t.Fatal("LoadDotEnv returned empty, expected to find .env")
	}
	if got := os.Getenv("FROM_DOTENV"); got != "dotenv_value" {
		t.Errorf("FROM_DOTENV = %q, want dotenv_value", got)
	}
	if got := os.Getenv("ALREADY_SET"); got != "real_env_wins" {
		t.Errorf("ALREADY_SET = %q, want real_env_wins (真實環境變數優先)", got)
	}
}
