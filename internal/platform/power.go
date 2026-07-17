// 本檔為「即時功耗量測」抽象（CLAUDE.md Step 1.5）——**結構預留、現階段不實作**。
//
// 動機：使用率加權模型（P_idle + 使用率×(P_active−P_idle)，v13 [D7]）以型號查表估功耗；
// 若能取得「即時實測功耗」可作為精度增強的覆蓋層（overlay），把估算換成量測。
//
// 為何現階段不實作（且預設回不支援）：
//   - Intel/AMD RAPL：Linux 走 /sys/class/powercap（intel-rapl）或 MSR，需 root／
//     CAP_SYS_RAWIO；Windows 無對等公開介面；虛擬機／容器多不可讀。
//   - Apple Silicon：`powermetrics` 需 sudo，且僅 macOS；無穩定公開 API。
//   - BYOD 現實：多數員工自帶裝置無管理者權限，privileged 量測不可行；跨平台缺口大。
// 因此本層預設 unsupportedPowerSampler（一律回 ErrPowerUnsupported / ok=false），Agent
// 回退到使用率加權模型；日後有權限環境（如公司配發機）再補平台實作即可，介面不變。
//
// TODO(backend): 即時功耗覆蓋（RAPL/powermetrics）作為精度增強——補上
// power_linux.go（intel-rapl／amd_energy）、power_darwin.go（powermetrics）等 build tag
// 分平台實作，並在後端能耗模型中以「有實測則優先、否則回退加權估算」整合。
package platform

import "errors"

// ErrPowerUnsupported 表示本機未提供（或無權限進行）即時功耗量測。
var ErrPowerUnsupported = errors.New("platform: real-time power sampling unsupported on this host")

// PowerSampler 抽象「即時整機／封裝功耗量測」。作為使用率加權模型的精度增強覆蓋層；
// 不支援時 Available 回 ErrPowerUnsupported、PowerWatts 回 ok=false，呼叫端據此回退估算。
type PowerSampler interface {
	// Available 於啟動時檢查即時功耗量測是否可用（權限、平台介面）。
	// 現階段一律回 ErrPowerUnsupported（見套件註解）。
	Available() error

	// PowerWatts 回傳當前功耗（瓦）。ok=false 表示本機不支援量測（回退加權模型）；
	// 有平台實作後才回 ok=true 與實測值。
	PowerWatts() (watts float64, ok bool, err error)
}

// unsupportedPowerSampler 為預設「不支援」實作（結構預留、不做真實量測）。
type unsupportedPowerSampler struct{}

// NewPowerSampler 回傳目前平台的即時功耗量測器。
//
// 現階段一律回不支援實作（見套件註解）；日後補 build tag 分平台實作後，改由各平台
// 檔提供 NewPowerSampler，介面與呼叫端不變。
func NewPowerSampler() PowerSampler { return unsupportedPowerSampler{} }

// Available 回 ErrPowerUnsupported。
func (unsupportedPowerSampler) Available() error { return ErrPowerUnsupported }

// PowerWatts 回 ok=false（不支援量測）。
func (unsupportedPowerSampler) PowerWatts() (float64, bool, error) { return 0, false, nil }

// 確保 unsupportedPowerSampler 滿足 PowerSampler 介面。
var _ PowerSampler = unsupportedPowerSampler{}
