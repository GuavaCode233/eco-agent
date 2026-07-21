// Command drive-demo 是路徑 C（雲端儲存）Step 2.1 的獨立驗證：真串 Google Drive API v3
// 取 about?fields=storageQuota。
//
// 用法：
//
//	go run ./cmd/drive-demo authorize   # 首次一次性 OAuth 授權，取得並保存 refresh token
//	go run ./cmd/drive-demo             # 讀 token → 取儲存用量並印出
//
// 前置（§6，勿硬編碼、勿提交版控）：
//
//	export GOOGLE_OAUTH_CLIENT_ID=...       # OAuth 2.0 用戶端 ID
//	export GOOGLE_OAUTH_CLIENT_SECRET=...   # OAuth 2.0 用戶端密鑰
//	export GOOGLE_OAUTH_TOKEN_PATH=...      # （可選）refresh token 落地路徑
//
// 未設憑證或尚未授權時優雅降級：印出指引、結束，不崩潰（對齊 §6 路徑 C 降級要求）。
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"

	"eco-agent/internal/config"
	"eco-agent/internal/sensors/drive"
)

func main() {
	// 開發便利：載入專案根目錄 .env（若存在）到環境變數，讓 go run 直接沿用 §6 憑證。
	// 真實環境變數優先；找不到 .env 視為無操作。
	if p := config.LoadDotEnv(); p != "" {
		fmt.Printf("（已載入 %s）\n", p)
	}

	if len(os.Args) > 1 && os.Args[1] == "authorize" {
		runAuthorize()
		return
	}
	runFetch()
}

// runAuthorize 執行一次性 OAuth 授權並保存 refresh token。
func runAuthorize() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	fmt.Println("=== 路徑 C Step 2.1：Google Drive OAuth 一次性授權 ===")
	path, err := drive.Authorize(ctx, os.Stdout)
	if err != nil {
		if errors.Is(err, drive.ErrCredentialsNotConfigured) {
			degradeHint(err)
			return
		}
		fmt.Fprintf(os.Stderr, "授權失敗：%v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✓ 授權完成，refresh token 已保存至：%s\n", path)
	fmt.Println("  之後可執行 `go run ./cmd/drive-demo` 取用量。")
}

// runFetch 讀 token → 取 storageQuota → 印出。
func runFetch() {
	ctx := context.Background()

	fmt.Println("=== 路徑 C Step 2.1：真串 Google Drive API 取 storageQuota ===")
	hc, err := drive.NewClient(ctx)
	if err != nil {
		if errors.Is(err, drive.ErrCredentialsNotConfigured) {
			degradeHint(err)
			return
		}
		if errors.Is(err, drive.ErrNotAuthorized) {
			fmt.Println("尚未授權。請先執行一次性授權：")
			fmt.Println("  go run ./cmd/drive-demo authorize")
			return
		}
		fmt.Fprintf(os.Stderr, "建立 client 失敗：%v\n", err)
		os.Exit(1)
	}

	q, err := drive.NewAPIClient(hc).StorageQuota(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "取 storageQuota 失敗：%v\n", err)
		os.Exit(1)
	}

	fmt.Printf("\n儲存用量（真串 Drive API v3 回傳）：\n")
	fmt.Printf("  帳號總用量 usage           = %d bytes（%.3f GB）\n", q.Usage, q.UsageGB())
	fmt.Printf("  Drive 內容 usageInDrive    = %d bytes（%.3f GB）\n", q.UsageInDrive, q.UsageInDriveGB())
	fmt.Printf("  Drive 垃圾桶 usageInTrash  = %d bytes\n", q.UsageInDriveTrash)
	if q.Limit > 0 {
		fmt.Printf("  配額上限 limit             = %d bytes（%.3f GB）\n", q.Limit, float64(q.Limit)/1e9)
	} else {
		fmt.Printf("  配額上限 limit             = 無上限（unlimited）\n")
	}
	fmt.Println("\n（2.5 將以 drive_usage_gb 換算能耗並走 HTTPS 送出；本步僅驗證真串取量。）")
}

// degradeHint 印出憑證未設定時的優雅降級指引（§6）。
func degradeHint(err error) {
	fmt.Printf("\n路徑 C 已跳過（優雅降級）：%v\n", err)
	fmt.Println("請設定下列環境變數後再試（見 .env.example / README，勿提交版控）：")
	fmt.Printf("  %s、%s（必填）\n", drive.EnvClientID, drive.EnvClientSecret)
	fmt.Printf("  %s（可選，未設用使用者設定目錄預設）\n", drive.EnvTokenPath)
}
