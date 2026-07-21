# Eco-Agent 開發 Context Prompt（供 Claude Code 讀取）

> 本檔是給 Claude Code 的**單一任務脈絡**。目標：從零建立 **Eco-Sensing 專案的 Desktop Agent（代號 Eco-Agent）** 骨架。
> **本檔已內嵌 Eco-Agent 所需的全部規格（參數、觸發邏輯、payload、換算式、開發步驟），可自足執行，無需另讀其他文件即可完成任務。**
> 完整專案脈絡與各項設計的決策依據（「為什麼這樣選」）見背景參考文件 `docs/Eco-Sensing_專案context文件_v14.md` 第 4.4 節（數位能耗監測 Desktop Agent）。若本檔與 v14 語意衝突，以 v14 為準。
>
> **建議放置位置**：本檔放 `eco-agent/CLAUDE.md`（專案根目錄，Claude Code 啟動時自動讀取為常駐脈絡）；v14 背景文件放 `eco-agent/docs/` 供人回溯，不作為 Claude Code 主脈絡。兩者皆納入版控（勿被 `.gitignore` 忽略），供組員共享。`§2` 開發步驟表的「狀態」欄請隨進度更新。


---

## 0. 你要做什麼（一句話）

用 **Go** 實作一個無人值守的背景常駐程式，並行採集三條數位能耗感測路徑（電腦使用 / 雲端儲存 / 印表機），把資料寫進**本機持久化佇列**，再由**四重觸發**批次上傳到後端。

現階段**後端尚未完成**，因此：
- **綁定機制（4.4.2）與集中配置參數（4.4.4）以常數／mock 替代**，但**所有佇列、觸發、冪等、去識別化邏輯都要真做**。
- 上傳只 mock **HTTP 端點與 token**，其餘上傳流程（打包、批次、at-least-once、撤銷夾帶檢查）照真實邏輯寫。
- **路徑 C（雲端）真串 Google Drive API**（OAuth 憑證位置見 §6）。

所有「等後端」的地方，一律用**顯著標記**標出（見 §7 標記約定），方便日後替換。

---

## 1. 技術與架構約束（不可偏離）

- **語言**：Go（單一靜態執行檔、跨平台交叉編譯 `GOOS`/`GOARCH`）。理由：無頭背景常駐、idle 記憶體 10–20MB、goroutine 契合三路徑並行。
- **並行模型**：三條感測路徑各自為獨立 goroutine；佇列巡檢（`checkInterval`）一條 goroutine；彼此以 channel／執行緒安全的佇列溝通。
- **本機佇列**：落磁碟持久化（**SQLite 單檔優先**，或 append-only 檔）。**絕不可只放記憶體**——關機/崩潰後資料須仍在。
- **去識別化**：上傳前打包時**移除姓名/Email，只保留員工 ID Token**。Agent 全程只持有不可逆 token，不直接持有員工 ID。
- **傳輸協定**：電腦（路徑 A）與印表機（路徑 B）走 MQTT；雲端（路徑 C）走 HTTPS 直進後端 REST（不經 MQTT Broker）。**現階段兩者的實際送出都先 mock（見 §7）**，但要保留協定分流的程式結構。
- **憑證保護**：Refresh Token 存系統金鑰庫（Windows DPAPI／macOS Keychain），**不寫純文字檔**。現階段 token 為 mock 常數，但**存取介面要照金鑰庫抽象寫**，日後換真值不改結構。

### 建議專案結構（可依 Go 慣例調整，但職責分離要保留）
```
eco-agent/
  cmd/eco-agent/main.go            // 進入點：載入配置、啟動各 goroutine、掛關機 hook
  internal/config/                 // 集中配置參數（§4.4.4，現為常數，預留後端拉取）
  internal/enroll/                 // 裝置綁定與 token（§4.4.2，現為 mock）
  internal/queue/                  // 本機持久化佇列（SQLite）、冪等去重
  internal/sensors/
    computer/                      // 路徑 A
    drive/                         // 路徑 C（真串 Google Drive API）
    printer/                       // 路徑 B
  internal/uploader/              // 打包、去識別化、四重觸發、批次上傳（HTTP mock）、撤銷夾帶檢查
  internal/platform/              // OS 差異：GetLastInputInfo / IOHID、金鑰庫、關機事件
```

---

## 2. 開發步驟（嚴格依此順序：路徑 A → 路徑 C → 路徑 B）

