// Command printer-demo 是路徑 B（印表機）Step 3.1 的獨立驗證：以 SNMP v2c 查
// prtMarkerLifeCount（OID 1.3.6.1.2.1.43.10.2.1.4）取累計頁數、查序號 OID 取
// printer_serial，兩者原樣印出（v0.23 [D15]：差分與歸鍵防呆皆移至後端，Agent 不在本機
// 相減，也不判斷 counter 是否重置）。
//
// 用法：
//
//	go run ./cmd/printer-demo -mock       # 對本機 mock SNMP responder 輪詢（免真實印表機）
//	go run ./cmd/printer-demo             # 對 ECO_AGENT_PRINTER_HOST 指定的真實印表機輪詢
//
// 前置（真實印表機，見 .env.example / README）：
//
//	export ECO_AGENT_PRINTER_HOST=192.168.1.50   # 必填；未設即視為本機無個人專屬印表機
//	export ECO_AGENT_PRINTER_COMMUNITY=public    # 可選，預設 public
//	export ECO_AGENT_PRINTER_PORT=161            # 可選，預設 161
//	export ECO_AGENT_PRINTER_OID=...             # 可選，index 非 1.1 的機種可覆寫
//	export ECO_AGENT_PRINTER_SERIAL_OID=...      # 可選，指定單一序號 OID，略過三候選 fallback 鏈
//
// 未設 ECO_AGENT_PRINTER_HOST 時優雅降級：印出指引、結束，不崩潰（§2 Step 3.4）。
//
// 注意：本 demo 僅驗證 3.1「SNMP 取值」，以固定間隔連續輪詢數次觀察。正式的輪詢觸發
// （3.2）將沿用 Step 2 的持久化時間戳到期判斷（lastPrinterPollAt，同掛 checkInterval），
// 不是這裡的固定迴圈；入列與送出屬 3.3，尚未實作（見 cmd/printer-sensor-demo）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"time"

	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/platform"
	"eco-agent/internal/queue"
	"eco-agent/internal/sensors/printer"
)

func main() {
	mock := flag.Bool("mock", false, "改對本機 mock SNMP responder 輪詢（免真實印表機）")
	polls := flag.Int("polls", 3, "輪詢次數")
	interval := flag.Duration("interval", 5*time.Second, "輪詢間隔（僅本 demo 用；正式為 3.2 時間戳到期判斷）")
	flag.Parse()

	// 開發便利：載入專案根目錄 .env（若存在）到環境變數。真實環境變數優先。
	if p := config.LoadDotEnv(); p != "" {
		fmt.Printf("（已載入 %s）\n", p)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Println("=== 路徑 B Step 3.1：SNMP 取 page counter 與序號 ===")

	client, agent, err := buildClient(*mock)
	if err != nil {
		if errors.Is(err, printer.ErrNotConfigured) {
			degradeHint(err)
			return
		}
		fmt.Fprintf(os.Stderr, "建立 SNMP 客戶端失敗：%v\n", err)
		os.Exit(1)
	}
	if agent != nil {
		defer agent.Close()
	}

	// device_uuid（§4.4.2）與憑證同存一份本機佇列；本 demo 只驗證 SNMP 讀值，隨手開一個
	// 臨時佇列即可，不涉及真實的上傳/觸發（見 cmd/printer-sensor-demo）。
	dir, err := os.MkdirTemp("", "printer-demo-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "mkdir temp 失敗：%v\n", err)
		os.Exit(1)
	}
	defer os.RemoveAll(dir)
	q, err := queue.Open(ctx, filepath.Join(dir, "queue.db"))
	if err != nil {
		fmt.Fprintf(os.Stderr, "queue.Open 失敗：%v\n", err)
		os.Exit(1)
	}
	defer q.Close()

	// 歸戶對象：個人專屬印表機與員工一對一，讀數直接歸給本機綁定的 ID Token。
	// MOCK: enroll 現階段回傳固定假 token（§5），去識別化語意不變——只帶 token，不帶姓名/Email。
	enr := enroll.New(platform.NewMemoryKeychain(), q)
	if err := enr.EnsureBound(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "EnsureBound 失敗：%v\n", err)
		os.Exit(1)
	}
	idToken, err := enr.IDToken()
	if err != nil {
		fmt.Fprintf(os.Stderr, "取 ID Token 失敗：%v\n", err)
		os.Exit(1)
	}

	addr, oid := client.Target()
	fmt.Printf("目標：%s　page counter OID：%s\n", addr, oid)
	fmt.Printf("歸戶 ID Token：%s\n\n", idToken)

	run(ctx, client, agent, *polls, *interval)
}

