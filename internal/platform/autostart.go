package platform

// Autostart 管理「登入時自動啟動 Eco-Agent」（docs/Eco-Agent_系統匣UI規劃.md U4.4）。
type Autostart interface {
	// Supported 回報目前環境可否設定開機自啟；不支援時系統匣不顯示此選項。
	Supported() bool
	// Enabled 回報是否已設定開機自啟（且指向目前的執行檔）。
	Enabled() (bool, error)
	// SetEnabled 設定或取消開機自啟。
	SetEnabled(on bool) error
}

// NewAutostart 回傳目前平台的開機自啟實作。
func NewAutostart() Autostart { return newAutostart() }
