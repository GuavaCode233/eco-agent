# Eco-Agent 後端串接進度表

> 取代 `Eco-Agent_後端串接需求清單_0910.md`「只有後端視角」的侷限，改用**後端／Eco-Agent 雙軌步驟表**追蹤這次串接的實作進度，讓雙邊工程師能對照彼此的配合事項與時序依賴。權威規格見 `docs/Eco-Sensing_專案context文件_v26.md` §4.4、`docs/Eco-Sensing_驗證機制_端點關係表.md`。
>
> 端點命名以本檔為準：Ingest 端點採 `POST /api/agent/digital-usage/batch`（v26 [D16] 命名，與 0910 清單原文 `POST {base_url}/digital-usage/batch` 不同），與其餘綁定鏈端點同屬 `/api/agent/*` 命名空間。

## 進度表

| # | 項目 | 後端事項 | Eco-Agent 事項（對應現有 mock） | 狀態 |
|---|------|---------|--------------------------------|------|
| 0 | DB schema | migration `0004_agent_binding_secret_and_printer_fallback.sql`：`binding_code.device_secret_hash`、`digital_usage.printer_identity_unknown`＋對應 partial unique index | 無 | ✅ 已完成（程式碼），⬜ 待實際套用到 Supabase 執行個體 |
| 1 | 集中配置 | `GET /api/agent/sensor_config`，回傳 11 參數＋`version`（寫死常數，不建表，P2 才做真正的 policy 表） | 替換 `internal/config` mock：開機呼叫此端點，解析回應覆蓋本機預設值 | ✅ 後端已實作 |
| 2 | 索取綁定碼（①） | `POST /api/agent/binding-code`：body `{device_uuid}`，回 `{code, device_secret, expires_at}`；以 `device_uuid` 作為 `device.id` 直接 upsert（不新增欄位） | 替換 `internal/enroll` mock：呼叫①並帶上已持久化的 `device_uuid`（`Enroller.DeviceUUID()` 已實作），收下 `code`／`device_secret`／`expires_at` | ✅ 後端已實作，⬜ Agent 端待接 |
| 3 | App 掃碼核銷（②） | `POST /api/agent/bind`（App 呼叫，非 Agent；`Authorization: Bearer <App access token>`，body **僅** `{code}`，不接受 `employee_id`） | 無（Agent 只需把 `code` 編入 QR 顯示，後續動作在③） | ✅ 後端已實作（App 端串接不在本次範圍） |
| 4 | Agent 領取 token（③） | `GET /api/agent/binding-code/{code}/token`（header `X-Device-Secret`）；`status=consumed` 時**現場**呼叫 `mint_tokens_for_binding_code` 簽發 Access/Refresh（見下方「後端內部設計備註」） | 替換 mock：帶 `code`＋`X-Device-Secret` 輪詢至 `status=consumed`，取得 `access_token`／`refresh_token` 寫入系統金鑰庫（Refresh Token 走 DPAPI/Keychain，不落地純文字） | ✅ 後端已實作，⬜ Agent 端待接 |
| 5 | Access Token 換發（④） | `POST /api/agent/token/refresh`（body `{refresh_token}`），效期以 `device_binding.bound_at + 90天` 推算 | 替換 `internal/enroll.go:254` 的 no-op stub，改為定期呼叫此端點換發 | ✅ 後端已實作，⬜ Agent 端待接 |
| 6 | 撤銷／解綁（⑤） | `POST /api/agent/device-bindings/{device_binding_id}/revoke`（管理端觸發，非 Agent；呼叫者與認證**未決議**，比照既有 `revoke-sessions` 端點先求可用） | 無需新增邏輯——Agent 側撤銷偵測走上傳回應 `401`/`403` 自清憑證，`internal/enroll.go:299` 已有清本機金鑰庫的部分 | ✅ 後端已實作 |
| 7 | 批次上傳 | `POST /api/agent/digital-usage/batch`（asyncpg 條件式 upsert＋印表機差分/重置邏輯，`sensing_mode='auto'`） | 替換 `internal/uploader` mock URL（`transport.go:101`）為正式端點與 TLS 設定，`Uploader` 邏輯本身不需更動 | ✅ 後端已實作，⬜ Agent 端待接、⬜ 待對真實 Supabase 執行個體跑通驗證 |

**圖例**：✅ 已完成　⬜ 待做

---

## 端點形狀速查

### ① `POST /api/agent/binding-code`（無認證）
Request: `{"device_uuid": "<uuid>"}`
Response: `{"code": str, "device_secret": str, "expires_at": "<ISO timestamp>"}`

### ② `POST /api/agent/bind`（`Authorization: Bearer <App access token>`）
Request: `{"code": str}`（不可帶 `employee_id`）
Response: `{"status": "consumed"}`

### ③ `GET /api/agent/binding-code/{code}/token`（header `X-Device-Secret: <device_secret>`）
Response（`pending`）：`{"status": "pending", "access_token": null, "refresh_token": null, "expires_in": null}`
Response（`consumed`）：`{"status": "consumed", "access_token": str, "refresh_token": str, "token_type": "bearer", "expires_in": 3600}`

### ④ `POST /api/agent/token/refresh`（無 Bearer，body 帶 refresh token）
Request: `{"refresh_token": str}`
Response: `{"access_token": str, "token_type": "bearer", "expires_in": 3600}`

