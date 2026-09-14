# Eco-Agent 後端串接改動清單

> 本檔依據 `docs/Eco-Agent_後端串接需求清單_0910.md` 與 `docs/Eco-Agent_後端串接進度表.md`（權威版本，取代 0910 清單）整理出 Eco-Agent 程式碼側需要改動的具體位置，並依開發順序表逐步落地；各章節內文完成後於文中標註「已落地」，進度以上方開發順序表的狀態欄為準。

## Context

進度表記錄後端目前已實作完畢的端點形狀（sensor_config、綁定五端點、批次上傳）。進度表是權威版本，端點統一在 `/api/agent/*` 命名空間下，Ingest 端點正式命名為 `POST /api/agent/digital-usage/batch`。

Eco-Agent 目前三處為 mock：`internal/config`（sensor_config 常數）、`internal/enroll`（裝置綁定五端點全 mock）、`internal/uploader`（上傳端點指向 mock server）。CLAUDE.md 明確指示「後端端點就緒後，Agent 側只需替換這三處 mock 實作，不需更動整體架構」——本次改動即依此邊界規劃，不動 `internal/queue`（`EventID` 格式 `<id_token>|<usage_date>|<path_type>` 已與後端一致，無需改動）與 `internal/sensors/*`。

另外，進度表確認了一項先前規格文件未定案、會影響實作範疇的決策：**撤銷走被動偵測**（Agent 上傳收到 401/403 才自清憑證），`POST /api/agent/device-bindings/{device_binding_id}/revoke` 是管理端呼叫、**不是 Agent 呼叫**——`enroll.go` 現有的 `Unbind` 已經是正確行為，只需把過時的 TODO 註解更新掉，不需要新增 HTTP 呼叫。

已與使用者確認的方向：本次也一併補上真實 Keychain 實作（Windows DPAPI／macOS Keychain Services），因為 mock 的 `MemoryKeychain` 無法真的撐住 90 天 Refresh Token；QR 顯示先以 log 印出 `ecosensing://bind?code=...` URI 為主，不做 ASCII/圖檔渲染。

## 開發順序表

> 依相依關係排序，非依章節編號順序：§1 是三個模組共用的基礎，須最先落地；§4 Keychain 需先於 §3 enroll（enroll 要把真實 token 寫進去才有意義）；§5 uploader 排最後，因為它要驗證的是 enroll 產出的真實 access token 能否打通上傳。**狀態欄圖例**沿用 `CLAUDE.md` §2 慣例：⬜ 未開始 ／ 🟡 進行中 ／ ✅ 完成 ／ ⏸️ 暫緩。每完成一步，除更新本表，也請同步更新對應章節內文的完成情形。

| 順序 | 對應章節 | 內容 | 狀態 |
|------|----------|------|------|
| 1 | §1 | 共用：後端 base URL 與 HTTP client 注入 | ✅ |
| 2 | §2 | `internal/config`：sensor_config 真串（開機拉取一次，失敗 fallback 本地常數） | ✅ |
| 3 | §4 | `internal/platform`：真實 Keychain 實作（Windows DPAPI／macOS Keychain Services） | ✅ |
| 4 | §3 | `internal/enroll`：綁定五端點真串（`Bind`／`refreshAccessTokenLocked`／`Unbind` 註解更新／測試改注入假後端） | ✅ |
| 5 | §5 | `internal/uploader`：上傳端點指向真實後端（base URL 組合、`TLSClientConfig` 視情況補） | ✅ |
| 6 | 驗證方式 | 端到端驗證（含撤銷路徑、Keychain 持久化、`go test ./...`） | ⬜ |

---

## 1. 共用：後端 base URL 與 HTTP client 注入

三個模組（config／enroll／uploader）都要打同一個後端。新增一個共用的 base URL 環境變數 `ECO_AGENT_API_BASE_URL`，依 `internal/config` 現有的 profile 機制切換：

- 測試值（`ProfileTesting`）：`http://127.0.0.1:7860`
- 正式值（`ProfileProduction`）：`https://uie47061-eco-sensing-backend.hf.space`

三處各自組出完整路徑：

- `GET {base}/api/agent/sensor_config`
- `POST {base}/api/agent/binding-code`
- `GET {base}/api/agent/binding-code/{code}/token`
- `POST {base}/api/agent/token/refresh`
- `POST {base}/api/agent/digital-usage/batch`（`internal/uploader/transport.go` 現有 `ECO_AGENT_UPLOAD_URL` 可繼續作為獨立覆寫，優先權高於 base URL 組合，維持與現有 mock server／測試的相容性）

