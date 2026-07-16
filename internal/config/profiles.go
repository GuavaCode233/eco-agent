package config

import "time"

// 本檔定義兩組 profile 的具體數值（正式／測試）。
//
// 正式值：複製自 v12 §4.4.4「集中配置參數（已定案）」。
// 測試值：依 v12 §4.4.4 測試調參原則大幅縮短時間類參數，供數分鐘內觀察完整流程。
//
// 同步提醒：正式值屬「複製關係」，v12 §4.4.4 若改動任一值，本檔須一併同步。
//
// 每一參數皆標 TODO(backend)：日後由 5.2 集中配置服務（sensor_config）下發，
// 本檔常數退居為拉取失敗時的內建預設值。

// --- 正式值（v12 §4.4.4 定案）---
const (
	// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發。
	prodBindingCodeTTL = 5 * time.Minute
	// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發。
	prodAccessTokenExp = 1 * time.Hour
	// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發。
	prodRefreshTokenExp = 90 * 24 * time.Hour // 90 天，到期重綁、不輪換

	// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發。
	prodComputerUsageRecordInterval = 60 * time.Second
	// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發。
	prodDriveQuotaInterval = 24 * time.Hour
	// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發。
	prodCheckInterval = 60 * time.Second
	// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發。
	prodThresholdCount = 60
	// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發。
	prodMaxAge = 24 * time.Hour
	// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發；正式值待實測，暫定 300 秒。
	prodPrinterPollInterval = 300 * time.Second
	// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發。
	prodUploadBatchMax = 720
)

// --- 測試值（縮短時間類參數，落在 v12 §4.4.4 測試建議值範圍內）---
const (
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
