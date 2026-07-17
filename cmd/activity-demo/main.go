// Command activity-demo 是 Step 1.1 活動偵測層的獨立驗證（CLAUDE.md Step 1.1）。
//
// 每秒印出「距上次輸入的間隔」。手動驗證方式：
//   - 持續操作鍵盤/滑鼠 → idle 應接近 0 並不斷歸零；
//   - 放手不動 → idle 應隨秒數穩定遞增；
//   - 再動一下 → idle 立刻回落。
// 於不支援平台（非 Windows/macOS）會印出 ErrActivityUnsupported 並結束（優雅降級）。
//
// 執行：go run ./cmd/activity-demo   （Ctrl+C 結束）
package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"time"

	"eco-agent/internal/platform"
)

func main() {
	d := platform.NewActivityDetector()

	if err := d.Available(); err != nil {
		if errors.Is(err, platform.ErrActivityUnsupported) {
			fmt.Fprintf(os.Stderr, "此平台未提供活動偵測（優雅降級）：%v\n", err)
			os.Exit(0)
		}
		fmt.Fprintf(os.Stderr, "活動偵測不可用：%v\n", err)
		os.Exit(1)
	}

	fmt.Println("=== Eco-Agent Step 1.1 活動偵測 demo ===")
	fmt.Println("操作/閒置鍵鼠，觀察 idle 間隔歸零與遞增。Ctrl+C 結束。")

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt)

	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			fmt.Println("\n結束。")
			return
		case <-ticker.C:
			idle, err := d.IdleTime()
			if err != nil {
				fmt.Fprintf(os.Stderr, "IdleTime 失敗：%v\n", err)
				continue
			}
			fmt.Printf("距上次輸入：%6.1f 秒\n", idle.Seconds())
		}
	}
}