`internal/enroll/enroll.go` 目前沒有任何 HTTP client 欄位；仿照 `internal/uploader` 的 `Sender`/`WithSender` 模式，替 `Enroller` 加一個可注入的 HTTP 介面（例如 `BindingClient`），讓 `enroll_test.go` 能用 `httptest.Server` 假後端測試，不需真的連外部服務。

**已落地**：

- `internal/config/config.go`：新增 `EnvAPIBaseURL`（`ECO_AGENT_API_BASE_URL`）與 `Config.BaseURL`；`Load()` 依 profile 取預設值後以環境變數覆寫，`LoadProfile()` 維持純粹（不讀環境變數，供測試取得可預期的 profile 預設值）。
- `internal/config/profiles.go`：`prodAPIBaseURL`／`testAPIBaseURL` 兩組預設值，分別對齊上方測試值／正式值。
- `internal/config/endpoints.go`（新檔）：`PathSensorConfig`／`PathBindingCode`／`PathTokenRefresh`／`PathDigitalUsageBatch` 四個路徑常數、`PathBindingCodeToken(code)`（轉義後組出含 code 的路徑）、`Config.APIURL(path)`（組出完整端點，去除 BaseURL 尾端多餘 `/`）。此檔只提供組裝，**尚未被任何模組實際呼叫**——config 拉取（§2）、enroll 五端點（§3）、uploader 端點切換（§5）三步落地時才會用到。
- `internal/enroll/enroll.go`：新增 `BindingClient` 介面（`Do(req) (*http.Response, error)`，`*http.Client` 天然滿足）、`Option`／`WithBindingClient`／`WithBaseURL`，`New()` 改為變參選項（向後相容，既有 7 處呼叫端不需改）並給預設 `httpClient`（`&http.Client{Timeout: 10s}`）。`Bind()`／`refreshAccessTokenLocked()` 仍是 mock，**尚未實際使用**此 client／baseURL——留給 §3 接線。
- 測試：`internal/config/config_test.go` 新增 `TestBaseURLDefaultsPerProfile`／`TestLoadReadsEnvAPIBaseURL`／`TestAPIURL`；`internal/enroll/enroll_test.go` 新增 `TestWithBindingClientAndBaseURLInjectable`。
- `.env.example`：補上 `ECO_AGENT_API_BASE_URL` 說明與預設值對照。

---

## 2. `internal/config` — sensor_config 真串

- `config.go` 的 `Load()`/`LoadProfile()`（現在只回本地常數）：開機時對 `GET /api/agent/sensor_config` 發一次請求，解析回應 JSON（`version`、`bindingCodeTTL`、`accessTokenTTL`、`refreshTokenTTL`、`computerUsageRecordInterval`、`driveQuotaInterval`、`checkInterval`、`thresholdCount`、`maxAge`、`printerPollInterval`、`uploadBatchMax`，皆為秒數）覆蓋 `Config` struct 對應欄位。
- 抓取失敗（連線失敗、非 200、解析錯誤）→ **fall back 到現有本地常數**（`productionConfig()`/`testingConfig()`），記 log，不讓 Agent 卡住或崩潰。
- 進度表明確說明「版本比對 + 只在版本不符時重拉」機制延至 P2（DB 表未建），因此**這次只做「開機拉取一次」**，不用實作版本比對或從上傳回應夾帶版本號的機制——`internal/uploader` 的 `Response` struct 維持只有 `StatusCode`，不需改動。
- `Config` struct 欄位本身已與回應 1:1 對應，不需改結構；只換 `Load()` 內部實作。
- 移除/更新 `config.go` 與 `profiles.go` 中各參數上的 `TODO(backend)` 標記為已完成，或改標「P2：版本比對」視情況保留。

**已落地**：