每條路徑完成後**都必須能獨立執行與測試**，也要能與已完成的前面路徑**合併測試**。每一步結束時提供：可執行的 demo 進入點、對應單元測試、以及一段「如何獨立驗證」的說明。

**狀態欄圖例**：⬜ 未開始 ／ 🟡 進行中 ／ ✅ 完成 ／ ⏸️ 暫緩。**Claude Code 完成每個子項後請更新對應「狀態」欄。**

### 總覽

| 步驟 | 內容 | 產出協定 | 觸發模式 | 狀態 |
|------|------|----------|----------|------|
| Step 0 | 地基：本機持久化佇列 + 配置常數 + 綁定 mock + 上傳骨架（四重觸發） | — | — | ✅ |
| Step 1 | 路徑 A：電腦使用（狀態值輪詢，短區間，active/idle 兩態，使用率加權、後端計算） | MQTT（mock 送出） | 固定區間輪詢 | ✅ |
| Step 2 | 路徑 C：雲端儲存（狀態值輪詢，長區間，真串 Google Drive） | HTTPS（mock 送出） | 持久化時間戳到期判斷 | 🟡 |
| Step 3 | 路徑 B：印表機（僅個人專屬機 SNMP 輪詢歸戶） | MQTT（mock 送出） | 持久化時間戳到期判斷 | ⬜ |

### Step 0 — 地基（佇列 + 配置 + 上傳骨架）

> 先把三路徑共用的地基建好，否則路徑 A 無處可寫。

| # | 子項 | 說明 | 狀態 |
|---|------|------|------|
| 0.1 | `internal/config` | §4.4.4 所有參數以**常數**定義，標 `// TODO(backend): 由 5.2 集中配置服務下發`；提供 `Config` struct 與 `Load()`，現回傳常數、預留改 HTTP 拉取 | ✅ |
| 0.2 | `internal/queue` | SQLite 持久化佇列，介面至少含 `Enqueue`／`PeekBatch(n)`／`MarkUploaded(ids)`／`OldestAge()`／`Count()`；每筆帶**唯一事件 ID**（`id_token + 日期 + 路徑類型` 組穩定鍵）供後端冪等去重 | ✅ |
| 0.3 | `internal/enroll` | 提供 `IDToken()`／`AccessToken()` 等介面，現**回傳 mock 常數**（§7），照金鑰庫抽象預留真實實作 | ✅ |
| 0.4 | `internal/uploader` | 實作**四重觸發**與 at-least-once（詳 §3），上傳函式先打 mock 端點（§7） | ✅ |
| 0.V | 獨立驗證 | 手動 `Enqueue` 幾筆假資料，觀察四種觸發各自正確 flush、mock 端點收到、佇列僅在「200」後清除 | ✅ |

### Step 1 — 路徑 A：電腦使用（狀態值輪詢，短區間，active/idle 兩態）

> **能耗模型（v14 [D7]）**：棄「活躍時間 × TDP」。Agent **純感測、只送原始量**，能耗由後端以使用率加權模型 `P_idle + 使用率 ×(P_active − P_idle)` 計算。誘因對齊「節能」（歸戶 idle 開機的浪費）而非「少用」。

| # | 子項 | 說明 | 狀態 |
|---|------|------|------|
| 1.1 | `internal/platform`（活動偵測） | 封裝 Windows `GetLastInputInfo()` 與 macOS `IOHIDGetModifierLockState()`，回傳「距上次輸入的間隔」；macOS 需 Accessibility 授權，啟動時檢查並給引導訊息 | ✅ |
| 1.2 | CPU 使用率（跨平台） | 用 `gopsutil`（`cpu.Percent`）取即時 CPU 使用率，Windows/macOS 一致介面、免特殊權限；併入同一輪詢週期取樣 | ✅ |
| 1.3 | `internal/sensors/computer`（active/idle 分態） | 每 `computerUsageRecordInterval`（60 秒）輪詢，依「距上次輸入間隔」是否超過閾值判該區間為 **active／idle**，分別累計時數並記平均 CPU 使用率；**Agent 不算能耗**，只 `Enqueue` 原始量。Payload：`date`、`pc_active_hours`、`pc_idle_hours`、`pc_avg_cpu_util`、`cpu_model`（取代舊 `pc_tdp_w`） | ✅ |
| 1.4 | sleep/喚醒處理 | sleep/hibernate/關機時 Agent 被掛起、不計費（本無記錄，其低耗電自然不進帳）；喚醒後以 **wall-clock 時間戳差分**辨識掛起空白（間隔遠大於輪詢區間），該段不計 active/idle | ✅ |
| 1.5 | 即時功耗 fallback（預留、不實作） | Intel RAPL／Apple `powermetrics` 更準但需權限、不跨平台、BYOD 多不可行；**結構預留、現階段不實作**，標 `// TODO(backend): 即時功耗覆蓋（RAPL/powermetrics）作為精度增強` | ✅ |
| 1.6 | 流量量特性 | 關機期間無時數可採，跳過即可、**不需補查**（不套用路徑 C 的 deadline-check） | ✅ |
| 1.V | 獨立驗證 | 跑 Agent，操作/閒置電腦，確認 active/idle 時數分別累計、平均使用率合理、佇列筆數隨時間增長並達 `thresholdCount`／`maxAge` 觸發上傳；模擬睡眠喚醒後時間戳差分正確跳過空白 | ✅ |
| 1.M | 合併驗證 | 與 Step 0 佇列/觸發串起端到端跑通 | ✅ |

