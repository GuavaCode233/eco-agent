package config

import "time"

// 本檔定義兩組 profile 的具體數值（正式／測試）。
//
// 正式值：複製自 v12 §4.4.4「集中配置參數（已定案）」。
// 測試值：依 v12 §4.4.4 測試調參原則大幅縮短時間類參數，供數分鐘內觀察完整流程。
//
// 同步提醒：正式值屬「複製關係」，v12 §4.4.4 若改動任一值，本檔須一併同步。
//
// 這組常數現為 Load() 向 5.2 集中配置服務（sensor_config）拉取失敗時的 fallback 預設值
// （見 sensor_config.go；docs/Eco-Agent_後端串接改動清單.md §2）；拉取成功則以回應覆蓋。
// PrinterPollInterval 正式值待實測，暫定 300 秒。

// --- 正式值（v12 §4.4.4 定案）---
const (
	// prodAPIBaseURL 為正式後端 base URL（docs/Eco-Agent_後端串接改動清單.md §1）。
	prodAPIBaseURL = "https://uie47061-eco-sensing-backend.hf.space"

	prodBindingCodeTTL  = 5 * time.Minute
	prodAccessTokenExp  = 1 * time.Hour
	prodRefreshTokenExp = 90 * 24 * time.Hour // 90 天，到期重綁、不輪換

	prodComputerUsageRecordInterval = 60 * time.Second
	prodDriveQuotaInterval          = 24 * time.Hour
	prodCheckInterval               = 60 * time.Second
	prodThresholdCount              = 60
	prodMaxAge                      = 24 * time.Hour
	prodPrinterPollInterval         = 300 * time.Second // 待實測定案
	prodUploadBatchMax              = 720
)

// --- 測試值（縮短時間類參數，落在 v12 §4.4.4 測試建議值範圍內）---
const (
	// testAPIBaseURL 指向本機後端（供測試 profile 一鍵切換）。
	testAPIBaseURL = "http://127.0.0.1:7860"

	testBindingCodeTTL  = 1 * time.Minute     // 建議 1 分鐘
	testAccessTokenExp  = 3 * time.Minute     // 建議 2–5 分鐘
	testRefreshTokenExp = prodRefreshTokenExp // 無測試值，沿用正式值

	testComputerUsageRecordInterval = 10 * time.Second   // 建議 5–10 秒
	testDriveQuotaInterval          = 2 * time.Minute    // 建議 1–2 分鐘
	testCheckInterval               = 5 * time.Second    // 建議 5 秒
	testThresholdCount              = 3                  // 建議 3 筆
	testMaxAge                      = 3 * time.Minute    // 建議 2–3 分鐘
	testPrinterPollInterval         = 5 * time.Second    // 建議 秒級
	testUploadBatchMax              = prodUploadBatchMax // 無測試值，沿用正式值
)

// productionConfig 組出正式值配置。
func productionConfig() Config {
	return Config{
		Profile:                     ProfileProduction,
		BaseURL:                     prodAPIBaseURL,
		BindingCodeTTL:              prodBindingCodeTTL,
		AccessTokenExp:              prodAccessTokenExp,
		RefreshTokenExp:             prodRefreshTokenExp,
		ComputerUsageRecordInterval: prodComputerUsageRecordInterval,
		DriveQuotaInterval:          prodDriveQuotaInterval,
		CheckInterval:               prodCheckInterval,
		ThresholdCount:              prodThresholdCount,
		MaxAge:                      prodMaxAge,
		PrinterPollInterval:         prodPrinterPollInterval,
		UploadBatchMax:              prodUploadBatchMax,
	}
}

// testingConfig 組出測試值配置。
func testingConfig() Config {
	return Config{
		Profile:                     ProfileTesting,
		BaseURL:                     testAPIBaseURL,
		BindingCodeTTL:              testBindingCodeTTL,
		AccessTokenExp:              testAccessTokenExp,
		RefreshTokenExp:             testRefreshTokenExp,
		ComputerUsageRecordInterval: testComputerUsageRecordInterval,
		DriveQuotaInterval:          testDriveQuotaInterval,
		CheckInterval:               testCheckInterval,
		ThresholdCount:              testThresholdCount,
		MaxAge:                      testMaxAge,
		PrinterPollInterval:         testPrinterPollInterval,
		UploadBatchMax:              testUploadBatchMax,
	}
}