- `internal/config/sensor_config.go`（新檔）：`fetchSensorConfig(baseURL)` 對 `{base}/api/agent/sensor_config` 發 `GET`（逾時 `sensorConfigTimeout`=5s），解析 `sensorConfigResponse`（11 個欄位，命名與進度表一致；`version` 只接收不使用，版本比對延至 P2）；`applySensorConfig` 把秒數欄位換算 `time.Duration` 覆蓋進 `Config`。
- `config.go` 的 `Load()`：取 profile 本機常數為 baseline → 套用 `EnvAPIBaseURL` 覆寫 → 呼叫 `fetchSensorConfig`；任何失敗（建構請求、連線、非 200、JSON 解析）記一行 `slog` warning 並直接回傳 baseline（不覆蓋、不崩潰）；成功則覆蓋。`LoadProfile()` 維持不變（不拉取，供測試/demo 需要確定性數值時使用）。
- `profiles.go`：移除逐參數重複的 `TODO(backend)` 註解，改在檔頭一次說明「這組常數現為 `Load()` 拉取失敗時的 fallback」；`prodPrinterPollInterval` 待實測定案的提醒保留。
- 測試（`internal/config/sensor_config_test.go`，新檔）：`init()` 把套件層級的 `sensorConfigHTTPClient` 換成一個永遠立即失敗的 `RoundTripper`，讓所有測試（含既有 `TestLoadReadsEnvProfile` 等）預設「網路已停用」，不會意外打到 `prodAPIBaseURL`/`testAPIBaseURL` 這類真實位址；想測試「拉取成功」的案例改用 `withSensorConfigServer` 明確指向 `httptest.Server`。涵蓋：拉取成功覆蓋數值、連線失敗 fallback、非 200 fallback、JSON 格式錯誤 fallback，皆已在本機以 `go test ./internal/config/...` 通過（測試全程無真實網路存取，數十毫秒內跑完）。

---

## 3. `internal/enroll` — 綁定五端點真串

`internal/enroll/enroll.go` 檔頭（13–34 行）已經寫好完整流程說明，直接依此落地：

### 3.1 `Bind(ctx) error`（現在是純 mock，168–197 行）

1. `POST /api/agent/binding-code`，body `{"device_uuid": <DeviceUUID()>}`（`DeviceUUID()` 已是真實實作，不需改）→ 解析 `{code, device_secret, expires_at}`。
2. Log 印出 `ecosensing://bind?code=<code>` 供人工顯示／轉發給 App 掃碼（本次不做 QR 渲染，純文字 log）。
3. 輪詢 `GET /api/agent/binding-code/{code}/token`，header `X-Device-Secret: <device_secret>`，直到 `status=consumed` 或 `expires_at` 到期（用 `bindingCodeTTL` 作輪詢逾時上限，逾時回錯誤讓上層重新走 `Bind`）。
4. `status=consumed` 時取得 `access_token`／`refresh_token`／`id_token`（③ 回應已確認直接帶 `"id_token": "<device_binding.id_token>"`，§6.1 開放問題已解決，不需 JWT 解碼繞路）。
5. 把 `refresh_token`、`id_token` 寫入 Keychain（沿用現有 `keyIDToken`/`keyRefreshToken`），`access_token` 連同 `expires_in` 換算的到期時間存入記憶體快取（沿用現有 `accessToken`/`accessTokenExpiry` 欄位）。

`device_secret`／`code` 只在 `Bind()` 執行期間存於記憶體，不落地；若 Agent 在綁定完成前重啟，視為綁定失敗，需重新呼叫 `Bind()`（重新索取新 binding_code）——這是可接受的簡化，因為 `bindingCodeTTL` 本身就短（5 分鐘量級）。

### 3.2 `refreshAccessTokenLocked`（250–266 行，`_ = rt` 那行是核心 mock 點）

- 改為 `POST /api/agent/token/refresh`，body `{"refresh_token": rt}`（無需 Bearer header，注意這點跟其他端點不同）→ 解析 `{access_token, token_type, expires_in}`，更新 `e.accessToken`／`e.accessTokenExpiry`。
- 若後端回 401/403（refresh token 已失效/裝置已撤銷）：呼叫既有的 `clearLocked()` 邏輯把本機憑證清掉，讓上層走回 `EnsureBound` → 重新 `Bind()`，語意上與上傳路徑的撤銷自清一致。

### 3.3 `Unbind(ctx) error`（296–304 行）

- **不需要新增 HTTP 呼叫**——進度表確認 `revoke` 端點是管理端觸發，Agent 側撤銷偵測已經正確地掛在上傳回應 401/403（`ClearCredentials`）。只需要把附近「回應規格未定，需要 Agent 呼叫撤銷端點」的舊 TODO 註解更正為「撤銷為被動偵測，本函式僅清本機憑證，符合設計」，避免日後誤解要再補一支呼叫。

