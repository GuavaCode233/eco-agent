# Eco-Agent 後端串接手動測試檢查清單

> 依據 `docs/Eco-Agent_後端串接改動清單.md` 整理。§1–§5 已落地並 commit，`cmd/eco-agent/main.go`（步驟 0：真正打真實後端 + 真實系統金鑰庫的進入點）已建立（commit `a535d5b`）。本檔列出**步驟 6 端到端驗證**實際手動測試時要盯的風險點，依區塊逐一核對。

## 1. 環境變數

- **`ECO_AGENT_UPLOAD_URL` 優先權最高**（[transport.go](../internal/uploader/transport.go)）——如果之前測 mock 時 shell 或 `.env` 裡還留著這個變數，上傳會悄悄繼續打舊的 mock 端點而不是真後端。務必先確認清掉。
- 設定 `ECO_AGENT_API_BASE_URL`（或靠 `ECO_AGENT_PROFILE=testing` → `http://127.0.0.1:7860` 的預設值），並確認實際指向的是你要測的那個後端（本機 vs. HF Space）。
- 若打的是 HF Space（`https://uie47061-eco-sensing-backend.hf.space`），要留意**冷啟動延遲**——第一次請求可能很慢甚至逾時，[sensor_config.go](../internal/config/sensor_config.go) 的 5 秒 `sensorConfigTimeout` 在 Space 冷啟動時可能會直接觸發 fallback（只會記一行 warning，不會報錯），看起來「有跑」但其實跳過了真實抓取，容易漏看。

## 2. `sensor_config`（§2）

- 確認真實回應的 11 個欄位名稱／大小寫跟 `sensorConfigResponse` 完全對齊，否則 JSON 解析會失敗並靜默 fallback（要盯 log 裡的 warning）。
- 也刻意測一次失敗路徑：把後端關掉，確認 `Load()` 不會卡住或崩潰。

## 3. 綁定流程（§3）——風險最高，因為完全沒碰過真實伺服器

- `POST /api/agent/binding-code`：確認接受 `{"device_uuid": ...}`、回傳 `{code, device_secret, expires_at}`，且 `expires_at` 是 RFC3339 格式（解析失敗只會退回用 `bindingCodeTTL`，不會報錯，格式不對很容易被忽略）。
- 綁定請求只接受 HTTP 200 或 201 視為成功（[enroll_http.go:113](../internal/enroll/enroll_http.go#L113)）——確認真實端點實際回哪一個。
- 輪詢 `GET /api/agent/binding-code/{code}/token`：**只要不是 200 就直接視為失敗**，不會繼續輪詢（[enroll_http.go:138-140](../internal/enroll/enroll_http.go#L138-L140)）。如果真實後端在 pending 狀態下偶爾回 404 之類的非 200，`Bind()` 會直接中止而不是繼續等——務必確認後端在 pending 期間穩定回 200 + `status:"pending"`。
- 確認 `consumed` 回應裡真的有非 null 的 `id_token`——§6.1 說這點已在進度表定案，但那只是文件上的說法，要拿真實回應驗證一次。
- 手動跑一次「App 掃碼」端：印出 `ecosensing://bind?code=...` 這行 log 後，用 curl/Swagger 模擬 App 在 `bindingCodeTTL` 內核銷；另外也測一次故意讓它逾時，確認 `Bind()` 會報錯且之後重試可以成功。

## 4. Token 換發（§3.2）

- `POST /api/agent/token/refresh` 的 body 是 `{"refresh_token": ...}`——**不帶 Bearer header**，跟其他端點不同，手動用 curl 測時很容易憑習慣加錯。
- 測 401/403 分支：確認只會清本機憑證（不是設成「已撤銷」終止態），下次 `EnsureBound` 會自動重新 `Bind()`。

## 5. 撤銷（被動偵測）

- 確認 `POST /api/agent/device-bindings/{id}/revoke` 真的只給管理端呼叫，且撤銷生效是反映在**下一次上傳**而不是換發 token 時。測法：先正常綁定，用該管理端點撤銷，再觸發一次上傳，確認 Agent 會自清 Keychain 憑證並停止上傳，之後自動走回 `Bind()`。

## 6. 上傳端點（§5）

- 確認真實的 `Authorization: Bearer <access_token>` header 與扁平 payload 形狀（`usage_date`／`path_type`／`collected_at` + 各路徑量值，不含 `employee_id`/`device_id`）跟後端實際解析邏輯一致——文件裡的假設從未拿真實程式碼驗證過。
- 確認 `path_type` 列舉大小寫（`computer`／`printer`／`drive`）跟後端 `DIGITAL_USAGE.path_type` 值域完全一致。
- 測冪等性：上傳到一半殺掉/重啟 Agent，確認後端不會出現重複列（依賴 `EventID` = `id_token|usage_date|path_type` 跟後端的 upsert 鍵一致）。
- 測亂序重送：較新的 `collected_at` 先到、較舊的後到，確認後端正確保留較新值（`EXCLUDED.collected_at > digital_usage.collected_at`）。

## 7. TLS（§6.2，文件明確標示未解決）

- 確認後端 HTTPS 憑證是受信任 CA 還是自簽。`uploader`／`enroll` 目前都沒有設定 `TLSClientConfig`，若是自簽憑證會直接噴一個籠統的 TLS 錯誤，很容易誤判成網路問題。

## 8. Keychain 持久化

- `platform.NewOSKeychain()`（Windows 上是 DPAPI）目前只被 `cmd/keychain-demo` 用泛用的 set/get/delete 測過，從未透過真實的 `enroll.Bind()` 流程走過一次。要驗證 `bindRealLocked` 寫入的 token 在 Agent（`cmd/eco-agent`）重啟後真的還在。
- macOS 版 Keychain（`keychain_darwin.go`）**完全沒有在任何機器上編譯或測試過**（目前開發環境沒有 Mac）——如果這一輪要顧到跨平台，記得標成未驗證。

## 9. 這次要測到多完整

- 路徑 B（印表機）需要 `ECO_AGENT_PRINTER_HOST` 可透過 SNMP 連通，否則會優雅跳過——要考慮這輪是否需要真實/模擬印表機來把三種 payload 形狀都打到後端，還是先用 A+C 兩條路徑就夠。
- 路徑 C 需要在 `.env` 填真實的 `GOOGLE_OAUTH_CLIENT_ID`/`GOOGLE_OAUTH_CLIENT_SECRET`——這是跟後端 API 無關的另一組憑證，專注在後端串接時很容易漏掉。