### ⑤ `POST /api/agent/device-bindings/{device_binding_id}/revoke`（無認證）
Response: `{"revoked": true|false}`

### `GET /api/agent/sensor_config`（無認證）
Response 範例：
```json
{
  "version": "1",
  "bindingCodeTTL": 300,
  "accessTokenTTL": 3600,
  "refreshTokenTTL": 7776000,
  "computerUsageRecordInterval": 60,
  "driveQuotaInterval": 86400,
  "checkInterval": 60,
  "thresholdCount": 60,
  "maxAge": 86400,
  "printerPollInterval": 300,
  "uploadBatchMax": 720
}
```

### `POST /api/agent/digital-usage/batch`（`Authorization: Bearer <Agent access token>`）
Request:
```json
{
  "id_token": "<device_binding.id_token>",
  "events": [
    {
      "event_id": "...",
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
Response: `{"accepted": <count>}`（200）；裝置遭撤銷時回 401/403（Agent 依此自清憑證）。

---

## 後端內部設計備註（供追溯，不影響 Agent 端行為）

端點③原始規格（v26 §4.4.2）敘述為「②簽發雙 token → ③交付」，但②是 App 觸發、③是 Agent 自行輪詢，兩者之間沒有現成通道可以把 token 從②傳到③。若②當下就簽發並回傳 token，token 會需要先落地成明文才能等③來取，多一段風險窗口。

因此後端實作改為：**②只建立 `device_binding` 並核銷 `binding_code`，不簽發任何 token；③第一次看到 `status=consumed` 時才現場呼叫 `mint_tokens_for_binding_code`**——Access Token 每次都重新簽發（無狀態 JWT，重簽無副作用），Refresh Token 僅在 `device_binding.refresh_token_hash` 尚未寫入時生成一次並存 hash。

**這是純後端內部實作細節，Eco-Agent 端行為完全不受影響**：Agent 仍是「帶 `code`＋`device_secret` 輪詢，直到收到 `status=consumed` 附帶的 tokens」，且在目前設計下②③間沒有額外延遲（③的簽發是同一個請求—回應內完成）。記錄於此僅供日後追查實作脈絡。

**附帶處理的可靠性缺口**：「Refresh Token 只在首次核銷時現場簽發」意味著若那次 HTTP 回應在傳輸中遺失（DB 已寫入雜湊，但 Agent 沒收到明文），單純照原設計 Agent 就再也拿不到 Refresh Token。實作上加了一個折衷：新生成的明文 Refresh Token 於**行程記憶體**暫存 10 分鐘寬限期，寬限期內重複輪詢③仍會拿到同一枚明文；超過寬限期才真正遺失（需重新走一次綁定流程）。這個暫存**不落地資料庫**，重啟服務或多副本部署時不會同步，屬於單機小規模場景的務實折衷，非長期保證。

---

## 已知待辦／限制

- 本次未建立 `sensor_config` 的 DB 表與版本比對機制（v26 明訂延至 P2），Agent 端若要做「版本不符才重拉」需等 P2。
- `factor_id`／`co2e_kg` 排碳係數計算不在本次範圍內，批次上傳落庫的 `digital_usage` 列這些欄位維持 `NULL`。
- 本機開發環境無法連線真實 Supabase 執行個體（DNS 無法解析），已完成的驗證僅止於：路由註冊、Pydantic 模型驗證、認證守門邏輯（401/403）、DB 連線失敗的錯誤處理路徑。**尚未對真實資料庫跑過完整綁定＋上傳生命週期**，正式環境部署前需依 `docs/Eco-Agent_後端串接需求清單_0910.md` 對應章節與本檔第 9 節（見下）補做一次 Swagger 全流程驗證。

## 建議驗證流程（比照既有 B6「Swagger 全生命週期跑通」慣例）

1. `POST /api/agent/binding-code`（假 `device_uuid`）→ 取得 `code`、`device_secret`。
2. `POST /api/auth/login` 取得 App Access Token。
3. `POST /api/agent/bind`（`Authorization: Bearer <App token>`，body `{code}`）→ 確認 `binding_code.status` 變為 `consumed`。
4. `GET /api/agent/binding-code/{code}/token`（`X-Device-Secret`）→ 步驟3前應為 `pending`，之後應為 `consumed` 並帶出 Agent tokens。
5. `POST /api/agent/digital-usage/batch`（`Authorization: Bearer <Agent token>`）送一組涵蓋三路徑的 `events`，其中一筆印表機無 `printer_serial`（驗證 fallback）、兩筆印表機序號相同但第二筆 `printer_page_counter` 較小（驗證重置跳過邏輯）→ 應回 200 `{"accepted": N}`；直接查 `digital_usage` 表確認 upsert／勝出規則／`print_pages` 差分正確。
6. `POST /api/agent/token/refresh` → 確認換發新 Access Token。
7. `POST /api/agent/device-bindings/{id}/revoke` → 重打步驟5（用舊 token）→ 應回 401/403。
8. 重複步驟1兩次、同一 `device_uuid` → 確認 `device`／`binding_code` 是 upsert 而非重複建列。
9. `GET /api/agent/sensor_config` → 確認 11 個欄位＋版本號皆正確。

完成後可將 `docs/Eco-Sensing_驗證機制_端點關係表.md` §1.4 的五個 🟡 標記更新為 ✅。