// buildClient 依 -mock 決定對本機假代理或真實印表機查詢。
func buildClient(mock bool) (*printer.SNMPClient, *printer.MockAgent, error) {
	if !mock {
		c, err := printer.NewSNMPClientFromEnv()
		return c, nil, err
	}
	// MOCK: 本機 SNMP responder，起始累計值模擬一台已印過 1000 頁的印表機。
	agent, err := printer.StartMockAgent("127.0.0.1:0", printer.DefaultCommunity,
		map[string]uint64{printer.DefaultPageCounterOID: 1000})
	if err != nil {
		return nil, nil, err
	}
	agent.SetString(printer.DefaultSerialPrtGeneralOID, "DEMO-MOCK-PRINTER-SN")
	host, port := agent.Addr()
	c, err := printer.NewSNMPClient(host, printer.WithPort(port))
	if err != nil {
		agent.Close()
		return nil, nil, err
	}
	fmt.Println("（-mock：已啟動本機 mock SNMP responder，每次輪詢間自動 +3 頁）")
	return c, agent, nil
}

// run 連續輪詢並印出每次的累計值與序號（皆原樣讀出，不在本機做任何換算，[D15]）。
func run(ctx context.Context, c *printer.SNMPClient, agent *printer.MockAgent, polls int, interval time.Duration) {
	counter := uint64(1000)

	for i := 1; i <= polls; i++ {
		cur, err := c.PageCounter(ctx)
		if err != nil {
			if errors.Is(err, printer.ErrNoPageCounter) {
				fmt.Printf("#%d 目標有回應但無可用 page counter：%v\n", i, err)
			} else {
				// 3.4：不通則記 log、跳過，不使 Agent 卡住；下次輪詢自然重試。
				fmt.Printf("#%d 查詢失敗（跳過，下次輪詢重試）：%v\n", i, err)
			}
		} else {
			serial, serr := c.SerialNumber(ctx)
			if serr != nil {
				fmt.Printf("#%d 累計頁數 = %d，序號查無（%v；[D14] 將回退以 device_id 歸鍵並標記）\n", i, cur, serr)
			} else {
				fmt.Printf("#%d 累計頁數 = %d，序號 = %s\n", i, cur, serial)
			}
		}

		if i == polls {
			break
		}
		if agent != nil { // mock：模擬期間列印 3 頁
			counter += 3
			agent.SetValue(printer.DefaultPageCounterOID, counter)
		}
		select {
		case <-ctx.Done():
			fmt.Println("\n已中止。")
			return
		case <-time.After(interval):
		}
	}

	fmt.Println("\n（正式流程以 payload {usage_date, printer_page_counter, printer_serial} 原樣入列、走 HTTPS 送出；")
	fmt.Println("  頁數差分與 counter 重置防呆、能耗換算（頁數 × 紙張生命週期係數）皆由後端計算，[D15]。）")
}

// degradeHint 印出未設定目標印表機時的優雅降級指引（§2 Step 3.4）。
func degradeHint(err error) {
	fmt.Printf("\n路徑 B 已跳過（優雅降級）：%v\n", err)
	fmt.Println("請設定下列環境變數後再試（見 .env.example / README）：")
	fmt.Printf("  %s（必填，印表機 IP 或主機名）\n", printer.EnvHost)
	fmt.Printf("  %s、%s、%s、%s（可選）\n", printer.EnvCommunity, printer.EnvPort, printer.EnvOID, printer.EnvSerialOID)
	fmt.Println("\n或免真實印表機，改跑本機 mock SNMP responder：")
	fmt.Println("  go run ./cmd/printer-demo -mock")
}