### Step 2 — 路徑 C：雲端儲存（狀態值輪詢，長區間，持久化時間戳觸發）

> **關鍵設計，務必照做，不可用 `sleep(24h)` 絕對計時器。**

| # | 子項 | 說明 | 狀態 |
|---|------|------|------|
| 2.1 | `internal/sensors/drive`（真串 API） | **真串 Google Drive API v3**，`about?fields=storageQuota` 取用量；OAuth 憑證位置見 §6 | ✅ |
| 2.2 | 觸發模型（時間戳） | 不用絕對計時器；用**持久化時間戳 `lastDriveQuotaCheckAt`**（與佇列同一份 SQLite/落磁碟，見 `queue.SetState/GetState`）；**掛 `checkInterval`（60 秒巡檢）**，判斷 `now() - lastDriveQuotaCheckAt >= driveQuotaInterval`（24h）才查、`Enqueue`、更新時間戳。掛巡檢而非 `computerUsageRecordInterval`（職責分離）。查詢／入列失敗不更新時間戳，下次巡檢自然重試 | ✅ |
| 2.3 | 冷啟動 | 時間戳不存在（`GetState` ok=false）或無法解析視為「已到期」，第一次巡檢即查並寫入時間戳 | ✅ |
| 2.4 | 開機補查 | 關機數日後開機，若距上次查詢已超過 `driveQuotaInterval`，開機後首次巡檢自動補查——與「開機後檢查」合流（`Run` 啟動先立即巡檢一次），**無需另寫** | ✅ |
| 2.5 | 能耗換算與送出 | Agent 純感測、只送原始量（比照路徑 A）：Payload `{date, drive_usage_gb}`（= `storageQuota.usage` 換算 GB），能耗（儲存量GB × PUE × 電力係數）由後端計算；走 HTTPS（協定分流由 uploader 處理，現 mock 送出） | ✅ |
| 2.V | 獨立驗證 | `cmd/drive-sensor-demo`：縮短 `driveQuotaInterval` 觀察到期即查；預置很久以前時間戳 → 啟動即補查；冷啟動（無時間戳）第一次即查 | ✅ |
| 2.M | 合併驗證 | A + C 同跑，各自節奏、共用同一佇列與上傳觸發 | ⬜ |

### Step 3 — 路徑 B：印表機（僅個人專屬機 SNMP 輪詢歸戶）

> 依 v14：路徑 B 歸戶前提已定案。**本階段只做「個人專屬印表機 SNMP 輪詢歸戶」一軌**；共用機的 Print Server Log / Pull Printing API 標「未來實作」不在範圍；手動上傳用紙量屬 App 端、非 Agent 路徑。

| # | 子項 | 說明 | 狀態 |
|---|------|------|------|
| 3.1 | `internal/sensors/printer`（SNMP） | SNMP（UDP 161）查 OID `1.3.6.1.2.1.43.10.2.1.4`（page counter 累計值），前後相減得增量頁數，以 mock ID Token 歸戶 | ⬜ |
| 3.2 | 感測模式（時間戳） | page counter 無推播 → 只能**輪詢**；用 `printerPollInterval`（暫定 300 秒、標 TODO）；同屬狀態量長輪詢，**沿用 Step 2 時間戳到期判斷**（`lastPrinterPollAt`，同掛 `checkInterval`） | ⬜ |
| 3.3 | 能耗換算與送出 | 能耗 = 增量頁數 × 紙張生命週期係數；Payload：`date`、`print_pages`；走 MQTT（現 mock 送出） | ⬜ |
| 3.4 | BYOD 摩擦點 | SNMP 需與印表機同網段——啟動時檢查連通性，不通則跳過並記 log，不使 Agent 卡住 | ⬜ |
| 3.V | 獨立驗證 | 對可 SNMP 的印表機（或本機 mock SNMP responder）輪詢，確認增量頁數正確、歸戶到 mock ID Token | ⬜ |
| 3.M | 合併驗證 | A + C + B 三路徑齊跑，單一佇列匯集、四重觸發統一上傳，端到端 demo | ⬜ |



