// 本檔提供跨平台「CPU 使用率取樣」抽象（CLAUDE.md Step 1.2）。
//
// 職責：回傳「自上次取樣以來的平均 CPU 使用率」（0–100%），以及 CPU 型號名稱。
// 路徑 A（電腦使用，Step 1.3）於每個輪詢週期取一次，併入 active/idle 分態後記平均
// 使用率；本層只提供原始使用率，不做分態、不算能耗（純感測，v13 [D7]）。
//
// 與活動偵測（activity.go）不同，CPU 使用率透過 gopsutil 取得，gopsutil 已在其內部
// 封裝 Windows／macOS／Linux 差異，故本檔為單一跨平台實作、無需 build tag 分檔，且
// 免特殊權限（CLAUDE.md §1.2）。上層（sensors/computer）僅依賴 CPUSampler 介面。
package platform

import (
	"fmt"
	"strings"
	"sync"

	"github.com/shirou/gopsutil/v4/cpu"
)

// CPUSampler 抽象「跨平台 CPU 使用率取樣」。並行安全：Percent 以互斥鎖保護 gopsutil
// 的差分狀態（見下方實作說明）。
type CPUSampler interface {
	// Percent 回傳「自上次呼叫 Percent 以來」的平均 CPU 使用率（0–100，全核心彙總）。
	//
	// 採非阻塞差分：本次呼叫與上次呼叫之間的 CPU time 差分換算使用率。因此首次呼叫
	// （建構後尚無前次基準）語意不穩定，建構時已先行 prime 一次；上層應以輪詢區間
	// （如 60 秒）為間隔規律呼叫，取得該區間的平均使用率。
	Percent() (float64, error)

	// Model 回傳 CPU 型號名稱（供 payload cpu_model）。執行期不變，故快取一次。
	Model() (string, error)

	// Available 於啟動時檢查 CPU 取樣可用；可用回 nil，否則回導引用錯誤。
	Available() error
}

// gopsutilCPUSampler 以 gopsutil 實作 CPUSampler。
//
// gopsutil 的 cpu.Percent(0, false) 為非阻塞模式：以「本次呼叫」與「套件內記錄的上次
// 呼叫」之間的 CPU time 差分計算使用率。此差分狀態是 gopsutil 套件級全域，本 Agent
// 為唯一呼叫者，故以本型別的 mu 保護呼叫序列，避免並行呼叫互相污染基準。
type gopsutilCPUSampler struct {
	mu sync.Mutex

	modelOnce sync.Once
	model     string
	modelErr  error
}

// NewCPUSampler 回傳 gopsutil 版 CPU 取樣器，並先 prime 一次差分基準。
//
// prime 目的：讓建構後「第一次」Percent 有合理的前次基準，量測到的是「建構→首呼」
// 這段區間的使用率，而非「開機至今」的長期平均（後者對即時感測無意義）。
func NewCPUSampler() CPUSampler {
	s := &gopsutilCPUSampler{}
	// 忽略 prime 的回傳與錯誤：僅為建立差分基準；真正取值由後續 Percent 負責。
	_, _ = cpu.Percent(0, false)
	return s
}

// Percent 實作 CPUSampler。
func (s *gopsutilCPUSampler) Percent() (float64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// interval=0：非阻塞，與上次呼叫差分；percpu=false：回傳單一全核心彙總值。
	vals, err := cpu.Percent(0, false)
	if err != nil {
		return 0, fmt.Errorf("platform: cpu.Percent: %w", err)
	}
	if len(vals) == 0 {
		return 0, fmt.Errorf("platform: cpu.Percent returned no samples")
	}
	return vals[0], nil
}

// Model 實作 CPUSampler；型號執行期不變，僅讀取並快取一次。
func (s *gopsutilCPUSampler) Model() (string, error) {
	s.modelOnce.Do(func() {
		infos, err := cpu.Info()
		if err != nil {
			s.modelErr = fmt.Errorf("platform: cpu.Info: %w", err)
			return
		}
		for _, info := range infos {
			if name := strings.TrimSpace(info.ModelName); name != "" {
				s.model = name
				return
			}
		}
		// 有些平台/虛擬機不填 ModelName；非致命，回空字串不報錯，交由上層決定是否略過欄位。
		s.model = ""
	})
	return s.model, s.modelErr
}

// Available 實作 CPUSampler：試取一次使用率，驗證 gopsutil 於本機可用。
func (s *gopsutilCPUSampler) Available() error {
	if _, err := s.Percent(); err != nil {
		return fmt.Errorf("platform: CPU sampling unavailable: %w", err)
	}
	return nil
}

// 確保 gopsutilCPUSampler 滿足 CPUSampler 介面。
var _ CPUSampler = (*gopsutilCPUSampler)(nil)
