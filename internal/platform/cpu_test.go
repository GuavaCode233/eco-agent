package platform

import (
	"sync"
	"testing"
	"time"
)

// TestCPUSampler 驗證跨平台 CPU 取樣的基本契約：可用、使用率落在 0–100、型號可讀。
//
// gopsutil 於 Windows/macOS/Linux 皆支援，故不像活動偵測需分平台分支；此處對所有
// 執行測試的平台皆期望成功。
func TestCPUSampler(t *testing.T) {
	s := NewCPUSampler()

	if err := s.Available(); err != nil {
		t.Fatalf("Available() = %v; want nil", err)
	}

	// 間隔一小段時間再取，讓差分視窗有實際 CPU time 可計算。
	time.Sleep(100 * time.Millisecond)
	pct, err := s.Percent()
	if err != nil {
		t.Fatalf("Percent() err = %v; want nil", err)
	}
	if pct < 0 || pct > 100 {
		t.Fatalf("Percent() = %v; want within [0,100]", pct)
	}

	// Model 於部分虛擬機可能為空字串，但不應報錯。
	if _, err := s.Model(); err != nil {
		t.Fatalf("Model() err = %v; want nil", err)
	}
}

// TestCPUSamplerModelCached 驗證 Model 多次呼叫回傳一致（快取一次）。
func TestCPUSamplerModelCached(t *testing.T) {
	s := NewCPUSampler()
	first, err := s.Model()
	if err != nil {
		t.Fatalf("Model() #1: %v", err)
	}
	second, err := s.Model()
	if err != nil {
		t.Fatalf("Model() #2: %v", err)
	}
	if first != second {
		t.Fatalf("Model() not stable: %q != %q", first, second)
	}
}

// TestCPUSamplerConcurrent 驗證並行呼叫 Percent 不觸發 data race（-race 下有效）。
// gopsutil 的差分基準為套件級全域，本型別以 mu 保護呼叫序列。
func TestCPUSamplerConcurrent(t *testing.T) {
	s := NewCPUSampler()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				if _, err := s.Percent(); err != nil {
					t.Errorf("Percent() concurrent: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()
}