---

## 3. 上傳觸發模型（四重觸發，皆不綁絕對時刻）— 必須完整實作

| 觸發 | 角色 | 實作要點 |
|------|------|----------|
| **累積達量** `thresholdCount` | 主力 | 佇列達 N 筆即 flush；跟資料量走、自我調節 |
| **關機／登出前 hook** | 兜底 | Windows shutdown event／macOS `NSWorkspaceWillPowerOffNotification`，關機前搶送零頭 |
| **開機／喚醒後檢查** | 補送 | 啟動時先檢查佇列有無上次未送完者，立即補上（路徑 C/B 的到期補查也在此合流） |
| **最長滯留時間** `maxAge` | 保底 | 佇列最舊一筆超過 X 小時仍未送即 flush |

**At-least-once（至少一次送達）**
- 佇列資料**僅在後端回 200 才標記已上傳並清除**；失敗（離線/後端不可用）則保留，搭下次觸發重送。
- 每筆帶**唯一事件 ID**，供後端 upsert/冪等去重，重複送達不重複計算。

**重試策略（照 v14 §4.4.4 定案）**
- **不設獨立重試計時器**：失敗資料留佇列，搭下次正常觸發重送。
- **不設最大重試次數上限**：送不出即保留至成功（佇列膨脹由 `maxAge` 與離線期間無新資料自然封頂）。
- **不採指數退避**：重試節奏跟隨既有稀疏觸發（最密 `checkInterval` 60 秒），無密集重試迴圈。

**撤銷夾帶檢查（現階段對 mock 端點也要保留這條邏輯）**
- 每次 flush 上傳時，讀取後端回應的有效性狀態；若回 `401/403` 視為已撤銷 → **自我清除憑證（含金鑰庫 Refresh Token）、停止上傳**。
- mock 端點預設回 200；**提供一個開關/環境變數讓 mock 回 401/403**，以便測試撤銷自清路徑。

---

## 4. 集中配置參數（§4.4.4，現以常數實作，預留後端下發）

> **同步提醒**：下表數值複製自 v14 §4.4.4，屬複製關係。v14 若改動任一值，本表須一併同步。

在 `internal/config` 以常數定義下列值，每個都標 `// TODO(backend): 由 5.2 集中配置服務（sensor_config）下發`：

| 變數名 | 正式值 | 測試建議值 |
|--------|--------|-----------|
| `bindingCodeTTL` | 5 分鐘 | 1 分鐘 |
| Access Token exp | 1 小時 | 2–5 分鐘 |
| Refresh Token exp | 90 天（到期重綁、不輪換） | — |
| `computerUsageRecordInterval` | 60 秒 | 5–10 秒 |
| `driveQuotaInterval` | 24 小時 | 1–2 分鐘 |
| `checkInterval` | 60 秒 | 5 秒 |
| `thresholdCount` | 60 筆 | 3 筆 |
| `maxAge` | 24 小時 | 2–3 分鐘 |
| `printerPollInterval` | 實測後定值（暫定 300 秒） | 秒級 |
| `uploadBatchMax` | 720 筆 | — |

> 請提供一個 `config.testing.go` 或環境變數開關，一鍵切換「正式值 / 測試值」，方便在數分鐘內觀察完整流程。

---

## 5. 裝置綁定（§4.4.2，現以 mock 實作，預留後端串接）

現階段後端不存在，因此：
- `internal/enroll` **不實作真正的索取 binding_code / 掃碼 / 換 token 流程**，改提供 mock：
  - `IDToken()` 回傳一個固定的 mock 員工 ID Token 常數（標 §7）。
  - `AccessToken()` / `RefreshToken()` 回傳 mock 常數；金鑰庫存取介面照真實抽象寫（`platform.Keychain`），現在讀寫 mock 值。
