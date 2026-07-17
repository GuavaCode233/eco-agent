package platform

import (
	"errors"
	"testing"
)

// TestPowerSamplerUnsupported 驗證預設即時功耗量測為「不支援」（Step 1.5 結構預留）：
// Available 回 ErrPowerUnsupported、PowerWatts 回 ok=false，呼叫端據此回退加權模型。
func TestPowerSamplerUnsupported(t *testing.T) {
	p := NewPowerSampler()

	if err := p.Available(); !errors.Is(err, ErrPowerUnsupported) {
		t.Fatalf("Available() = %v; want ErrPowerUnsupported", err)
	}

	w, ok, err := p.PowerWatts()
	if err != nil {
		t.Fatalf("PowerWatts() err = %v; want nil", err)
	}
	if ok {
		t.Fatalf("PowerWatts() ok = true; want false (unsupported)")
	}
	if w != 0 {
		t.Fatalf("PowerWatts() watts = %v; want 0 when unsupported", w)
	}
}
