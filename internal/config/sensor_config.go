package config

// 本檔實作 Load() 的開機拉取：對 5.2 集中配置服務發一次 GET sensor_config，以回應覆蓋
// profile 本機常數（docs/Eco-Agent_後端串接改動清單.md §2）。
//
// 現階段只做「開機拉取一次」——不實作版本比對或「上傳回應夾帶版本號、版本不符再拉取」
// （延至 P2，sensor_config 表尚未建）；Response 亦因此忽略回應的 version 欄位。

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// sensorConfigTimeout 是拉取 sensor_config 的逾時（涵蓋連線與讀取）。抓取失敗一律 fallback
// 本地常數（見 Load()），故此逾時只決定「開機卡多久才放棄」，非正確性關鍵。
const sensorConfigTimeout = 5 * time.Second

// sensorConfigHTTPClient 是拉取 sensor_config 用的 HTTP client。
//
// 測試預設會覆寫此變數指向 httptest.Server 或直接失敗的 Transport，避免單元測試意外打到
// 真實後端（見 config_test.go 的 init）；正式執行則維持這裡的預設值。
var sensorConfigHTTPClient = &http.Client{Timeout: sensorConfigTimeout}

// sensorConfigResponse 對應 GET {base}/api/agent/sensor_config 的回應形狀；除 Version 外
// 其餘數值皆為秒數，1:1 對應 Config 的資料收集與上傳觸發欄位（裝置綁定三個欄位亦一併下發）。
type sensorConfigResponse struct {
	// Version 目前僅接收、不使用——版本比對延至 P2（見本檔頂部說明）。
	Version int `json:"version"`

	BindingCodeTTLSec  int `json:"bindingCodeTTL"`
	AccessTokenTTLSec  int `json:"accessTokenTTL"`
	RefreshTokenTTLSec int `json:"refreshTokenTTL"`

	ComputerUsageRecordIntervalSec int `json:"computerUsageRecordInterval"`
	DriveQuotaIntervalSec          int `json:"driveQuotaInterval"`
	CheckIntervalSec               int `json:"checkInterval"`
	ThresholdCount                 int `json:"thresholdCount"`
	MaxAgeSec                      int `json:"maxAge"`
	PrinterPollIntervalSec         int `json:"printerPollInterval"`
	UploadBatchMax                 int `json:"uploadBatchMax"`
}

// fetchSensorConfig 對 baseURL 下的 sensor_config 端點發一次 GET 並解析回應。
// 任何失敗（建構請求、連線、非 200、JSON 解析）皆回傳 error，由呼叫端決定 fallback。
func fetchSensorConfig(baseURL string) (sensorConfigResponse, error) {
	url := Config{BaseURL: baseURL}.APIURL(PathSensorConfig)

	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return sensorConfigResponse{}, fmt.Errorf("config: build sensor_config request: %w", err)
	}
	resp, err := sensorConfigHTTPClient.Do(req)
	if err != nil {
		return sensorConfigResponse{}, fmt.Errorf("config: request sensor_config: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return sensorConfigResponse{}, fmt.Errorf("config: sensor_config returned status %d", resp.StatusCode)
	}

	var out sensorConfigResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return sensorConfigResponse{}, fmt.Errorf("config: parse sensor_config response: %w", err)
	}
	return out, nil
}

// applySensorConfig 把 sensor_config 回應覆蓋進 cfg；秒數欄位換算為 time.Duration。
// cfg.Profile／cfg.BaseURL 不受影響——sensor_config 只下發資料收集/上傳與綁定簽發參數。
func applySensorConfig(cfg *Config, r sensorConfigResponse) {
	cfg.BindingCodeTTL = time.Duration(r.BindingCodeTTLSec) * time.Second
	cfg.AccessTokenExp = time.Duration(r.AccessTokenTTLSec) * time.Second
	cfg.RefreshTokenExp = time.Duration(r.RefreshTokenTTLSec) * time.Second

	cfg.ComputerUsageRecordInterval = time.Duration(r.ComputerUsageRecordIntervalSec) * time.Second
	cfg.DriveQuotaInterval = time.Duration(r.DriveQuotaIntervalSec) * time.Second
	cfg.CheckInterval = time.Duration(r.CheckIntervalSec) * time.Second
	cfg.ThresholdCount = r.ThresholdCount
	cfg.MaxAge = time.Duration(r.MaxAgeSec) * time.Second
	cfg.PrinterPollInterval = time.Duration(r.PrinterPollIntervalSec) * time.Second
	cfg.UploadBatchMax = r.UploadBatchMax
}