- **預留串接空間**：把「索取 binding_code → 顯示 QR（custom scheme URI，見 v14 §4.5）→ 等 App 掃碼 → 後端發雙 token → 存金鑰庫」整條流程寫成一個帶 `// TODO(backend)` 的 stub 函式與註解，日後後端就緒即可填。
- 綁定的完整規格（BINDING_CODE 表、5 分鐘 TTL、consumed/expired、防重放、雙 token、撤銷）**寫進註解**供日後實作對照，不在本階段落地後端側。

---

## 6. Google Drive API（路徑 C 真串）— 需人工填入的憑證位置

路徑 C 真串 Google Drive API v3，你需要在程式中**明確標出下列需人工提供的設定位置**，並用環境變數/設定檔載入（**切勿把金鑰寫死進原始碼或提交進版控**）：

- `GOOGLE_OAUTH_CLIENT_ID`、`GOOGLE_OAUTH_CLIENT_SECRET`：OAuth 2.0 用戶端憑證。
- OAuth token 存放路徑（首次授權後的 refresh token 落地位置）。
- 授權範圍 scope：`https://www.googleapis.com/auth/drive.metadata.readonly`（只讀 metadata 足以取 storageQuota）。

請在 README 或設定檔範本（`.env.example`）標明這三處，並在程式對應位置留 `// TODO(secrets): 填入 Google OAuth 憑證，勿硬編碼` 註解。若憑證未提供，路徑 C 應優雅降級（記 log 並跳過，不使整個 Agent 崩潰）。

---

## 7. 「等後端」標記約定（務必一致，方便日後全域搜尋替換）

在所有暫以常數/mock 代替、日後要接後端的位置，統一加以下標記之一：

- `// TODO(backend): <說明>` — 等後端 API 就緒才能替換的邏輯（綁定、配置下發、真實上傳端點、撤銷回應）。
- `// TODO(secrets): <說明>` — 需人工填入的憑證（Google OAuth）。
- `// MOCK: <說明>` — 目前回傳假值的函式/常數（mock token、mock HTTP 端點、mock storageQuota fallback）。

**mock 上傳端點**：實作一個可設定的 `uploadURL`（環境變數），預設指向本機 mock（例如 `http://localhost:8080/mock/ingest`）。附一個極簡的 Go mock server（可另檔），預設回 200；提供開關讓它回 401/403 以測撤銷自清。

---

## 8. 交付與驗證清單（每步都要能獨立 + 合併測試）

完成時請確保：
1. `go build` 三平台交叉編譯皆過（至少 Windows/macOS）。
2. 每條路徑各有：獨立 demo 跑法 + 單元測試 + 「如何獨立驗證」說明。
3. 端到端 demo：三路徑齊跑 → 佇列匯集 → 四重觸發上傳到 mock 端點。
4. 專測兜底路徑（照 v14 測試原則）：
   - 關機前 hook 搶送零頭；
   - 開機後補送未送完資料；
   - 路徑 C/B 到期判斷觸發（含冷啟動視為已到期、關機數日後開機補查）；
   - 撤銷生效（mock 回 401/403 → 自清憑證停止上傳）；
   - 冪等去重（同一事件 ID 重送不重複）。
5. 用測試值配置能在數分鐘內觀察上述完整流程。
6. 全部 `TODO(backend)` / `TODO(secrets)` / `MOCK` 標記可用 grep 一次列出，附一份清單於 README。

---

## 9. 關鍵不可違反事項（重申）

- 路徑 C **不可用 `sleep(24h)` 絕對計時器**；必用持久化時間戳 + `checkInterval` 到期判斷。
- 佇列**必落磁碟**，非純記憶體。
- 去識別化：上傳只帶 ID Token，不帶姓名/Email。
- Refresh Token 走金鑰庫抽象，**不寫純文字檔**（即使現在是 mock 值）。
- 佇列**只在 200 後清除**；at-least-once 不可打折。
- 開發順序**嚴格 A → C → B**，每步可獨立/合併測試。

## graphify

This project has a knowledge graph at graphify-out/ with god nodes, community structure, and cross-file relationships.

Rules:
- For codebase questions, first run `graphify query "<question>"` when graphify-out/graph.json exists. Use `graphify path "<A>" "<B>"` for relationships and `graphify explain "<concept>"` for focused concepts. These return a scoped subgraph, usually much smaller than GRAPH_REPORT.md or raw grep output.
- If graphify-out/wiki/index.md exists, use it for broad navigation instead of raw source browsing.
- Read graphify-out/GRAPH_REPORT.md only for broad architecture review or when query/path/explain do not surface enough context.
- After modifying code, run `graphify update .` to keep the graph current (AST-only, no API cost).
