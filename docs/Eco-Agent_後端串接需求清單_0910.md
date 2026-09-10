# Eco-Agent 後端串接需求清單

> 本檔整理 **Eco-Agent（Desktop Agent）** 在後端尚未架設前，需要交給後端團隊的串接資訊。內容依據目前程式碼實際狀態（非僅規格文件）整理，所有引用皆附檔案位置以便查證。若與 `.claude/CLAUDE.md`、`docs/Eco-Sensing_專案context文件_v26.md` 有語意衝突，以 v26 為準；本檔僅作「串接交付清單」用途。
>
> 完成後請將此清單交給後端團隊作為 API 設計輸入；後端端點就緒後，Agent 側只需替換 `internal/uploader`、`internal/enroll`、`internal/config` 三處的 mock 實作，不需更動整體架構。

---

## 1. 資料上傳端點（Ingest API）

**預計路由**：`POST {base_url}/digital-usage/batch`
（目前為 mock 端點 `http://localhost:8080/mock/ingest`，標記於 `internal/uploader/transport.go:101`；屆時只需替換 URL 與 TLS 設定，`Uploader` 邏輯不需變動）

### 1.1 Request Header

| Header | 值 | 說明 |
|---|---|---|
| `Content-Type` | `application/json` | |
| `Authorization` | `Bearer <access_token>` | 短期 Access Token（現為 mock 常數，見 §2） |

### 1.2 Request Body

扁平陣列，每筆事件為單層 JSON 物件（**不**包一層 `payload`）：

```json
{
  "id_token": "<員工 ID Token，不可逆、不含姓名/Email>",
  "events": [
    {
      "event_id": "<id_token>|<usage_date>|<path_type>",
      "path_type": "computer",
      "usage_date": "2026-09-10",
      "collected_at": "2026-09-10T13:45:00.123456789Z",
      "pc_active_hours": 3.5,
      "pc_idle_hours": 1.2,
      "pc_avg_cpu_util": 27.4,
      "cpu_model": "Intel(R) Core(TM) i7-1165G7"
    }
  ]
}
```

**共同欄位（三路徑皆有，用於冪等鍵與勝出判定）**：

| 欄位 | 型別 | 說明 |
|---|---|---|
| `event_id` | string | 冪等唯一鍵，組成 `"<id_token>|<usage_date>|<path_type>"`（`internal/queue/queue.go:67`） |
| `path_type` | string | 列舉 `computer` / `drive` / `printer`，由 Agent 明送，後端不可用欄位樣態推斷 |
| `usage_date` | string | `YYYY-MM-DD`，該筆用量所屬日期 |
| `collected_at` | string | RFC3339Nano、UTC，Agent 端**採集時間戳**（非後端接收時間）。同一 `event_id` 重送時每次都會更新 |

**各路徑量值欄位**（附加在共同欄位之上，同層扁平）：

| 路徑 | 欄位 | 型別 | 說明 |
|---|---|---|---|
| computer | `pc_active_hours` | float | 該區間內 active 累計時數 |
| | `pc_idle_hours` | float | 該區間內 idle 累計時數 |
| | `pc_avg_cpu_util` | float | 平均 CPU 使用率（%） |
| | `cpu_model` | string（可缺） | CPU 型號，取不到時省略此欄位 |
| drive | `drive_usage_gb` | float | Google Drive 已用容量（GB） |
| | `drive_trash_gb` | float | 垃圾桶可釋放容量（GB） |
| printer | `printer_page_counter` | number | SNMP 累計頁數**原始值**（非增量，後端需自行做差分與計數器重置防呆） |
| | `printer_serial` | string（可缺） | 印表機序號，三個候選 OID 皆查不到時省略 |

> 備註：`employee_id`、`device_id` **皆不在 payload 內**。Agent 只持有 `id_token`，後端需以其查 `DEVICE_BINDING` 表解出兩者。

### 1.3 Response 語意

| 狀態碼 | Agent 行為 |
|---|---|
| `200` | 該批次標記已上傳，佇列清除（at-least-once，僅 200 才清除） |
| `401` / `403` | 視為裝置已撤銷：清除本機憑證（含金鑰庫 Refresh Token）、停止上傳 |
| 其他 / 連線失敗 | 批次留在佇列，等下一次觸發（累積達量／關機前／開機後／`maxAge`）自然重送；**不設獨立重試計時器、不設重試上限、不做指數退避** |

> 目前 Agent 僅讀取狀態碼，**不解析 response body**；若後端規劃在回應中夾帶配置版本號（供 §3 集中配置比對），需另外定義欄位與時機，Agent 端尚未實作解析邏輯。

### 1.4 批次大小

單次請求最多 `uploadBatchMax` 筆（正式值 720 筆，測試值同），超過則分批送出。

---

## 2. 裝置綁定與 Token 流程

現況：`internal/enroll/enroll.go` 全數為 mock，需要後端提供以下端點方能串接真實流程（完整規格見該檔案 13–35 行的流程註解）：

