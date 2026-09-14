package config

import (
	"net/url"
	"strings"
)

// 後端 API 路徑（BaseURL 之下的子路徑）。config／enroll／uploader 三模組共用同一 BaseURL，
// 各自以 APIURL 組出完整端點（見 docs/Eco-Agent_後端串接改動清單.md §1）。
const (
	// PathSensorConfig：集中配置下發（§2）。
	PathSensorConfig = "/api/agent/sensor_config"
	// PathBindingCode：索取一次性綁定碼（§3.1 步驟 1）。
	PathBindingCode = "/api/agent/binding-code"
	// PathTokenRefresh：以 Refresh Token 換發 Access Token（§3.2）。
	PathTokenRefresh = "/api/agent/token/refresh"
	// PathDigitalUsageBatch：批次上傳去識別化資料（§5）。
	PathDigitalUsageBatch = "/api/agent/digital-usage/batch"
)

// PathBindingCodeToken 組出輪詢綁定碼核銷狀態的路徑（§3.1 步驟 3）。
func PathBindingCodeToken(code string) string {
	return PathBindingCode + "/" + url.PathEscape(code) + "/token"
}

// APIURL 組出 BaseURL 與子路徑（見上方 Path* 常數）的完整端點 URL。
// BaseURL 尾端多餘的 "/" 會被去除，避免與子路徑開頭的 "/" 重複。
func (c Config) APIURL(path string) string {
	return strings.TrimRight(c.BaseURL, "/") + path
}