### 3.4 測試

`enroll_test.go` 的 `TestEnsureBoundThenTokens`、`TestAccessTokenRefreshOnExpiry` 目前斷言 mock 常數，改用注入的假 HTTP client／`httptest.Server` 模擬五端點回應（pending→consumed、401 refresh 等），驗證真實流程下的狀態轉換與 Keychain 寫入。

**已落地**（含一項對原規劃的調整，見下方說明）：

- **設計調整——真實流程改為「有設定 base URL 才啟用」，而非直接取代 mock**：規劃原文假設 `Bind()`／`refreshAccessTokenLocked` 直接換成真實 HTTP 流程，但 repo 目前有 6 支 demo（`all-paths-demo`／`computer-demo`／`drive-sensor-demo`／`eco-agent-demo`／`printer-demo`／`printer-sensor-demo`）與 `uploader_test.go` 都呼叫 `enroll.New(platform.NewMemoryKeychain(), q)`（不帶 base URL），且 CLAUDE.md 明確要求「每步驟都要能獨立執行與測試」；若無條件換成真實流程，這些完全不依賴後端的 demo 會在第一次 `EnsureBound` 就對空字串 URL 發請求失敗、當場全部跑不動。因此改為：`bindLocked`／`refreshAccessTokenLocked` 檢查 `e.baseURL`——非空才走 `enroll_http.go` 的真實五端點流程，為空（`New()` 預設值，未呼叫 `WithBaseURL`）則維持原本的 mock 捷徑（`bindMockLocked`，邏輯與原本完全相同）。7 個既有呼叫端因此零改動、零回歸；真正要接後端時，未來的 `cmd/eco-agent/main.go`（目前尚未建立）只需在建構 `Enroller` 時加 `enroll.WithBaseURL(cfg.BaseURL)`（可再選配 `enroll.WithBindingCodeTTL(cfg.BindingCodeTTL)`）即可切換到真實流程，不需要再改 `enroll` 套件本身。
- `internal/enroll/enroll_http.go`（新檔）：五端點的 wire 格式與 HTTP 往返（`requestBindingCode`／`pollBindingCodeToken`／`refreshAccessTokenHTTP`／`bindRealLocked`，共用 `doJSON` helper），端點路徑直接重用 §1 的 `config.PathBindingCode`／`PathBindingCodeToken`／`PathTokenRefresh`／`Config.APIURL`（`enroll` 因此新增對 `config` 套件的依賴；`config` 不依賴 `enroll`，無循環）。輪詢邏輯：逾時上限取 `min(bindingCodeTTL 推算的 deadline, 伺服器回的 expires_at)`（`expires_at` 解析失敗僅記警告、不影響逾時判斷），輪詢間隔另可用 `WithBindingCodePollInterval` 覆寫（供測試縮短）。
- `internal/enroll/enroll.go`：`Bind()`／`bindLocked`／`refreshAccessTokenLocked` 依上述改為分派；新增 `bindMockLocked`（原 mock 邏輯原封不動搬過來）。`refreshAccessTokenLocked` 收到 401/403 時呼叫 `clearLocked()`（不是 `ClearCredentials()`）並回 `ErrNotBound`——刻意不設 `revoked` 終止態，因為這不是上傳路徑偵測到的撤銷，只是「這份 refresh token 死了、需要重綁」，下次 `EnsureBound` 會因 `isBoundLocked()`==false 自動重新 `Bind()`。`Unbind` 的舊 TODO 註解已依規劃改為說明撤銷為被動偵測、本函式僅清本機憑證即符合設計，未新增 HTTP 呼叫。新增 `Option`：`WithBindingCodeTTL`、`WithBindingCodePollInterval`、`WithLogger`（QR URI 走這個 logger，預設 `slog.Default()`）。
- 測試：**`TestEnsureBoundThenTokens`／`TestAccessTokenRefreshOnExpiry` 未改動**——因為上述設計調整後，這兩個測試（不設定 base URL）驗證的正是「mock 捷徑路徑」，仍然有效、且與原本語意一致，不需要為了「不再是暫時 mock」而重寫。改為在新檔 `internal/enroll/enroll_http_test.go` 新增 5 個測試專門涵蓋真實 HTTP 流程：`TestBindRealFlowSuccessAfterPending`（pending 數次後 consumed，驗證四個憑證正確落地＋`X-Device-Secret` header 正確帶上）、`TestBindRealFlowTimesOutWhenAlwaysPending`（一直 pending 到逾時，驗證回錯誤且不落地任何憑證）、`TestBindRealFlowBindingCodeRequestFails`（端點①回 500，驗證直接失敗、不進入輪詢）、`TestRefreshAccessTokenHTTPSuccess`（端點④成功換發）、`TestRefreshAccessTokenHTTP401ClearsCredentialsNotRevoked`（端點④回 401，驗證清憑證但 `revoked` 仍為 `false`）。全數以 `httptest.Server` + Go 1.22+ 的 `ServeMux` 方法/萬用字元路由模擬後端，不連真實網路，已在本機以 `go test ./internal/enroll/...` 全數通過（含原有測試，共 13 個測試、約 0.25 秒跑完）。

