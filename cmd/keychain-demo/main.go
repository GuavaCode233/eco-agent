// Command keychain-demo 是 §4 真實 Keychain 實作的獨立驗證
// （docs/Eco-Agent_後端串接改動清單.md §4；platform.NewOSKeychain）。
//
// 對「這台機器上真實的系統金鑰庫」（Windows DPAPI／macOS Keychain Services，其餘平台
// fallback 記憶體並印警告）寫入一筆假 Refresh Token、讀回比對、再刪除確認冪等，
// 全程不使用 MemoryKeychain（demo 系列其餘指令為求可重複執行仍用 MemoryKeychain，見
// eco-agent-demo 等）。
//
// 手動驗證方式：
//   - 跑一次：應印出 Set/Get 一致、Delete 後 Get 回 ErrKeychainNotFound。
//   - Windows：可額外到 %LOCALAPPDATA%\eco-agent\credentials.dat 確認內容非明文。
//   - macOS：可額外用「鑰匙圈存取」App 或 `security find-generic-password -s eco-agent`
//     確認項目短暫存在（Delete 後應查不到）。
//   - 重跑第二次：因程式已 Delete 乾淨，行為應與第一次相同（非累積狀態）。
//
// 執行：go run ./cmd/keychain-demo
package main

import (
	"errors"
	"fmt"
	"os"

	"eco-agent/internal/platform"
)

const demoKey = "eco-agent.keychain-demo"

func main() {
	kc := platform.NewOSKeychain()
	fmt.Println("=== Eco-Agent §4 真實 Keychain demo ===")

	const want = "demo-refresh-token-0001"
	fmt.Printf("Set(%q, %q) ...\n", demoKey, want)
	if err := kc.Set(demoKey, want); err != nil {
		fatal("Set", err)
	}

	got, err := kc.Get(demoKey)
	if err != nil {
		fatal("Get", err)
	}
	if got != want {
		fatal("Get", fmt.Errorf("回傳 %q，期望 %q", got, want))
	}
	fmt.Printf("Get 回傳一致：%q\n", got)

	if err := kc.Delete(demoKey); err != nil {
		fatal("Delete", err)
	}
	fmt.Println("Delete 完成")

	if _, err := kc.Get(demoKey); !errors.Is(err, platform.ErrKeychainNotFound) {
		fatal("Get after Delete", fmt.Errorf("= %v，期望 ErrKeychainNotFound", err))
	}
	fmt.Println("Delete 後 Get 正確回 ErrKeychainNotFound（冪等確認）")

	// 二次 Delete 應仍成功（冪等）。
	if err := kc.Delete(demoKey); err != nil {
		fatal("Delete (2nd)", err)
	}
	fmt.Println("\n=== demo 結束：Set/Get/Delete 往返與冪等皆正確 ===")
}

func fatal(step string, err error) {
	fmt.Fprintf(os.Stderr, "demo failed at %s: %v\n", step, err)
	os.Exit(1)
}
