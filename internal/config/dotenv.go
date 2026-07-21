// 本檔提供極簡的 .env 載入器（開發便利用）。
//
// 動機：Go 程式只讀作業系統環境變數，不會自動讀 .env 檔。為讓 `go run ./cmd/...` 於
// 開發時能直接沿用專案根目錄的 .env（§6 Google OAuth 憑證等），提供 LoadDotEnv 於各
// demo/進入點啟動時呼叫。
//
// 語意（對齊常見 dotenv 慣例）：
//   - 僅設定「尚未存在」的變數——真實環境變數永遠優先，正式部署不受 .env 影響。
//   - 找不到 .env 視為無操作、不報錯（正式部署本就不依賴 .env 檔）。
//
// 為維持零相依，這裡自行解析而不引入 godotenv。僅支援 KEY=VALUE、# 註解、空行、
// 選用的 export 前綴與成對引號，足以涵蓋本專案 .env.example 的格式。
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// LoadDotEnv 從工作目錄起向上尋找 .env（最多回溯數層以涵蓋 `go run` 於子目錄執行的情形），
// 找到第一個即載入其中尚未設定的變數。回傳實際載入的檔案路徑；未找到回空字串。
func LoadDotEnv() string {
	path, ok := findDotEnv()
	if !ok {
		return ""
	}
	if err := loadDotEnvFile(path); err != nil {
		return ""
	}
	return path
}

// findDotEnv 由 cwd 向上回溯尋找 .env。
func findDotEnv() (string, bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	// 回溯上限 6 層：足以從 cmd/<x> 或深層測試目錄找回專案根，又不致漫遊整個檔案系統。
	for i := 0; i < 6; i++ {
		p := filepath.Join(dir, ".env")
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break // 已到根
		}
		dir = parent
	}
	return "", false
}

// loadDotEnvFile 解析 .env 並把尚未存在的變數設入行程環境。
func loadDotEnvFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, val, ok := parseDotEnvLine(sc.Text())
		if !ok {
			continue
		}
		if _, exists := os.LookupEnv(key); exists {
			continue // 真實環境變數優先，不覆寫
		}
		_ = os.Setenv(key, val)
	}
	return sc.Err()
}

// parseDotEnvLine 解析單行；非 KEY=VALUE（空行、註解、無 =）回 ok=false。
func parseDotEnvLine(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	if line == "" || strings.HasPrefix(line, "#") {
		return "", "", false
	}
	line = strings.TrimPrefix(line, "export ")
	i := strings.IndexByte(line, '=')
	if i <= 0 {
		return "", "", false
	}
	key = strings.TrimSpace(line[:i])
	val = strings.TrimSpace(line[i+1:])
	val = trimMatchingQuotes(val)
	return key, val, key != ""
}

// trimMatchingQuotes 去除成對的單/雙引號（若有）。
func trimMatchingQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}