---

## 4. `internal/platform` — 真實 Keychain 實作

`internal/platform/keychain.go` 目前只有 `MemoryKeychain`。新增（照現有 `Keychain` interface，不改介面）：

- `keychain_windows.go`（build tag `windows`）：用 `golang.org/x/sys/windows`（已在 go.mod）呼叫 DPAPI `CryptProtectData`/`CryptUnprotectData` 加密後存一個本機檔案（例如 `%LOCALAPPDATA%\eco-agent\credentials.dat`），`Get`/`Set`/`Delete` 對應讀寫該檔案內的加密 blob。
- `keychain_darwin.go`（build tag `darwin`）：透過 cgo 呼叫 Security.framework Keychain Services（`SecItemAdd`/`SecItemCopyMatching`/`SecItemDelete`）存取系統鑰匙圈項目。
- 依平台在 `cmd/eco-agent/main.go` 用 build tag 或 runtime 判斷選對實作（現在很可能是硬寫 `platform.NewMemoryKeychain()`，改成依平台選擇，非 Windows/macOS 平台維持 fallback 到記憶體並記 log 警告）。
- 兩個新檔案各自需要對應的單元測試（可在對應平台的 CI 環境跑，或至少寫成能在本機手動驗證）。

**已落地**：

- `internal/platform/keychain_windows.go`（新檔，`//go:build windows`）：`windowsKeychain` 以 DPAPI（`golang.org/x/sys/windows` 的 `CryptProtectData`/`CryptUnprotectData`，`CRYPTPROTECT_UI_FORBIDDEN` 避免無頭背景常駐跳出系統提示）逐鍵加密，JSON 落地於 `newWindowsKeychain` 傳入的路徑（`NewOSKeychain()` 預設 `%LOCALAPPDATA%\eco-agent\credentials.dat`，`LOCALAPPDATA` 未設時退到 `os.UserConfigDir()`），寫入採「暫存檔 + rename」原子落地。
- `internal/platform/keychain_darwin.go`（新檔，`//go:build darwin`，cgo）：`darwinKeychain` 依規格用 `SecItemAdd`/`SecItemCopyMatching`/`SecItemUpdate`/`SecItemDelete`（`kSecClassGenericPassword`，service 固定 `eco-agent`、account 即呼叫端傳入的 key），C 側收樣板邏輯（建 query dictionary、呼叫 SecItem*、轉 `CFTypeRef`）降低 cgo 邊界複雜度。**此檔未在本機驗證**——本開發環境是 Windows、無 macOS 工具鏈（`clang`/Xcode Command Line Tools），`GOOS=darwin CGO_ENABLED=1 go build` 在此環境必然因缺 C 編譯器而失敗，只做到 `gofmt` 格式檢查；正式視為完成前建議在實機 Mac 跑一次 `go build`/`go test`/`keychain-demo` 確認。
- `internal/platform/keychain_other.go`（新檔，`//go:build !windows && !darwin`）：`NewOSKeychain()` fallback 到 `MemoryKeychain` 並用 `slog` 印警告（含 `runtime.GOOS`），對應「非 Windows/macOS 平台維持 fallback 並記警告」——已用 `GOOS=linux CGO_ENABLED=0 go build ./...` 驗證可編譯。
- 統一入口 `platform.NewOSKeychain()`（各平台檔各自定義同名函式，build tag 互斥）供未來 `cmd/eco-agent/main.go`（目前尚未建立，見 §Context）呼叫；現有 demo 系列（`eco-agent-demo` 等）維持用 `platform.NewMemoryKeychain()`——它們刻意用暫存目錄佇列、每次重跑重新 mock 綁定，換成真實金鑰庫會在開發機的系統金鑰庫留下每次 demo 產生的假憑證，不是預期行為。
- 新增 `cmd/keychain-demo`：對「這台機器上真實的系統金鑰庫」（而非 `MemoryKeychain`）做 Set/Get/Delete 往返 + 冪等驗證的獨立 demo（§V 慣例）。已在本機（Windows）實跑通過：Set/Get 值一致、Delete 後 `Get` 正確回 `ErrKeychainNotFound`。
- 測試：`internal/platform/keychain_windows_test.go`（`//go:build windows`）涵蓋往返、找不到鍵、刪除冪等、跨實例持久化（模擬重啟）、多鍵互不干擾，並斷言落地檔案不含明文——已在本機以 `go test ./internal/platform/...` 全數通過。darwin 因同上原因無法在本機執行測試，僅完成寫作與格式檢查。

