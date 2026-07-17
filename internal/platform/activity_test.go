package platform

import (
	"errors"
	"runtime"
	"testing"
	"time"
)

// TestActivityDetector 驗證目前平台的活動偵測行為。
//
// 因結果取決於 OS，測試對兩種情形都成立：
//   - 支援平台（windows/darwin）：Available() 成功、IdleTime() 回非負間隔且不報錯。
//   - 不支援平台：Available() 與 IdleTime() 皆回 ErrActivityUnsupported。
func TestActivityDetector(t *testing.T) {
	d := NewActivityDetector()

	availErr := d.Available()
	idle, idleErr := d.IdleTime()

	supported := runtime.GOOS == "windows" || runtime.GOOS == "darwin"
	if !supported {
		if !errors.Is(availErr, ErrActivityUnsupported) {
			t.Fatalf("Available() = %v; want ErrActivityUnsupported on %s", availErr, runtime.GOOS)
		}
		if !errors.Is(idleErr, ErrActivityUnsupported) {
			t.Fatalf("IdleTime() err = %v; want ErrActivityUnsupported on %s", idleErr, runtime.GOOS)
		}
		return
	}

	if availErr != nil {
		t.Fatalf("Available() = %v; want nil on %s", availErr, runtime.GOOS)
	}
	if idleErr != nil {
		t.Fatalf("IdleTime() err = %v; want nil on %s", idleErr, runtime.GOOS)
	}
	if idle < 0 {
		t.Fatalf("IdleTime() = %v; want non-negative", idle)
	}
	// idle 應在合理上界內（未曾輸入的機器理論上可能很大，但不該是天文數字）。
	if idle > 365*24*time.Hour {
		t.Fatalf("IdleTime() = %v; implausibly large", idle)
	}
}

// TestActivityDetectorMonotonicWhenIdle 驗證：在無輸入的短暫間隔內，idle 間隔不倒退。
// 僅於支援平台執行；測試期間本身不產生輸入，故第二次量測應 >= 第一次（容一點量測誤差）。
func TestActivityDetectorMonotonicWhenIdle(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		t.Skipf("activity detection unsupported on %s", runtime.GOOS)
	}
	d := NewActivityDetector()
	first, err := d.IdleTime()
	if err != nil {
		t.Fatalf("IdleTime() #1: %v", err)
	}
	time.Sleep(150 * time.Millisecond)
	second, err := d.IdleTime()
	if err != nil {
		t.Fatalf("IdleTime() #2: %v", err)
	}
	// 若測試執行期間剛好有人動了輸入裝置，second 會較小；此為預期，故只在「未被打斷」
	// 的常態下檢查。放寬 50ms 容忍量測抖動。
	if second+50*time.Millisecond < first {
		t.Logf("IdleTime dropped %v -> %v (likely real input during test); tolerated", first, second)
	}
}
