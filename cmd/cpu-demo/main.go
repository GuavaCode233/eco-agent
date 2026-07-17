// Command cpu-demo 是 Step 1.2 CPU 使用率取樣層的獨立驗證（CLAUDE.md Step 1.2）。
//
// 先印出 CPU 型號（payload cpu_model 來源），再每秒印出「自上次取樣以來的平均 CPU
// 使用率」。手動驗證方式：
//   - 閒置電腦 → 使用率應偏低並趨於平穩；
//   - 施加負載（如跑一段忙碌迴圈、開重的程式）→ 使用率應明顯上升；
//   - 負載結束 → 使用率回落。
// gopsutil 於 Windows/macOS/Linux 皆支援，故各平台皆可執行。
//
// 執行：go run ./cmd/cpu-demo   （Ctrl+C 結束）
package main

import (
	"fmt"
	"os"
	"os/signal"
	"time"

	"eco-agent/internal/platform"
)

func main() {
	s := platform.NewCPUSampler()

	if err := s.Available(); err != nil {
		fmt.Fprintf(os.Stderr, "CPU 取樣不可用：%v\n", err)
		os.Exit(1)
	}

	model, err := s.Model()
	if err != nil {
		fmt.Fprintf(os.Stderr, "讀取 CPU 型號失敗：%v\n", err)
	} else if model == "" {
		fmt.Println("CPU 型號：(此平台未提供，將略過 cpu_model 欄位)")
	} else {
		fmt.Printf("CPU 型號：%s\n", model)
	}

	fmt.Println("=== Eco-Agent Step 1.2 CPU 使用率 demo ===")
	fmt.Println("閒置/施加負載，觀察平均使用率升降。Ctrl+C 結束。")

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
			pct, err := s.Percent()
			if err != nil {
				fmt.Fprintf(os.Stderr, "Percent 失敗：%v\n", err)
				continue
			}
			fmt.Printf("平均 CPU 使用率：%5.1f %%\n", pct)
		}
	}
}