### 2.1 索取綁定碼

`POST /api/agent/binding-code`

- Agent 送出本機持久化的 `device_uuid`（UUID v4，已為真實值，非 mock）。
- 後端建立/更新 `BINDING_CODE` 記錄：`status=pending`、`created_at`、`expires_at = created_at + bindingCodeTTL`（正式值 5 分鐘）、綁定該 `device_id`（採 upsert 而非盲插，避免中途放棄的綁定產生孤兒紀錄）。
- 回傳一次性 `binding_code`。

### 2.2 App 掃碼換發 Token

- Agent 將 `binding_code` 編碼為自訂 scheme QR：`ecosensing://bind?code=<binding_code>`。
- 員工以已登入的 Eco-Sensing App 掃碼，App 將「已驗證身分 + binding_code」送至後端。
- 後端驗證 `status=pending && expires_at > now()`（防重放），建立 `device_binding`，回填 `BINDING_CODE.employee_id`、`consumed_at`、`status=consumed`。
- 核發雙 Token：
  - **Access Token**：短期（正式值 1 小時），不需後端存放，可隨時以 Refresh Token 換發。
  - **Refresh Token**：長期（正式值 90 天，到期需重新綁定、**不輪換**），後端僅需存 `refresh_token_hash`。

### 2.3 Access Token 換發

供 Agent 定期以 Refresh Token 換發新 Access Token（目前 `internal/enroll/enroll.go:254` 為 no-op stub）。TTL 應由後端回應決定，不應由 Agent 端寫死。

### 2.4 撤銷 / 解綁

供管理端撤銷裝置繫結（目前 `internal/enroll/enroll.go:299` 僅清本機金鑰庫，未呼叫後端）。撤銷生效方式已定案為**被動偵測**：不另設心跳，撤銷狀態只在上傳回應為 `401`/`403` 時被 Agent 察覺並自清憑證（見 §1.3）。

---

## 3. 集中配置服務（`sensor_config`）

**端點需求**：`GET sensor_config`，回傳以下 11 個欄位供 Agent 覆蓋本機常數，並帶版本號供比對是否需要重新拉取：

| 參數 | 正式值 | 測試值 |
|---|---|---|
| `bindingCodeTTL` | 5 分鐘 | 1 分鐘 |
| Access Token 效期 | 1 小時 | 3 分鐘 |
| Refresh Token 效期 | 90 天 | 同正式值 |
| `computerUsageRecordInterval` | 60 秒 | 10 秒 |
| `driveQuotaInterval` | 24 小時 | 2 分鐘 |
| `checkInterval` | 60 秒 | 5 秒 |
| `thresholdCount` | 60 筆 | 3 筆 |
| `maxAge` | 24 小時 | 3 分鐘 |
| `printerPollInterval` | 300 秒（待實測定案） | 5 秒 |
| `uploadBatchMax` | 720 筆 | 同正式值 |

> 設計預期為「開機拉取一次 + 版本號隨上傳回應夾帶、版本不符才重拉」，但該版本比對機制目前**僅為文件設計，尚未實作**於 `internal/uploader`。若後端要落地此機制，需與 Agent 端另行對齊回應欄位格式與觸發時機。

另有兩處候選參數日後也可能併入 `sensor_config`（非必要，供後端評估是否一併規劃）：
- 電腦 active/idle 判定的 idle 閾值（`internal/sensors/computer/computer.go:28`）
- 印表機 SNMP 目標位址相關設定（`internal/sensors/printer/client.go:73`，目前由環境變數 `ECO_AGENT_PRINTER_HOST` 等提供）

---

## 4. 不需要後端提供的部分

以下屬 Agent 本機關注點，**無需**後端協助或提供資訊：
- Google OAuth 憑證（`GOOGLE_OAUTH_CLIENT_ID`/`SECRET`）：由部署端自行於 Google Cloud Console 申請，透過環境變數載入，不進版控。
- SNMP 印表機目標位址與 OID：屬部署現場設定，經環境變數提供。
- 本機 SQLite 佇列結構：純 Agent 內部實作，與後端無資料交換。

---

## 5. 交付總結（給後端團隊的最小需求）

後端要讓 Eco-Agent 可以正式串接，最少需要提供：

1. **資料上傳端點**：`POST {base_url}/digital-usage/batch`，接受 §1.2 的扁平 JSON 陣列格式，以 `Authorization: Bearer` 驗證，回應語意依 §1.3（200 成功、401/403 撤銷）。
2. **裝置綁定三端點**：索取綁定碼、App 掃碼換發雙 Token、Access Token 換發，另加一支解綁/撤銷端點（§2）。
3. **集中配置端點**：`GET sensor_config`，回傳 §3 的 11 個參數與版本號。

現況所有等待後端串接之處，程式碼內皆以 `// TODO(backend)`、`// TODO(secrets)`、`// MOCK:` 標記，可用 grep 一次列出核對（詳見 `README.md` 底部標記清單）。
