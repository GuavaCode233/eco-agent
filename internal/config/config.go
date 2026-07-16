// Package config 提供 Eco-Agent 的集中配置參數（v12 §4.4.4）。
//
// 現階段所有數值以「常數」實作，並依 profile（正式／測試）切換，供在數分鐘內
// 觀察完整流程（見 v12 §4.4.4 測試調參原則）。
//
// TODO(backend): 這些參數最終由 5.2 集中配置服務（sensor_config）統一管理與下發。
// Load() 目前僅回傳本機常數，日後改為「開機拉取一次 + 每次上傳回應夾帶配置版本號、
// 版本不符再拉取」的 HTTP 流程（見 v12 §5.2）。屆時本檔常數退居為「拉取失敗時的
// 內建預設值」。
package config

import (
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

// Config 是 Eco-Agent 執行期使用的集中配置。
//
// 欄位分兩組：
//   - 裝置綁定（後端簽發策略）：由後端維護，Agent 端僅作為本機預設／對照。
//   - 資料收集與上傳觸發：由 5.2 集中配置服務下發，Agent 依此運作。
//
// 所有數值來源見 v12 §4.4.4；本 struct 亦攜帶 Profile 供 log／除錯辨識。
type Config struct {
	// Profile 標示本組數值來自哪個情境（production／testing）。
	Profile Profile

	// --- 裝置綁定（後端簽發策略；§4.4.2 / §4.4.4）---

	// BindingCodeTTL：短效一次性綁定碼效期。TODO(backend): 由後端簽發策略維護。
	BindingCodeTTL time.Duration
	// AccessTokenExp：短期上傳憑證效期（不落庫、與 id_token 無關）。TODO(backend)。
	AccessTokenExp time.Duration
	// RefreshTokenExp：長期換發憑證效期（到期重走綁定，不輪換）。TODO(backend)。
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
	// TODO(backend): 正式值待實測；暫定 300 秒。
	PrinterPollInterval time.Duration
	// UploadBatchMax：單次上傳批量上限。
	UploadBatchMax int
}

// Load 回傳目前生效的配置。
//
// Profile 由環境變數 ECO_AGENT_PROFILE 決定（"testing" 走測試值，其餘一律走正式值）。
//
// TODO(backend): 目前僅回本機常數。後端就緒後，這裡改為向 5.2 集中配置服務拉取
// （HTTP GET sensor_config）並以版本號比對更新；拉取失敗時 fallback 到本檔常數，
// 並保留上一版配置（見 v12 §7「開機是否強制拉取配置」待釐清項）。
func Load() Config {
	return LoadProfile(profileFromEnv())
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