---

## 5. `internal/uploader` — 上傳端點指向真實後端

- `transport.go` 的 `DefaultUploadURL` 與 `ECO_AGENT_UPLOAD_URL` 機制基本不變；若走 §1 的共用 base URL，預設值改為 `{base}/api/agent/digital-usage/batch`，`ECO_AGENT_UPLOAD_URL` 仍可覆寫（供本機 mock server 測試沿用）。
- `MockHTTPSender` 的邏輯（組 JSON、帶 `Authorization: Bearer <access_token>`、只讀 status code）已經符合後端規格，**不需要改資料流程**；可考慮改名成不含「Mock」字樣的名稱（例如 `HTTPSender`）以反映它現在也是正式路徑，但這是 cosmetic，非必須。
- `http.Client` 目前沒有另外設定 TLS——Go 預設會用系統憑證存放區驗證 HTTPS，正式後端若用一般受信任 CA 簽發的憑證，理論上不需額外程式碼；若後端用自簽憑證則需額外補 `TLSClientConfig`（視實際部署環境決定，先不假設）。
- `Response` struct 維持只有 `StatusCode`（呼應 §2，版本號夾帶機制延至 P2，不現在做）。

**已落地**：

- `internal/uploader/uploader.go` 的 `New()`：端點解析改為三段優先序——① `ECO_AGENT_UPLOAD_URL` 環境變數（不變，供本機 mock server／測試沿用，優先權最高）；② 皆未設時，若 `cfg.BaseURL` 非空，組出 `cfg.APIURL(config.PathDigitalUsageBatch)`（新，即真實後端端點）；③ 以上皆空（`cfg` 為零值等異常情形）才退到 `DefaultUploadURL` 這個本機 mock 常數兜底。`transport.go` 的 `DefaultUploadURL`／`EnvUploadURL` 常數本身不變，只更新了上方註解說明新的優先序與各自角色。
- **`MockHTTPSender` 未改名**：規劃文字本就標記這是 cosmetic、非必須；確認除 `uploader` 套件內部（`transport.go` 定義、`uploader.go` 使用）外無任何外部引用後，決定不做這個無功能影響的改名，避免無謂 diff。
- **TLS 未動**：規劃本就寫明「視實際部署環境決定，先不假設」；正式後端憑證是否為受信任 CA 簽發尚未確認（見 §6.2 開放問題），`http.Client` 沿用 Go 預設（系統憑證存放區），未加 `TLSClientConfig`。
- **零回歸確認**：檢查過 `internal/uploader` 全部既有呼叫端（`all-paths-demo`／`computer-demo`／`drive-sensor-demo`／`eco-agent-demo`／`printer-sensor-demo`，共 5 個 demo）皆已明確用 `WithUploadURL(ts.URL+"/mock/ingest")` 指向各自的 in-process mock server，完全不受 `New()` 預設端點變動影響——這次調整只影響「呼叫端沒有指定端點」時的預設行為，屬新增分支、非改動既有路徑。
- 測試（新檔 `internal/uploader/default_url_test.go`）：`TestNewDefaultUploadURLFromBaseURL`（無環境變數、`cfg.BaseURL` 非空 → 組出 digital-usage/batch 端點）、`TestNewEnvUploadURLTakesPriorityOverBaseURL`（環境變數優先於 `cfg.BaseURL`）、`TestNewFallsBackToDefaultUploadURLWhenBaseURLEmpty`（兩者皆空 → 退到 `DefaultUploadURL`）。皆直接建構 `Uploader` 並斷言內部 `*MockHTTPSender.url`（同套件測試，未發送任何請求），加上既有 9 個 uploader 測試（共 12 個），已在本機以 `go test ./internal/uploader/...` 全數通過。

