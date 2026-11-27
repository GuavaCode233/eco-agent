//go:build windows

package platform

import (
	"testing"

	"golang.org/x/sys/windows/registry"
)

// 寫入暫用機碼而非真正的 Run 機碼，測試結束刪除。
func TestAutostartRoundTrip(t *testing.T) {
	orig := runKeyPath
	runKeyPath = `Software\EcoAgentAutostartTest`
	t.Cleanup(func() {
		runKeyPath = orig
		_ = registry.DeleteKey(registry.CURRENT_USER, `Software\EcoAgentAutostartTest`)
	})

	a := windowsAutostart{exe: `C:\Program Files\Eco-Agent\eco-agent.exe`}
	if on, err := a.Enabled(); err == nil && on {
		t.Fatal("enabled before SetEnabled")
	}
	if err := a.SetEnabled(true); err != nil {
		t.Fatal(err)
	}
	if on, err := a.Enabled(); err != nil || !on {
		t.Fatalf("Enabled after enable = %v, %v", on, err)
	}
	// 指向其他路徑（舊版位置）視為未啟用。
	other := windowsAutostart{exe: `D:\old\eco-agent.exe`}
	if on, _ := other.Enabled(); on {
		t.Fatal("entry for another path reported as enabled")
	}
	if err := a.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	if on, _ := a.Enabled(); on {
		t.Fatal("still enabled after disable")
	}
	if err := a.SetEnabled(false); err != nil {
		t.Fatalf("disabling twice: %v", err)
	}
}

func TestAutostartUnsupportedForGoRun(t *testing.T) {
	if !isTempBuild(`C:\Users\x\AppData\Local\Temp\go-build123\b001\exe\eco-agent.exe`) {
		t.Fatal("go run build path not detected")
	}
	if isTempBuild(`C:\Program Files\Eco-Agent\eco-agent.exe`) {
		t.Fatal("installed path detected as temp build")
	}
	if (windowsAutostart{}).Supported() {
		t.Fatal("empty exe should be unsupported")
	}
}
