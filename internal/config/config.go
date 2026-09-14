// Package config 提供 Eco-Agent 的集中配置參數（v12 §4.4.4）。
//
// 數值依 profile（正式／測試）先定出本機常數作為預設／fallback（見 v12 §4.4.4 測試調參
// 原則），Load() 開機時再對 5.2 集中配置服務（GET sensor_config）拉取一次覆蓋（見
// sensor_config.go）；拉取失敗（連線失敗、非 200、解析錯誤）則保留本機常數、記 log，
// 不讓 Agent 卡住或崩潰。
//
// TODO(backend): 「每次上傳回應夾帶配置版本號、版本不符再拉取」延至 P2（見
// docs/Eco-Agent_後端串接改動清單.md §2、§6.2）——sensor_config 目前無版本比對機制，
// 現階段固定「開機拉取一次」。
package config

import (
	"log/slog"
	"os"
	"time"
)

// Profile 表示一組配置數值的情境：正式部署或測試觀察。
type Profile string

const (
	// ProfileProduction 為 v12 §4.4.4 定案的正式值。
	ProfileProduction Profile = "production"
	// ProfileTesting 為大幅縮短時間類參數的測試值，供數分鐘內跑完整流程。
	ProfileTesting Profile = "testing"
)

// EnvProfile 是切換 profile 的環境變數名稱（一鍵切換正式／測試）。
const EnvProfile = "ECO_AGENT_PROFILE"

// EnvAPIBaseURL 覆寫後端 base URL 的環境變數；未設定時依 Profile 取預設值
// （見 profiles.go 的 prodAPIBaseURL／testAPIBaseURL）。config／enroll／uploader
// 三模組共用同一 base URL（見 docs/Eco-Agent_後端串接改動清單.md §1）。
const EnvAPIBaseURL = "ECO_AGENT_API_BASE_URL"

// Config 是 Eco-Agent 執行期使用的集中配置。
//
// 欄位分兩組：
//   - 裝置綁定（後端簽發策略）：由後端維護，Agent 端僅作為本機預設／對照。
//   - 資料收集與上傳觸發：由 5.2 集中配置服務下發，Agent 依此運作。
//
// 所有數值來源見 v12 §4.4.4；本 struct 亦攜帶 Profile 供 log／除錯辨識。此 struct 與
// sensor_config 回應（見 sensor_config.go）1:1 對應，Load() 拉取成功時逐欄位覆蓋。
type Config struct {
	// Profile 標示本組數值來自哪個情境（production／testing）。
	Profile Profile

	// BaseURL 是後端 API 的共用 base URL；config／enroll／uploader 三模組各自在此之下
	// 組出完整端點路徑（見 endpoints.go）。預設依 Profile 決定，可用 EnvAPIBaseURL 覆寫。
	BaseURL string

	// --- 裝置綁定（後端簽發策略；§4.4.2 / §4.4.4）---

	// BindingCodeTTL：短效一次性綁定碼效期。
	BindingCodeTTL time.Duration
	// AccessTokenExp：短期上傳憑證效期（不落庫、與 id_token 無關）。
	AccessTokenExp time.Duration
	// RefreshTokenExp：長期換發憑證效期（到期重綁、不輪換）。
	RefreshTokenExp time.Duration

	// --- 資料收集與上傳觸發（Eco-Agent；§4.4.3 / §4.4.4）---

	// ComputerUsageRecordInterval：路徑 A 電腦使用量輪詢區間（短區間）。
	ComputerUsageRecordInterval time.Duration
	// DriveQuotaInterval：路徑 C 雲端儲存查詢區間（長區間）。
	// 注意：非絕對計時器；以持久化時間戳 lastDriveQuotaCheckAt 於 checkInterval
	// 巡檢時做到期判斷觸發（見 v12 §4.4 路徑 C）。此欄位僅為「到期門檻」。
	DriveQuotaInterval time.Duration
	// CheckInterval：本機佇列巡檢區間（達量／maxAge／到期判斷皆掛於此）。
	CheckInterval time.Duration
	// ThresholdCount：累積數量門檻，佇列達此筆數即 flush（主力觸發）。
	ThresholdCount int
	// MaxAge：資料最長滯留時間，最舊一筆超過即 flush（保底觸發）。
	MaxAge time.Duration
	// PrinterPollInterval：路徑 B 印表機輪詢區間（中區間，實測後定值）。
	PrinterPollInterval time.Duration
	// UploadBatchMax：單次上傳批量上限。
	UploadBatchMax int
}

// Load 回傳目前生效的配置：先依環境變數決定的 Profile 取本機常數為預設／fallback，
// 再對 5.2 集中配置服務發一次 GET sensor_config 覆蓋（見 sensor_config.go）；拉取失敗
// （連線失敗、非 200、解析錯誤）時記 log 並沿用本機常數，不讓 Agent 卡住或崩潰
// （docs/Eco-Agent_後端串接改動清單.md §2）。
//
// Profile 由環境變數 ECO_AGENT_PROFILE 決定（"testing" 走測試值，其餘一律走正式值）；
// BaseURL 可另用 ECO_AGENT_API_BASE_URL 覆寫。
//
// TODO(backend): 現在固定「開機拉取一次」；「上傳回應夾帶配置版本號、版本不符再拉取」
// 延至 P2（sensor_config 尚無版本比對機制，見 §6.2）。
func Load() Config {
	cfg := LoadProfile(profileFromEnv())
	if v := os.Getenv(EnvAPIBaseURL); v != "" {
		cfg.BaseURL = v
	}

	resp, err := fetchSensorConfig(cfg.BaseURL)
	if err != nil {
		slog.Default().Warn("config: sensor_config fetch failed, falling back to local constants",
			"profile", cfg.Profile, "base_url", cfg.BaseURL, "err", err)
		return cfg
	}
	applySensorConfig(&cfg, resp)
	return cfg
}

// LoadProfile 依指定 profile 回傳配置，供測試與明確指定情境使用。
func LoadProfile(p Profile) Config {
	switch p {
	case ProfileTesting:
		return testingConfig()
	default:
		return productionConfig()
	}
}

// profileFromEnv 讀取環境變數決定 profile；未設定或無法辨識時預設正式值。
func profileFromEnv() Profile {
	if os.Getenv(EnvProfile) == string(ProfileTesting) {
		return ProfileTesting
	}
	return ProfileProduction
}