---

## 6. 待跟後端確認的開放問題（不阻塞規劃，但要在實作前/中對齊）

### 6.1 `id_token` 從何取得 — ✅ 已解決

原本的疑慮：批次上傳 payload 需要頂層 `id_token` 欄位（[D12]/[D14] 的去識別化設計：不帶 `employee_id`/`device_id`，只帶不可逆的 `id_token`），但進度表初版 ③ `GET /api/agent/binding-code/{code}/token` 的回應範例只列出 `access_token`/`refresh_token`，沒有 `id_token`，一度懷疑要另外用 JWT 解碼 `access_token` 才能取得。

**現況（進度表已更新）**：③ 端點的兩種回應都已直接補上 `id_token` 欄位：

```json
// pending
{"status": "pending", "access_token": null, "refresh_token": null, "expires_in": null, "id_token": null}
// consumed
{"status": "consumed", "access_token": str, "refresh_token": str, "token_type": "bearer", "expires_in": 3600, "id_token": "<device_binding.id_token>"}
```

因此 §3.1 步驟 4 的實作直接從 `consumed` 回應解析 `id_token` 即可，**不需要 JWT 解碼繞路**。先前分析中「Eco-Agent `access_token` 是否為 JWT、能否比照 App 端 `get_current_employee` 解碼取值」的討論仍具參考價值（保留於下方供追溯），但已非本次實作所需路徑。

<details>
<summary>保留：先前的 JWT 解碼分析（已非必要路徑，僅供追溯）</summary>

參考 `Eco-Sensing_App_驗證機制_開發參考.md` 後，曾確認兩點：

1. App 的 `access_token` 明確是 JWT（§4.2：`"access_token": "<JWT>"`），且該文件 §1 明講「與 Eco-Agent 雙 token 同構，差別僅觸發情境」——與進度表「Access Token 為無狀態 JWT，重簽無副作用」的描述互相印證，兩者應是同一套後端 token 簽發機制。
2. 但 App 的識別方式（`employee_id` 完全由後端從 Bearer token 解出、從不出現在 body）與 Eco-Agent 的 `id_token`（刻意留在 payload 裡的欄位，[D12]）不是同一種模式；且 `internal/queue.EventID` 在感測當下就需要 `id_token`，這個時間點不保證 `access_token` 有效，所以無論能否從 JWT 解出，都必須把 `id_token` 獨立持久化在 Keychain。JWT 解碼至多只能是 `Bind()` 當下取值的備援管道之一。

既然 ③ 回應已直接給欄位，這條備援路徑不需要走。

</details>

### 6.2 其他開放問題

- HTTPS 憑證是自簽還是受信任 CA？決定 uploader/enroll 的 `http.Client` 是否要額外配置 `TLSClientConfig`。
- `sensor_config` 目前是「寫死常數，不建表」，之後若後端真的上 P2 版本比對機制，Agent 端要再補一次「上傳回應夾帶版本號」的解析邏輯——本次不做，先記錄。

---

## 驗證方式（規劃階段，供實作後對照）

1. 用進度表 §「建議驗證流程」的 9 個步驟，先跑一輪 Swagger/curl 手動驗證後端端點形狀，確認跟本次假設一致（尤其 §6.1 的 `id_token` 問題）。
2. `go test ./...`：`internal/config`、`internal/enroll`、`internal/uploader`、`internal/platform` 的新/改測試全過。
3. 端到端手動 demo（`cmd/eco-agent`）：指向真實後端（或本機跑後端服務），從全新裝置（無 Keychain 資料）開始，觀察 log 印出的 `ecosensing://bind?code=...`，人工用 App／curl 模擬掃碼核銷，確認 Agent 輪詢後拿到 tokens、三路徑資料正常上傳、重開 Agent 後 Keychain 內憑證仍在（驗證真實 Keychain 持久化）。
4. 撤銷路徑：呼叫 `POST /api/agent/device-bindings/{id}/revoke`（模擬管理端），下一次上傳應收到 401/403，確認 Agent 自清憑證並停止上傳、之後自動重新走 `Bind()`。
