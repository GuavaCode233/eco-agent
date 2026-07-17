# Eco-Sensing 專案 Context 文件

> 大學畢業專題 ｜ 企業範疇三碳排 AI 智慧核算助理
> 本文件作為 Project 內的共用脈絡，供討論、開發與分工對齊使用。

---

## 0. 文件用途與閱讀約定

這份文件是專案的「單一事實來源（Single Source of Truth）」。每次與 Claude 或團隊成員討論時，可先參照此文件對齊目標、架構與當前進度。內容應隨開發演進持續更新。

**閱讀約定（v0.8 起）**：

- **現況快照**：全文件定案總覽，回答「現在是什麼」的第一站。
- 各模組章節固定分兩段：**「規格（現行定案）」**只寫現行設計、全部肯定句；**「決策記錄（脈絡與依據）」**保留「為什麼這樣選、曾否決什麼」的推理過程。引用設計時以規格段為準，決策記錄僅供回溯脈絡。
- 每個模組開頭有一行 **依賴宣告**，標明該模組依賴的其他模組與共用資源。
- 第 7 節待釐清議題以 `[章節][狀態]` 標籤標註歸屬與進度。

---

## 現況快照（讀我優先；細節與依據見對應章節）

| 模組／層面 | 現行定案 | 詳見 |
|-----------|----------|------|
| 差旅核算 | 三軌上傳（高鐵票／計程車紙本／App 截圖），OCR＋GPT-4o NER，員工確認後送出 | 4.1 |
| 廢棄物 | 員工掃桶上 QR 開 session → 投入 → App 點投入完畢；樹莓派匿名上傳、後端配對歸戶（A＋C＋D＋G 組合） | 4.2 |
| 電梯 | NFC 手機掃描進出樓層，HTTPS 直送後端（不經 MQTT） | 4.3 |
| Eco-Agent | Go 開發；方案 B 手機掃碼綁定＋雙 token；本地持久化佇列＋四重觸發上傳；集中配置參數已定案（4.4.4）；電腦路徑改使用率加權、Agent 純感測後端計算（4.4 [D7]） | 4.4 |
| 印表機歸戶 | 優先開發「個人專屬機（Eco-Agent SNMP 輪詢歸戶）」與「手動上傳用紙量（App 主動感測、須搭誘因）」；共用機的 Print Server Log 與 Pull Printing API 列為可行、待實作測試 | 4.4、7 |
| 綁定碼儲存 | 後端 `BINDING_CODE` 表持久化短效一次性碼（5 分鐘 TTL、消費即失效、過期即失效） | 4.4.2 |
| QR 辨識 | 全系統統一 custom scheme URI；掃描一律開/用 App，App 依 URI host/path 分流動作 | 4.5 |
| Agent 撤銷 | 每次上傳夾帶撤銷狀態（不另做心跳），`401/403` 即自清憑證；離線延遲上界 ≈ maxAge | 4.4.2 |
| 後端 | FastAPI＋Supabase（PostgreSQL），一律走 connection pooler；MQTT consumer 批次寫入；讀取端快取 | 5.1 |
| Controller | 自建輕量版，FastAPI 內管理模組（控制面／數據面邏輯分離）；配置經 MQTT retained＋HTTPS 夾帶雙通道下發 | 5.2 |
| 開發階段 | Roadmap 第一～三步進行中；後端依 P1→P2→P3 推進，P1 未開始 | 6、5.1 |

---

## 1. 專案概述

| 項目 | 內容 |
|------|------|
| 專題名稱 | Eco-Sensing：企業範疇三碳排 AI 智慧核算助理 |
| 核心定位 | 基於分眾虛擬感知器（SVS）設計之軟體定義物聯網（SD-IoT）平台 |
| 解決痛點 | 填補現有碳盤查流程中「員工行為數據採集」的技術空白 |
| 架構主軸 | SD-IoT 控制面（Control Plane）與數據面（Data Plane）分離 |
| 技術整合 | 多模態 OCR（差旅單據）＋ AI Vision（廢棄物分類）＋ Desktop Agent（數位能耗） |
| 行為激勵 | 綠色數位孿生（GDT）模型 × 利潤分享模型（Shared Savings） |
| 最終產出 | 範疇三碳排數據自動化核算 ＋ 視覺化報表 |

### 核心概念名詞（共用資料字典）

- **SVS（Segmented Virtual Sensors，分眾虛擬感知器）**：將不同類型的員工行為數據來源抽象為統一的「虛擬感知器」，由軟體定義其行為，而非綁定特定硬體。
- **SD-IoT（軟體定義物聯網）**：控制邏輯與數據傳輸分離，控制面集中管理感知器與規則，數據面負責實際資料流。
- **GDT（綠色數位孿生）**：以數位模型映射員工/組織的碳行為，作為視覺化與激勵的基礎。
- **Shared Savings（利潤分享模型）**：將減碳所節省的成本部分回饋給員工，形成行為誘因。

---

## 2. 系統範疇對應（GHG Protocol）

本系統聚焦於最難採集的**範疇三（Scope 3）**員工行為數據，並涵蓋部分 Scope 1/2 的辦公室能耗。

| 模組 | GHG 範疇 | 盤查對象 | 傳輸協定 |
|------|----------|----------|----------|
| 商務差旅碳核算 | Scope 3 Cat. 6（商務旅行） | 差旅里程、交通工具、碳排量 | HTTPS / REST API |
| 辦公室廢棄物辨識 | Scope 1/2（含廢棄物處理） | 廢棄物類型、重量 | MQTT |
| 電梯搭乘追蹤 | Scope 1/2（電力消耗） | 垂直交通電力、樓層移動 | HTTPS / REST API |
| 數位能耗監測（Desktop Agent） | Scope 3 Cat. 8 / 辦公行為 | 電腦使用、列印頁數、雲端儲存 | MQTT / HTTPS |

---

## 3. 傳輸協定架構（跨模組共用決策）

混合協定架構，依資料特性分流：

### MQTT — 本地感測裝置資料上傳
- 適用：廢棄物辨識（樹莓派 4B）、Desktop Agent（電腦/印表機）
- 特性：輕量、低耗電、適合高頻事件推送（封包最小 2 bytes）
- Broker：Mosquitto（統一中介）

### HTTPS / REST API — 呼叫外部第三方服務
- 適用：差旅收據 OCR（GPT-4o）、Google Drive API v3、TDX 運輸 API、Google Maps API、電梯搭乘數據（手機 App → 後端）
- 特性：加密傳輸、支援 OAuth 2.0、適合大型檔案（圖片）

---

## 4. 四大功能模組規格

### 4.1 商務差旅碳核算（HTTPS）

> 依賴：排放係數庫 `EMISSION_FACTOR`（後端維護、5.2 下發）｜GPT-4o（NER）｜TDX 運輸 API、Google Maps API｜App 掃描頁（上傳入口）

#### 規格（現行定案）

**三軌資料來源**，統一匯入碳核算引擎：

| 軌道 | 收據類型 | 自動化程度 | 處理路徑 |
|------|----------|------------|----------|
| A | 高鐵電子票 | 半自動 | OpenCV 前處理 → Tesseract OCR → GPT-4o NER → TDX 里程查詢 |
| B | 計程車紙本 | 人工補填 | OCR 取金額 → 員工手填起訖點 → Google Maps 換算里程 |
| C | App 乘車截圖 | 半自動 | OCR 取起訖/距離 → GPT-4o 辨識工具 → Google Maps 補算里程 |

- **GPT-4o NER 輸出**：固定 JSON Schema（origin / destination / transport_mode / date / amount），避免自由格式。
- **排放係數**：高鐵 32 gCO₂e/人公里（環保署認證）；計程車與其他交通依環保署/IPCC 係數；係數庫後端維護、不寫死於前端。
- **確認流程**：三軌計算完成後顯示「完整記錄確認畫面」，員工確認後送出，數據綁定報銷單據 ID。

#### 決策記錄（脈絡與依據）

- **[D1] 為何採「手動上傳照片/截圖」而非直接串接乘車 App**：原考慮直接串接 App 碳排量，但實際可直接串的僅 Uber for Business，故統一改採「手動上傳照片/截圖」做分析。

### 4.2 辦公室廢棄物辨識（MQTT）

> 依賴：App 掃碼與 session 事件（HTTPS）｜後端 session 配對歸戶（5.1）｜`EMISSION_FACTOR`｜5.2 配置下發（信心度閾值、session 逾時、互斥鎖開關）

#### 規格（現行定案）

- **硬體**：樹莓派 4B（主控）＋ USB 攝影機（投入辨識）＋ IoT 重力感測器（GPIO 連接，秤重與投入觸發）。固定 QR Code 標籤貼於垃圾桶外殼（綁定 bin_id）。
- **識別流程**：員工以手機 App 掃描垃圾桶上的固定 QR Code（含 bin_id）→ 後端為該 (employee_id, bin_id) 開啟一個 session（含逾時保護）→ 員工投入廢棄物 → 員工在 App 點選「投入完畢」→ 樹莓派完成本地辨識與秤重後上傳匿名投入事件 → 後端配對 session 並補上 employee_id，完成碳足跡歸戶。
- **歸戶設計（A＋C＋D＋G 組合）**：
  - **Session 綁定（方案 A）**：掃桶上 QR 開啟 session，「投入完畢」關閉並結算；session 內可累計多次投入。員工未點「投入完畢」時，由 session 逾時自動結算孤兒事件。
  - **互斥鎖防併發（方案 C）**：掃 QR 即向後端對該 bin 申請鎖，鎖成功才允許歸戶；鎖期間他人掃同桶顯示「使用中，請稍候」，避免兩人同時用同桶歸錯戶。低頻情境下 C 可視部署規模啟用。
  - **後端中介配對（方案 D）**：樹莓派只送**匿名**投入事件（不含 employee_id），身份配對完全在後端完成；樹莓派維持「純感測、不碰個資」定位，符合去識別化與控制面／數據面分離原則。
  - **重力觸發辨識＋投入完畢結算（方案 G）**：重力感測偵測到投入即事件驅動觸發單次本地辨識（非全時推論），事件先暫存；「投入完畢」做封存結算，兼顧辨識時機準確與算力可控。
- **Edge AI**：YOLOv8n（nano，TACO Dataset 微調）本地推論，**不上傳原始影像，只傳 JSON**。
  - 辨識 18 類辦公廢棄物；準確率：紙張 94.2%、金屬罐 96.8%、塑料瓶 88.5%、一般垃圾 82.1%。
  - 推論延遲 12.8–18.5 ms。
  - Fallback：信心度低於閾值時，影像特徵轉文字描述送 LLM 判定。
- **MQTT Payload**（topic: `waste/bin{id}`，樹莓派端，匿名）：bin_id、timestamp、waste_type、confidence、weight_g。**不含 employee_id**；employee_id 由後端配對 session 後寫入資料庫。
- **App 端事件**（HTTPS → 後端）：掃 QR 開 session（employee_id、bin_id、scan_timestamp）、投入完畢（session_id、confirm_timestamp）。
- **碳排換算**：`E_waste = Σ(W_j × EF_waste_type_j)`。

#### 決策記錄（脈絡與依據）

- **[D1] 移除 USB QR Code 掃描器**（v0.3）：識別方式改由員工手機鏡頭掃桶上固定 QR——身份驗證的責任移到已登入的 App，樹莓派維持純感測、不碰個資；同時省去一組硬體。
- **[D2] MQTT payload 移除 employee_id**（v0.3）：改由後端配對 session 後寫入（即方案 D 的落地），去識別化在源頭即成立。

### 4.3 電梯搭乘追蹤（HTTPS）

> 依賴：App 既有 HTTPS 通道與登入身份｜`EMISSION_FACTOR`（台電電力係數）

#### 規格（現行定案）

- **識別**：NFC（近場通訊）手機感應。電梯內嵌 NFC 訊號發射器，員工以手機掃描完成識別與樓層紀錄。
- **流程**：進電梯時手機掃描 NFC → 手機紀錄進入樓層 → 出電梯時手機再次掃描 → 手機計算樓層差 → 透過 HTTPS 上傳至後端 REST API。
- **資料上傳**：資料產生於手機端，由 App 既有的 HTTPS 連線直接送進後端，**不經過 MQTT Broker**。
- **Payload**：employee_id、timestamp_in/out、floor_in/out。
- **碳排換算**：電力係數採台電年度公告；耗電 = 樓層差 × 電梯額定功率 × 平均行程時間；可計算「步行替代搭乘」節碳量。

#### 決策記錄（脈絡與依據）

- **[D1] 為何不經 MQTT Broker**：NFC 流程下資料主體為手機而非常駐 IoT 裝置，沿用 App 與後端的天然通道即可，無需繞經 Broker。
- **[D2] 識別方式由 BLE 改 NFC**（v0.2）：改以手機主動掃描 NFC 完成識別與樓層紀錄，傳輸同步改 HTTPS。

### 4.4 數位能耗監測 Desktop Agent（Eco-Agent）（MQTT / HTTPS）

> 依賴：App 登入身份與 QR 掃碼能力（4.4.2 綁定）｜5.1 批次寫入與冪等去重｜5.2 集中配置服務（參數下發）｜系統金鑰庫（Windows DPAPI／macOS Keychain）

#### 規格（現行定案）

背景常駐程式，三條感測路徑：

| 路徑 | 對象 | 方法 | 協定 |
|------|------|------|------|
| A | 電腦使用 | Windows `GetLastInputInfo()` / macOS `IOHIDSystem`、`HIDIdleTime` 判活躍/閒置＋跨平台 CPU 使用率（`gopsutil`），每固定區間 `computerUsageRecordInterval` 輪詢；**Agent 只送原始量（active/idle 時數、平均使用率、CPU 型號），能耗由後端以使用率加權模型計算**（見「電腦能耗模型」） | MQTT |
| B | 印表機（個人專屬機） | SNMP（UDP 161）查詢 OID `1.3.6.1.2.1.43.10.2.1.4`，前後頁數相減，以 Agent 綁定 employee_id 歸戶。共用機歸戶另循 Print Server Log／Pull Printing（可行、待實作測試）或改由 App 手動上傳用紙量（非本 Agent 路徑，屬使用者主動感測）——詳見決策記錄 [D6] | MQTT |
| C | 雲端儲存 | OAuth 2.0 授權，Google Drive API `about?fields=storageQuota`，儲存量 × PUE | HTTPS |

**三條路徑的感測模式（輪詢 vs 事件觸發）**

- **電腦使用（路徑 A）＝ 狀態值輪詢，短區間，分 active/idle 兩態**：`GetLastInputInfo()` 回傳「距上次輸入的間隔」，屬只能查詢的狀態值、無事件可掛，故採固定區間輪詢。區間由變數 `computerUsageRecordInterval` 控制（精度 vs 開銷的權衡：區間短則時數準但佇列筆數多，區間長則省資源但邊界誤差大），數值由 5.2 集中配置服務下發。
  - **誘因對齊「節能」而非「少用」**：每個輪詢區間依「距上次輸入的間隔」是否超過閾值，判定該區間為 **active（有互動）** 或 **idle（開機但無操作）**；Agent 累計 active/idle 兩類時數。碳排歸戶的重點是可避免的浪費（idle 開機——離座沒讓電腦睡），而非必要的工作使用（active）。此設計避免舊「活躍時間 × TDP」把「使用電腦」本身當成過錯、促使員工減少使用的誘因錯位。
  - **sleep／關機不由 Agent 計費（也做不到）**：電腦進入 sleep（S3）／hibernate（S4）／關機時，OS 連同 Agent 進程一併掛起，Agent 不執行、無從採集。故只需區分 active/idle 兩態即可——sleep/關機期間本無記錄產生，其低耗電（sleep 極低、關機為 0）自然不進帳。**「該睡沒睡」的浪費以 idle 時數形式被記錄；「有睡」則以「無記錄」獲得獎勵**，誘因（鼓勵休眠/關機）不需偵測 sleep 本身即達成。
  - **喚醒後以時間戳差分辨識掛起空白**：每次輪詢記 wall-clock 時間，若與上次輪詢間隔遠大於 `computerUsageRecordInterval`（如區間 60 秒卻跳了數小時），判定中間曾掛起，該段不計 active 也不計 idle（本無資料）。與 4.4.3 [D3]「綁相對年齡而非絕對時刻、關機只暫停計時」同一精神。
  - **電腦能耗模型（使用率加權；Agent 純感測、後端計算）**：能耗計算集中於後端（同 5.1「Agent 純感測、碳排計算集中後端」原則），Agent 只送原始量。後端模型為 `功率 ≈ P_idle + 使用率 × (P_active − P_idle)`，其中 `P_active` 以 CPU 型號查 TDP 對照表取得、`P_idle` 為其一比例（如 0.2×TDP），能耗 = Σ(區間時長 × 該區間功率)。此為線性近似（真實功耗曲線非線性），但比舊「活躍時間 × TDP」（系統性高估 2–5 倍、且對負載無感）準確得多，且能區分重度運算者與輕度文書者。
    - **即時功耗 fallback（預留、不在現階段實作）**：Intel RAPL（Linux `/sys/class/powercap/intel-rapl`）／Apple `powermetrics` 可讀即時封裝功耗，比 TDP 更準；但需權限、不跨平台、BYOD 多半不可行，列為「可用則用」的增強路徑，結構預留、現階段以使用率加權為準。報告中作為絕對精度提升方向陳述。
  - **屬流量量、關機不需補查**：電腦路徑屬**流量量**（關機期間本無時數可採，跳過不需補），故不套用路徑 C 的 deadline-check 補查模式。
- **雲端儲存（路徑 C）＝ 狀態值輪詢，長區間，採持久化時間戳到期判斷觸發**：`storageQuota` 同為只能查詢的狀態值。儲存用量變化緩慢、且每次查詢須走 OAuth＋HTTPS 呼叫外部 API 成本較高，故採**獨立的長區間**變數 `driveQuotaInterval`（24h），與電腦路徑分開配置、由 5.2 下發。兩條路徑變化速率不同，不共用同一間隔。
  - **不以絕對計時器（sleep 24h）實作**：Eco-Agent 極可能不會連續運行 24 小時（員工自行關機），絕對計時器會與已否決的「每日 23:00 打包」犯同一錯——裝置關機即錯過該次查詢。故改採**持久化時間戳 + 到期判斷（deadline check）**，與 4.4.3 [D3]「綁相對年齡而非絕對時刻」一致。
  - **實作**：Agent 將每次雲端查詢的時間戳 `lastDriveQuotaCheckAt` **寫入本機持久化儲存**（同 4.4.3 的 SQLite／落磁碟佇列，非僅記憶體）。此到期判斷**掛在 `checkInterval`（60 秒佇列巡檢）**——巡檢時除既有的達量／`maxAge` 檢查外，另判斷 `now() - lastDriveQuotaCheckAt >= driveQuotaInterval`；成立則查詢 Drive `storageQuota`、寫入佇列並更新時間戳。掛巡檢而非 `computerUsageRecordInterval`，是為職責分離（後者專責採集電腦活躍時間）。
  - **冷啟動與開機補查**：首次綁定後時間戳尚不存在，視為「已到期」（等同 0／null），第一次巡檢即查一次並寫入時間戳。裝置關機數日後開機，只要距上次查詢已超過 `driveQuotaInterval`，開機後首次巡檢自動補查一次——與 4.4.3「開機後檢查」觸發天然合流，無需另寫補查邏輯。
  - **僅狀態量長輪詢需要此模式**：電腦路徑（路徑 A）屬**流量量**（關機期間本無時數可採，跳過不需補），故不套用；此 deadline-check 模式精準只用於**長區間狀態量查詢**（雲端；未來印表機 SNMP 同屬狀態量長輪詢，屆時可套同一套）。
- **印表機（路徑 B）＝ 依印表機歸屬分軌，歸戶前提已定案（v0.11）**：能否用哪種感測形式取決於「列印能否歸戶到個人」——SNMP page counter 為累計值、無推播能力（故只能輪詢），且讀到的是**整台機器總頁數**而非個人用量。經團隊決議，路徑 B 分為以下幾軌（詳見決策記錄 [D5]、[D6] 與第 7 節）：
  - **優先開發**：
    - **個人專屬印表機（Eco-Agent 感測）**：可靠 Agent 綁定的 employee_id 直接歸戶，SNMP 中區間輪詢（`printerPollInterval`）即足夠。
    - **手動上傳用紙量（Eco-Sensing App）**：員工於列印前後在 App 內輸入並上傳用紙量，屬**使用者主動感測、須搭誘因**（比照 i 減碳任務以 EXP／碳幣激勵）；不依賴任何印表機基礎設施，為共用機環境的可行補位手段。
  - **未來實作、測試（列為可行但待實作）**：共用印表機要歸戶到人須改用帶 user 欄位的來源——**Print Server Log**（逐工作帶送出者身份，天生事件式，可訂閱 Windows PrintService/Operational Event ID 307）或 **Pull Printing API**（刷卡列印，如 PaperCut，釋放前刷證驗證使身份與工作在源頭綁定）。兩者技術上皆可行、且「事件觸發＋歸戶」同時成立，但受限於實驗場域基礎設施前提，列為待實作與測試項。

- **本地彙整與去識別化**：資料先寫入本機持久化佇列，於上傳前打包彙整——移除姓名/Email，僅保留員工 ID Token（符合個資合規）。上傳時機採多重觸發（不綁固定時刻），詳見 4.4.3。
- **MQTT Payload**（topic: `digital/agent/{employee_id}`）：date、pc_active_hours、pc_idle_hours、pc_avg_cpu_util、cpu_model、print_pages、drive_usage_gb。（電腦路徑改送原始量——active/idle 時數、平均 CPU 使用率、CPU 型號——不再送 `pc_tdp_w`；能耗由後端計算。）
- **碳排換算（集中於後端）**：電力（電腦：使用率加權功率模型 `P_idle + 使用率 ×(P_active − P_idle)` × 時數 × 台電係數，`P_active` 由 CPU 型號查 TDP 表；未來可由 RAPL/powermetrics 即時功耗覆蓋）＋ 列印（頁數 × 紙張生命週期係數）＋ 雲端（GB × PUE × 電力係數）。TDP 對照表、P_idle 比例、PUE、各項係數皆屬 `EMISSION_FACTOR`／係數配置，後端維護、不寫死於 Agent。
- **注意**：Google Drive 數據走 HTTPS 直接進後端 REST API，**不經過 MQTT Broker**。

##### 4.4.1 開發架構：Go

- Desktop Agent（代號 **Eco-Agent**）以 **Go** 開發（選型理由見決策記錄 [D1]）。
- **跨平台交叉編譯**：單機以 `GOOS` / `GOARCH` 環境變數即可編出 Windows / macOS / Linux 三平台執行檔。
- **BYOD 權限注意**：macOS 偵測使用者活動（`IOHIDSystem`、`HIDIdleTime`）需 Accessibility 授權；SNMP 查印表機需與印表機同網段——此兩點為 BYOD 部署的實際摩擦點，需在安裝引導中明確處理。

##### 4.4.2 裝置綁定機制（Device Enrollment）

Eco-Agent 為無人值守背景程式，身份綁定採「**一次綁定、長期常駐、雙向可解除**」的裝置註冊（device enrollment），不採傳統「每次開啟登入」模式。歸戶的 `employee_id` 由此機制建立。綁定方式採**方案 B（手機 App 掃碼綁定）**（選型理由見決策記錄 [D2]）。

**綁定階段流程**

1. 員工開啟 Eco-Agent → Agent 向後端索取一次性 `binding_code`（短效）。後端於 `BINDING_CODE` 表建立一筆記錄（`status=pending`、`created_at`、`expires_at = created_at + 5 分鐘`、`device_id` 指向索取的 Agent 裝置）。
2. Agent 將 `binding_code` 編入 **QR Code** 顯示於畫面（QR 內容採全系統統一 custom scheme URI，見 4.5）。
3. 員工以**已登入的 Eco-Sensing App** 掃碼；App 依 URI host/path 判定為綁定動作。
4. App 把**已驗證身份 + binding_code** 送後端。
5. 後端核對 `binding_code`（檢查 `status=pending` 且 `expires_at > now()`）→ 建立 `device_binding` → 回填 `BINDING_CODE.employee_id`、`device_binding_id`、`consumed_at`，並將 `status` 改為 `consumed`；發放 **Access Token（短期）+ Refresh Token（長期）**。過期或已消費的碼一律拒絕（防重放）。
6. Agent 取得 token，**Refresh Token 存入系統金鑰庫**（Windows DPAPI／macOS Keychain，**不寫純文字檔**）→ 轉入背景常駐。

> 安全核心：Agent 全程**只接觸一次性 `binding_code`，不接觸員工帳密或員工 ID**；真正的身份驗證在已登入的手機 App 上完成，後端負責配對，符合「純感測、不碰個資」原則。

**綁定碼儲存（`BINDING_CODE` 表）**

- `binding_code` **必須於後端持久化**（後端要能核對，就須先有記錄可比對），儲存於 Supabase `BINDING_CODE` 表（ERD 已納入）。
- 屬**短效一次性憑證**：`expires_at` 到期即失效（`bindingCodeTTL` = 5 分鐘）；App 掃碼核對成功即改 `status=consumed`，同碼不可重複使用。
- 過期記錄由背景排程或查詢時 lazy 判斷（`expires_at < now()` 視為 expired）。
- P1 直接存 Supabase 一張表即可，不為此提前引入 Redis（與 5.1 [D3] 一致）；P3 若導入 Redis，一次性短效碼恰為 Redis TTL key 的典型用途，屆時可遷移。

**Session 與登出（雙向可解除）**

- **「永久有效」以雙 token 實作，而非永久 token**：
  - Access Token（短期，**1 小時**）→ 每次上傳資料用。**後端簽發策略參數、不落庫**（可由 Refresh Token 隨時換發，DB 不儲存），與 `id_token` 無關。
  - Refresh Token（長期，**90 天**）→ 安全存本機（金鑰庫），後端僅存 `refresh_token_hash`，專供換新 Access Token；Access Token 過期自動續期，使用者無感。
  - **輪換策略：不啟用**——Refresh Token 90 天固定不變、到期即需重走綁定流程（重新掃碼）。輪換（每次續期換發新 Refresh、舊的作廢）帶來的複雜度與離線誤判風險，於數十人專題規模 > 收益，列為 P3 資安強化備選；主要威脅已由「每次上傳夾帶撤銷」覆蓋。
- **員工端登出**（本機操作）：換機時於 Agent 點「解除綁定」→ 清本機憑證 + 通知後端標記解綁。
- **企業端遠端登出 / 撤銷**（Web 後台）：員工離職、裝置遺失或異常時，IT 將該裝置標記 `revoked`。
- **撤銷生效機制（採每次上傳夾帶，不另做心跳）**：Agent 每次 flush 上傳時，後端於回應夾帶有效性狀態；若已被撤銷則回 `401/403`，Agent 收到即**自我清除憑證（含金鑰庫 Refresh Token）、停止上傳**。此檢查搭既有上傳往返「搭便車」，與 4.4.3 上傳回應、5.2 配置版本號夾帶共用同一條回應通道，零額外通道。
  - **離線裝置撤銷延遲容忍度**：撤銷延遲 = 下次上傳觸發之前的時間，上界 ≈ 4.4.3 的 `maxAge`（24h，裝置有開機前提下）。離線期間裝置本就送不出資料，不造成資料正確性風險；真正要防的「重新上線後還能送」在其一上線 flush 即被 `401/403` 擋掉，故延遲可接受。
  - （備選，未採）開機/喚醒後先發輕量狀態查詢以提前撤銷檢查，列為 P3 韌性強化備選。

**身份標識與去識別化**

- 綁定時取得的身份標識即作為去識別化打包保留的**員工 ID Token**；Agent 全程只持有不可逆 token，不直接持有員工 ID，強化「純感測、不碰個資」定位。

##### 4.4.3 資料上傳觸發模型：本地持久化佇列 + 多重觸發

**核心觀念**

- 不等待某個「打包時刻」，而是**感測資料一產生就寫入本機持久化佇列（落磁碟，非僅記憶體）**，再由多個觸發條件擇機批次上傳。關機／崩潰不再造成遺失，因資料已在磁碟，裝置下次醒來自然補送。
- 本機佇列以嵌入式儲存實作（Go 生態常見 **SQLite**：單檔、零外部服務，契合 Eco-Agent 輕量定位；或 append-only 檔）。

**上傳觸發條件（四者先到先觸發，皆不綁絕對時刻）**

| 觸發 | 角色 | 說明 |
|------|------|------|
| **累積達量** | 主力（省成本、自我調節） | 佇列達 N 筆／一定大小即 flush。跟著資料量走而非時鐘，自動適應使用強度（重度使用者一天多次、輕度使用者數天一次） |
| **關機／登出前 hook** | 兜底 | Windows shutdown event／macOS `NSWorkspaceWillPowerOffNotification`，趕在關機前搶送未達量的零頭 |
| **開機／喚醒後檢查** | 補送 | 裝置醒來先檢查佇列有無「上次未送完」者，立即補上 |
| **最長滯留時間（max age）** | 保底（取代固定 23:00） | 佇列最舊一筆超過 X 小時（例：24h）仍未上傳即 flush，防輕度使用者資料滯留過久、儀表板數字過舊 |

> **實作註記（Eco-Agent Step 1.3 觀察）**：各路徑採「狀態值輪詢／一天一筆累計事件」（事件 ID = `id_token + 日期 + 路徑類型`），每次輪詢以當日累計 upsert 覆蓋同一筆，故**單一路徑單日僅佔佇列 1 筆**。因此「累積達量（`thresholdCount`）」對**單一路徑單獨運作幾乎不會觸發**，該路徑實際靠「關機前 hook／開機後補送／最長滯留」送出；`thresholdCount` 要到**多路徑（A＋C＋B）齊跑、多筆匯集**時才成為主力觸發。此為狀態值輪詢模型的自然結果，非缺陷。

**至少一次送達（At-least-once）**

- 佇列資料**僅在後端回 200 確認後才標記已上傳並清除**；上傳失敗（離線、後端不可用）則保留，下次觸發重試。
- 每筆帶**唯一事件 ID**（可由 `id_token + 日期 + 路徑類型` 組出穩定鍵），後端 **upsert／冪等去重**，重複送達不重複計算——複用 5.1 既有的 MQTT 批次寫入冪等基礎。

**與既有設計的銜接**

- 與 4.2 廢棄物 session「逾時自動結算孤兒事件」同一思路：不假設 happy path，為中斷保留兜底。
- 與 4.4.2 撤銷機制天然整合：每次 flush 上傳都會收到後端回應，順帶夾帶撤銷狀態檢查（`401/403` 即自清憑證），一石二鳥。
- flush 間隔、累積量門檻、最長滯留時數等參數，屬 5.2 集中配置服務（`sensor_config`）可下發之 Eco-Agent 策略。

##### 4.4.4 集中配置參數（已定案）

以下參數由 5.2 集中配置服務（`sensor_config`）統一管理與下發（後端簽發策略類則由後端維護）。

**裝置綁定（後端簽發策略）**

| 中文 | 變數名／英文名 | 數值 |
|------|--------------|------|
| 短效綁定碼效期 | `bindingCodeTTL` | 5 分鐘 |
| 短期上傳憑證效期 | Access Token exp | 1 小時（不落庫、與 `id_token` 無關） |
| 長期換發憑證效期 | Refresh Token exp | 90 天（到期重走綁定，不輪換） |

**資料收集與上傳觸發（Eco-Agent）**

| 中文 | 變數名／英文名 | 數值 |
|------|--------------|------|
| 電腦使用量輪詢區間 | `computerUsageRecordInterval` | 60 秒 |
| 雲端儲存查詢區間 | `driveQuotaInterval` | 24 小時（非絕對計時器；以持久化時間戳 `lastDriveQuotaCheckAt` 於 `checkInterval` 巡檢時做到期判斷觸發，見 4.4「三條路徑的感測模式」路徑 C） |
| 佇列巡檢區間 | `checkInterval` | 60 秒 |
| 累積數量門檻 | `thresholdCount` | 60 筆 |
| 資料最長滯留時間 | `maxAge` | 24 小時 |
| 印表機輪詢區間 | `printerPollInterval` | 供「個人專屬印表機（Eco-Agent SNMP 輪詢歸戶）」路徑使用；中區間，實測後定值（歸戶前提已定案，見 4.4 決策記錄 [D6]） |
| 單次上傳批量上限 | `uploadBatchMax` | 720 筆 |

**重試策略（採用「搭下次觸發重送」，無獨立重試迴圈）**

- **不設獨立重試計時器**：上傳失敗（離線／後端不可用）的資料留在本機佇列，搭下一次正常觸發（達量／巡檢／關機前／開機後）一起重送。
- **不設最大重試次數上限**：送不出即持續保留至成功（佇列膨脹由 `maxAge` 與離線期間無新資料自然封頂）；不做「N 次後丟棄」，避免「至少一次送達」出現資料遺失破口。
- **不採指數退避（exponential backoff）**：重試節奏跟隨既有稀疏觸發（最密為 `checkInterval` 60 秒），無密集重試迴圈需退避，故不需要。

> 測試調參原則：測試期將時間類參數大幅縮短以便在數分鐘內觀察完整流程（Access Token 縮 2–5 分、`driveQuotaInterval` 縮 1–2 分、`maxAge` 縮 2–3 分、輪詢/巡檢縮到秒級、`thresholdCount` 縮到個位數），正式再放回。並專測兜底路徑：關機前 hook、開機後補送、撤銷生效（`401/403` 自清）、冪等去重、Binding Code 過期/重放。

#### 決策記錄（脈絡與依據）

- **[D1] 為何採 Go 而非沿用 App 端的 Flutter**（v0.4）：
  - Eco-Agent 是**無頭背景常駐程式（daemon / system tray）**，幾乎無 UI，Flutter 為 GUI 而生的引擎開銷（Skia、Dart runtime，idle 即約 100MB+）對此純屬浪費。
  - Go 編譯為**單一靜態執行檔、零外部 runtime 依賴**，BYOD 情境下「下載一個檔、雙擊即跑」，部署摩擦最低（相較 Python 需 interpreter、.NET 需 framework）。
  - Idle 記憶體約 10–20MB；**goroutine** 天生契合三條並行感測路徑，記憶體開銷遠低於 OS 執行緒模型。
  - 生態系成熟：MQTT（`paho.mqtt.golang`）、SNMP（`gosnmp`）、OAuth2（`golang.org/x/oauth2`）、system tray、開機自啟皆有現成函式庫；跨平台原生呼叫經 `golang.org/x/sys/windows` 與 cgo（macOS IOKit）。
- **[D2] 為何棄「只輸入員工 ID 核對」、改採方案 B 掃碼綁定**（v0.4）：
  - 員工 ID 非機密（識別證、Email 可見），僅憑 ID 綁定會讓任何人把碳排灌到他人名下，對 Shared Savings（利潤分享）系統等同開作弊後門。
  - 改採「一次性驗證」：身份驗證僅在綁定當下發生一次，綁定後不再需要。
  - 方案 B 複用 App 既有基礎設施：員工端 App 已有完整登入身份系統（`DemoAuthStorage`、`currentUserProvider`）與 QR 能力（`QRCodePopup` 產生、回收計重的掃碼相機流程），開發成本最低；公司電腦與 BYOD 皆適用。
- **[D3] 為何拿掉固定「每日 23:00 打包」、改採多重觸發**（v0.7）：Eco-Agent 運行於員工桌面（含 BYOD 筆電），**無法保證任一固定時刻（如 23:00）裝置為開機狀態**——員工可能提早關機、週末不開、或長期 sleep，綁死絕對時鐘的排程會直接錯過而產生資料死角。原排程「累積一天再批次上傳以省成本」的目的，已由「累積達量」以更合理的方式達成（跟資料量走、無時鐘死角）；「每日一次」的節奏感則由「最長滯留時間」保住——後者綁定的是**資料的相對年齡**而非絕對時刻：裝置關機只會暫停計時、開機後繼續，不會像固定時鐘那樣直接錯過。
- **[D4] 印表機路徑備選方案**：SNMP（最精確，需網路印表機）／廠商 API（HP、Epson SDK，需個別串接）／Print server log（最簡單但有延遲）。目前主選 SNMP。
- **[D5] 印表機感測模式（輪詢 vs 事件觸發）與歸戶前提**（v0.9）：曾評估以「影印事件觸發」取代輪詢（只在真的列印時記錄、免無用輪詢）。結論：能否事件觸發取決於感測源——SNMP page counter 為累計值、無推播能力，故 SNMP 路徑只能輪詢；真正的事件式來源是 Print server log（有列印工作即寫一筆日誌）。但更關鍵的先決問題是歸戶：SNMP 讀到的是**整台機器總頁數**，無法辨識是誰印的。個人專屬印表機可靠 Agent 綁定的 employee_id 直接歸戶（SNMP 輪詢即足夠）；共用印表機要歸戶到人，須改用帶 user 欄位的來源（Print server log 或刷卡列印 / pull printing），此時「事件觸發＋歸戶」才同時成立——與 4.2 廢棄物「開 session 才能歸戶」同構。歸戶前提已於 v0.11 定案，見 [D6]。
- **[D6] 印表機路徑 B 歸戶前提定案與開發優先序**（v0.11）：經團隊決議，路徑 B 採「分軌並依基礎設施前提排優先序」，並新增一條不依賴任何印表機設施的備選：
  - **新增備選「手動上傳用紙量」**：員工於列印前後在 Eco-Sensing App 內輸入並上傳用紙量，定位為**使用者主動感測、須搭誘因**（比照 i 減碳任務以 EXP／碳幣激勵）。此路徑無須網路印表機、伺服器或 pull printing 系統，是共用機或缺乏集中列印設施場域的可行補位。權衡：資料完整度依賴員工自覺與誘因設計，屬「主動感測」的天生取捨（同 4.1 差旅單據上傳），故列為可行選項而非唯一手段。
  - **優先開發**：「個人專屬印表機感測（Eco-Agent，SNMP 輪詢歸戶）」與「手動上傳用紙量（Eco-Sensing App）」——兩者皆不依賴實驗場域尚未確定的集中列印基礎設施，可立即推進。
  - **列為可行、待實作測試**：共用機的 **Print Server Log** 與 **Pull Printing API** 兩路徑技術上皆可行（Go 條件下：前者可經 `golang.org/x/sys/windows` 呼叫 Windows Event Log API／`wevtutil`／PowerShell 訂閱 Event ID 307,或由後端集中採集；後者為純 HTTPS/REST 串接商業列印管理系統 API,對 Go 最友善,擺在後端 FastAPI 側較個別 Agent 合理），但受限於實驗場域是否具備集中列印伺服器或 pull printing 系統之前提,於未來報告中列為「可行但待實作、測試」,不納入現階段優先開發。
- **[D7] 電腦能耗模型：棄「活躍時間 × TDP」、改「使用率加權」（active/idle 分態，Agent 純感測、後端計算）**（v0.13）：
  - **問題一：誘因錯位**。舊式「活躍時間 × TDP」實質懲罰「使用電腦」本身——活躍時間越長碳排越高，會促使員工減少使用，而非節能。但員工工作本就需用電腦，該獎勵的是「不用時讓電腦休眠/關機」，而非少用。故改為區分 **active（有互動）／idle（開機無操作）** 兩態，歸戶重點放在可避免的浪費（idle 開機），對齊「節能減碳」而非「減少使用」。
  - **問題二：TDP 高估**。TDP 是散熱設計功耗（滿載標稱值），日常辦公負載 CPU 多在低使用率，用 TDP 當使用功率**系統性高估約 2–5 倍**，且對負載差異無感（重度運算者與輕度文書者估值相同）。改採**使用率加權** `P_idle + 使用率 ×(P_active − P_idle)`：CPU 使用率跨平台易取得（Windows PDH／macOS `host_statistics`，`gopsutil` 一套介面，免特殊權限），大幅收斂偏差且能區分負載。TDP 仍作為 `P_active` 上界代理（型號查表取得）。
  - **即時功耗（RAPL/powermetrics）作 fallback**：更準但需權限、不跨平台、BYOD 多不可行，列「可用則用」增強，結構預留、現階段以使用率加權為準；報告中作為絕對精度提升方向。
  - **sleep/關機不由 Agent 計費**：sleep（S3）/hibernate（S4）/關機時 Agent 進程被掛起、不運作，本就無從採集；其低耗電自然不進帳。「該睡沒睡」以 idle 時數被記錄、「有睡」以無記錄獲獎勵，誘因不需偵測 sleep 本身即達成。喚醒後以 wall-clock 時間戳差分辨識掛起空白（該段不計）。
  - **Agent 純感測、後端計算（採方案 b）**：依 5.1「Agent 純感測、碳排計算集中後端」與 5.2「係數後端維護下發」原則，Agent 只送原始量（active/idle 時數、平均 CPU 使用率、CPU 型號），TDP 表／P_idle 比例／電力係數皆屬後端係數配置，能耗由 FastAPI 碳排引擎計算。payload 因此以原始量取代舊 `pc_active_hours × pc_tdp_w`。

---

### 4.5 QR Code 統一辨識模式（跨模組共用決策）

> 依賴：Eco-Sensing App 掃碼相機流程（`QRCodePopup` 產生／回收計重掃碼）｜`app_links` 套件（deep link 格式約定）

#### 規格（現行定案）

- **全系統統一採單一 custom scheme URI 格式**編碼所有 QR 內容。無論綁定碼、垃圾桶 bin QR、員工識別 QR，掃描時**一律開啟／使用 App**，再由 App 依 URI 的 host/path **判斷動作並分流處理**（綁定 / 廢棄物歸戶 / 身份識別等）。
- QR 內容以可辨識的 URI 前綴（host/path）標明用途，App 內掃碼邏輯解析 URI、取出參數（如 `binding_code`、`bin_id`）後走既有流程（如綁定走 HTTPS 送後端核對）。
- **複用既有基礎設施**：App 既有的掃碼相機流程（1.4 掃描頁、回收計重掃碼、`QRCodePopup`）與 `app_links` 套件（1.8 已用於電梯 NFC deep link，支援 App 冷啟動與背景喚醒）。`app_links` 的 URI scheme 規範沿用作為**格式約定**。

#### 決策記錄（脈絡與依據）

- **[D1] 為何統一 URI 格式而非各模組各自為政**（v0.10）：同一支 App、同一支相機，靠 URI host/path 即可分流多種掃碼動作，維護單一格式最簡潔；避免每加一種 QR 就多一套解析邏輯。
- **[D2] 綁定情境下 deep link 喚起 vs App 內相機掃碼的釐清**（v0.10）：電梯 NFC 場景是「實體 tag 觸發 → OS 喚起本機 App」（App 為被開啟方）；Agent 綁定場景是「電腦螢幕顯示 QR → 手機 App 主動掃碼」（App 已開著、自身相機在掃）。後者實際觸發以 **App 內相機掃描解析**為主，`app_links` 的冷啟動／背景喚醒能力在此用不到；真正複用的是掃碼相機流程與 URI 格式約定。

---

## 5. 技術堆疊與橫切層（Tech Stack & Cross-cutting）

| 層級 | 技術 |
|------|------|
| 前端 App | Flutter（Android / iOS / Web 三平台），狀態管理 Provider / Riverpod |
| Edge AI | YOLOv8n（Python，樹莓派本地推論）、OpenCV、Tesseract OCR |
| 大語言模型 | OpenAI GPT-4o（OCR 後 NER、廢棄物 Fallback 判定） |
| IoT 傳輸 | MQTT（Mosquitto Broker）、NFC（近場通訊）、SNMP |
| 外部 API | TDX 運輸 API、Google Maps API、Google Drive API v3 |
| 後端 | **FastAPI（Python，async）** ＋ **Supabase（代管 PostgreSQL）** ＋ 碳排運算引擎 ＋ 係數資料庫 ＋ MQTT consumer 批次寫入（詳見 5.1） |
| 控制架構 | SD-IoT Controller（控制面／數據面分離；自建輕量版，實作為 FastAPI 內管理模組，詳見 5.2） |
| Desktop Agent | **Go**（單一靜態執行檔，跨平台交叉編譯）；MQTT/SNMP/OAuth2 函式庫；DPAPI／Keychain 憑證保護 |

### 5.1 後端框架與資料庫：FastAPI + Supabase

> 服務對象：四大模組全部的資料入庫、運算與查詢（橫切層，所有裝置經 FastAPI 進資料庫）。已由團隊成員完成初步部署，現有三段網址：託管平台（Space）、API base URL、Swagger 自動文件（`{base_url}/docs`，由 FastAPI 依路由與 Pydantic 模型自動生成）。

#### 規格（現行定案）

- 後端採 **FastAPI（Python，async）**；資料庫採 **Supabase（代管 PostgreSQL）**，ERD（`eco_sensing_erd.mmd`）直接對應建表。
- Supabase 提供 connection pooler（Transaction mode，port 6543）；FastAPI **一律走 pooler 連線**，避免 async 高併發耗盡資料庫連線數。
- **架構分工原則**：Supabase 管「資料存哪裡」；FastAPI 管「資料進來後怎麼算」——請求驗證、排放係數查詢、CO₂e 計算、廢棄物 session 配對歸戶、獎勵（EXP／碳幣）發放。App、樹莓派、Eco-Agent 一律經 FastAPI 進資料庫，**不直接讀寫 Supabase**，維持商業邏輯集中與控制面／數據面分離。
- **資料寫入策略**：MQTT Broker（Mosquitto）本身即為天然緩衝（訊息佇列）：後端 MQTT consumer 訂閱 topic → 記憶體佇列累積 → 定時／定量**批次寫入（batch insert）** Supabase。App 端 HTTPS 事件（差旅上傳、NFC 電梯、廢棄物 session 開啟／投入完畢）為低頻請求，即時直寫。**寫入端不設獨立 cache 層**（評估依據見決策記錄 [D3]）。
- **讀取端快取**：排行榜與企業端儀表板的聚合查詢採**讀取端快取**（TTL 約 5 分鐘），初期以 FastAPI 行程內記憶體快取實作，規模擴大後升級 Redis（sorted set 天生適合排行榜）。
- 容錯備註：批次佇列在後端崩潰時可能遺失數秒內未落地資料（碳排數據可容忍）；如需強化，將 MQTT QoS 設為 1 並延後 ack，由 Broker 保留未確認訊息。

**分階段開發步驟**

| 階段 | 目標 | 主要工作項目 | 完成判準 | 狀態 |
|------|------|--------------|----------|------|
| P1 直通版 | API 跑通、資料落地 | Supabase 依 ERD 建表；FastAPI 實作四大模組寫入／查詢 API（逐筆直寫，不加緩衝）；以 Swagger 測通全部端點；App 假資料改串真 API | 四大模組資料皆可經 API 寫入並查回 | ⬜ 未開始 |
| P2 批次與快取版 | 效能與穩定 | MQTT consumer ＋ 記憶體佇列批次寫入（廢棄物、Desktop Agent）；排行榜／儀表板讀取端快取（TTL 5 分）；廢棄物 session 逾時結算與互斥鎖落地 | 批次寫入上線；儀表板重複查詢不重算 | ⬜ 未開始 |
| P3 擴充版（視規模啟用） | 大規模部署韌性 | 導入 Redis（排行榜 sorted set、跨實例共享快取）；MQTT QoS／重送策略；基本監控與告警 | 多後端實例部署下快取結果一致 | ⬜ 未開始 |

#### 決策記錄（脈絡與依據）

- **[D1] FastAPI 選型理由**（v0.5）：async 非同步 I/O 適合大量裝置並發上傳的 IoT 場景；Pydantic 自動驗證請求欄位與型別，減少手寫防呆；自動生成 OpenAPI／Swagger 互動文件，前後端分工對接與測試成本低；與 Edge AI（YOLOv8n）、OCR 前處理腳本同為 Python，工具鏈一致。
- **[D2] Supabase 選型理由**（v0.5）：免自架、免管備份的雲端 PostgreSQL；附帶 Auth、Row Level Security、Realtime、Storage，未來可漸進採用。
- **[D3] 寫入端不設獨立 cache 層的評估**（v0.5）：專題規模（實驗場域數十名員工）之寫入頻率低——Desktop Agent 以本地佇列批次上傳（累積達量／關機前／開機後／最長滯留觸發，詳見 4.4.3）、其餘模組皆為事件驅動——寫入端不需獨立 cache 層，避免過早最佳化增加故障點。讀取端才是快取重點。

### 5.2 SD-IoT Controller：FastAPI 內建管理模組（自建輕量版）

> 服務對象：四大模組全部的註冊、配置、策略與監控（橫切層）。

#### 規格（現行定案）

**定位總結（一句話）**

> SD-IoT Controller ＝ FastAPI 內的管理模組（裝置註冊／綁定撤銷 ＋ 配置與策略下發 ＋ 係數庫 ＋ 健康監控），透過 MQTT retained topic 與 HTTPS 回應夾帶兩條通道對數據面施加控制；數據面 ＝ 四顆 SVS 的資料上行、MQTT consumer 批次寫入與碳排運算引擎。

- Controller 實作為 FastAPI 後端內的一個邏輯模組（例如 `controller/` package），**不另建獨立服務**：控制面與數據面的「分離」是**邏輯分離**（模組邊界、職責劃分），而非部署分離（獨立行程／主機）。

**控制面／數據面職責劃分**

| 層面 | 職責 | 在 Eco-Sensing 中的對應 |
|------|------|------------------------|
| 數據面（Data Plane） | 實際資料流與運算 | 四顆 SVS 的資料上行（MQTT topics、HTTPS 資料端點）、MQTT consumer 批次寫入、碳排運算引擎、廢棄物 session 配對歸戶 |
| 控制面（Controller） | 感知器的註冊、配置、策略、監控、生命週期 | 裝置註冊與綁定／解綁／遠端撤銷、排放係數庫維護與下發、參數配置（session 逾時、信心度閾值、批次參數）、裝置健康監控（`last_seen`／心跳） |

**既有設計已覆蓋的控制面功能（無需重工）**

1. **裝置生命週期管理**：`DEVICE`、`DEVICE_BINDING` 資料表，Eco-Agent 綁定／解綁／遠端撤銷（上傳回應夾帶撤銷狀態、`401/403` 自清憑證）流程（4.4.2）。
2. **係數下發**：排放係數庫「後端維護、不寫死於前端」（4.1、`EMISSION_FACTOR`），即控制面對數據面的配置下發。
3. **裝置監控雛形**：`DEVICE.last_seen` 與企業端「感測器管理」頁（在線率、異常設備）作為控制面監控視圖（目前假資料，待串真）。

**待補的核心工作：集中配置服務（Config / Policy Service）**

將散落於各裝置端寫死的參數收進後端 `sensor_config`（policy）表，由 Controller 統一管理與下發：

| 對象 | 可配置參數（範例） |
|------|--------------------|
| 樹莓派（廢棄物） | YOLOv8n 信心度閾值（Fallback 觸發點）、秤重觸發靈敏度 |
| 廢棄物 session | 逾時秒數、互斥鎖（方案 C）啟用開關——「視部署規模啟用」即典型控制面策略開關（policy toggle），由 Controller 決定而非改 code 重佈 |
| Eco-Agent | `thresholdCount`、`maxAge`、`checkInterval`、`computerUsageRecordInterval`、`driveQuotaInterval`、`uploadBatchMax`、`printerPollInterval`（已定案值見 4.4.4） |
| MQTT consumer | batch flush 間隔、批量上限、QoS 等級 |

**配置下發通道（依裝置性質分流，呼應混合協定架構）**

- **樹莓派**：MQTT **retained message** 發佈至 `config/bin/{id}` 類 topic——裝置一連線即取得最新配置，Controller 改參數即時推送；Mosquitto 原生支援，實作成本極低，為最具 SDN 特徵的控制通道。
- **Eco-Agent／App**：走 HTTPS——開機拉取一次，之後每次上傳時後端回應**夾帶配置版本號**，版本不符再拉取；複用 4.4.2 既有的「撤銷狀態夾帶於上傳回應」機制，一石二鳥。

**開發排程**

- 配置服務與心跳監控掛在 **P2 階段**（與 session 逾時、互斥鎖落地同期），不新增獨立階段；Roadmap 第二步「實作 SD-IoT 架構與 Controller」即對應本節的邏輯模組化落地。

#### 決策記錄（脈絡與依據）

- **[D1] 為何自建輕量版、不採 ONOS／OpenDaylight 等既有 SDN 框架**（v0.6）：該類框架以網路交換器管理為目標，與本專題「員工行為虛擬感知器」場景不匹配，導入成本遠大於效益。
- **[D2] 為何邏輯分離而非部署分離**（v0.6）：專題規模（數十名員工）下，控制面／數據面拆成獨立行程或主機並無效益；以模組邊界與職責劃分達成同等的架構清晰度。
- **[D3] SVS 涵蓋範圍之概念釐清**（v0.6）：SVS 涵蓋**全部四大模組**——差旅 OCR 軌（人＋手機＋OCR pipeline）在 SVS 框架下同為一顆虛擬感知器，其「感測元件」是員工與手機而非常駐硬體。不以「狹義 IoT 硬體」篩選模組，否則 SVS 的硬體解耦抽象即失去意義。
- **[D4] 「資源配置」的重新詮釋**（v0.6）：學術文獻中 SD-IoT 的 resource allocation 多指頻寬與運算資源排程，於本專題規模無實際意義。本專題將其**重新詮釋為可配置的採樣頻率、批次參數與 QoS 指派**——實質為資源使用的調控，且皆經由集中配置服務實現。

---

## 6. 開發步驟（Roadmap）

| 階段 | 目標 | 主要產出 | 狀態 |
|------|------|----------|------|
| 第一步 | Eco-Sensing App GUI | Flutter 三平台 App，先用假資料撐 UI（按鈕事件簡易回饋），Provider/Riverpod 狀態管理 | 🟡 進行中 |
| 第二步 | 銜接硬體（IoT） | 智能垃圾桶 QR Code 模組、電梯 NFC；實作 SD-IoT 架構與 Controller | 🟡 進行中 |
| 第三步 | 建立伺服器 | Supabase（PostgreSQL）建表、FastAPI 服務與碳排運算引擎、MQTT consumer 批次寫入（依 5.1 分階段 P1→P2 推進） | 🟡 進行中 |
| 第四步 | 串接 API | 同步後端與前端數據；串接大語言模型 API | ⬜ 未開始 |
| 第五步 | 測試與實驗 | 系統穩定性、防呆機制測試；導入學校/中小企業實驗環境並記錄成果 | ⬜ 未開始 |

> 狀態欄位請隨進度更新：⬜ 未開始 / 🟡 進行中 / ✅ 完成

---

## 7. 待釐清議題（Open Questions）

> 標籤格式：`[歸屬章節][狀態]`。狀態：待討論／待設計／待實測／待安排／已決議。

- [5.1][已決議] ~~後端框架與資料庫選型尚未定案~~ → **FastAPI + Supabase（PostgreSQL），詳見 5.1**。
- [5.1][待實測] 後端批次寫入參數（flush 間隔、批量上限）與排行榜快取 TTL，待實測調整；Redis（P3）的導入門檻（部署規模）待定。
- [共用][待討論] 碳排係數資料庫的更新機制與來源權威性如何維護？
- [5.2][已決議] ~~SD-IoT Controller 的具體實作方式（自建 vs 既有框架）？~~ → **自建輕量版，實作為 FastAPI 內管理模組（控制面／數據面為邏輯分離），配置經 MQTT retained message 與 HTTPS 回應夾帶下發，詳見 5.2**。
- [5.2][待設計] `sensor_config`（policy）表的欄位設計與配置版本號機制（全域版本 vs 分裝置版本），待 P2 實作時訂定。
- [專案][待安排] 實驗環境的取得（學校場域或合作企業）與受測員工招募。
- [共用][待討論] 個資與資安：NFC 樓層追蹤、Desktop Agent 監測的員工知情同意與合規邊界。
- [共用][待設計] GDT 與 Shared Savings 的量化模型與回饋機制設計細節。
- [4.2][待實測] 廢棄物模組 session 逾時秒數（孤兒事件自動結算門檻）與互斥鎖（方案 C）是否依部署規模啟用，待實測決定。
- [4.4][已決議] ~~Eco-Agent 綁定碼短效時長、Access/Refresh Token 期限與輪換策略~~ → **`bindingCodeTTL` 5 分鐘、Access Token 1 小時、Refresh Token 90 天（到期重綁、不輪換），詳見 4.4.4**。綁定碼儲存於 `BINDING_CODE` 表（詳見 4.4.2、ERD）。
- [4.4][已決議] ~~Eco-Agent 撤銷狀態的回傳時機與離線撤銷延遲容忍度~~ → **採每次上傳夾帶（不另做心跳）；離線撤銷延遲上界 ≈ `maxAge`（24h），延遲期間裝置本就送不出資料，可接受，詳見 4.4.2**。
- [4.4][已決議] ~~Eco-Agent 上傳觸發參數（computerUsageRecordInterval、driveQuotaInterval、累積量門檻、最長滯留時數）~~ → **已定案值詳見 4.4.4**（本地佇列儲存選型 SQLite vs append-only 檔仍待實測；唯一事件 ID 組成鍵與後端冪等去重策略待與 5.1 批次寫入對齊）。
- [4.4/5.2][待釐清] Eco-Agent 開機是否強制拉取配置一次，以及拉取失敗的容錯處理（沿用上一版配置 vs 阻擋上傳 vs 重試），待設計。
- [4.4][已決議] ~~印表機（路徑 B）碳排的歸戶前提~~ → **分軌並依基礎設施前提排優先序**：新增備選「手動上傳用紙量」（App 內列印前後輸入上傳,使用者主動感測、須搭誘因）；**優先開發**「個人專屬印表機感測（Eco-Agent SNMP 輪詢歸戶）」與「手動上傳用紙量（Eco-Sensing App）」；共用機的 **Print Server Log** 與 **Pull Printing API** 技術上可行,於未來報告列為「可行但待實作、測試」,不納入現階段優先開發。詳見 4.4 決策記錄 [D5]、[D6]。
  - 承上,實驗場域印表機屬個人或共用、以及共用機是否具備集中列印伺服器 / pull printing 系統,待場域確定後回填,以決定兩條共用機路徑的實作可行性。

---

## 8. 版本紀錄

| 日期 | 版本 | 變更摘要 |
|------|------|----------|
| 2026-06-18 | v0.1 | 初版，整理自技術架構文件與開發步驟 |
| 2026-06-18 | v0.2 | 電梯模組識別方式改為 NFC（手機掃描）、傳輸改 HTTPS（手機 → 後端，不經 MQTT）；技術堆疊 BLE→NFC；Roadmap 第 1–3 步更新為進行中 |
| 2026-06-19 | v0.3 | 4.2 廢棄物辨識識別流程改為「員工掃桶上 QR → 投入 → App 點投入完畢 → 樹莓派上傳並歸戶」；採推薦組合 A（session 綁定）＋C（互斥鎖防併發）＋D（後端中介配對）＋G（重力觸發辨識＋投入完畢結算）；移除 USB QR Code 掃描器；MQTT payload 移除 employee_id（改後端配對寫入）、新增 App 端 session 事件；第 7 節新增 session 逾時與併發鎖待定項 |
| 2026-06-30 | v0.4 | 新增 4.4.1 Desktop Agent（Eco-Agent）架構選型（採 Go，非 Flutter）與 4.4.2 裝置綁定機制（採方案 B 手機 App 掃碼綁定、雙 token session、雙向可解除/遠端撤銷、憑證存系統金鑰庫）；技術堆疊新增 Desktop Agent/Go 一列；第 7 節新增 binding_code 與撤銷時機待定項；ERD 同步新增 DEVICE_BINDING 實體 |
| 2026-07-04 | v0.5 | 新增 5.1 後端選型決議（FastAPI + Supabase/PostgreSQL、connection pooler、MQTT consumer 批次寫入、讀取端快取策略）與 P1–P3 分階段開發步驟；技術堆疊後端列更新；Roadmap 第三步產出對齊；第 7 節後端選型項標記已決議並新增批次/快取參數待定項 |
| 2026-07-05 | v0.6 | 新增 5.2 SD-IoT Controller 實作決議（自建輕量版：FastAPI 內管理模組、控制面／數據面邏輯分離職責表、SVS 涵蓋四大模組之概念釐清、既有控制面功能盤點、集中配置服務與雙通道下發設計、資源配置重新詮釋、掛入 P2 排程）；技術堆疊控制架構列更新；第 7 節 Controller 項標記已決議並新增 sensor_config 表設計待定項 |
| 2026-07-07 | v0.7 | 新增 4.4.3 資料上傳觸發模型（本地持久化佇列 + 多重觸發：累積達量／關機前 hook／開機後檢查／最長滯留時間，取代原「每日 23:00 打包」；至少一次送達 + 唯一事件 ID 冪等去重）；同步更新 4.4 主體、4.4.2、5.1、5.2 中所有「每日 23:00 打包」舊敘述以維持一致；第 7 節新增上傳觸發參數待定項 |
| 2026-07-08 | v0.8 | **結構重整（內容不增刪）**：新增「現況快照」總覽表與第 0 節閱讀約定；各模組（4.1–4.4）與橫切層（5.1、5.2）固定分「規格（現行定案）／決策記錄（脈絡與依據）」兩段，決策推理（Go vs Flutter、方案 B 綁定、拿掉 23:00、FastAPI/Supabase 選型、Controller 自建等）自規格中剝離為 [D] 條目；各模組加依賴宣告；第 5 節標題改為「技術堆疊與橫切層」；第 7 節議題加 `[章節][狀態]` 標籤 |
| 2026-07-09 | v0.9 | 4.4 規格段新增「三條路徑的感測模式」：電腦（`computerUsageRecordInterval`，短區間）與雲端（`driveQuotaInterval`，長區間）皆為狀態值輪詢、由 5.2 集中配置獨立下發；印表機為輪詢並依歸戶前提決定形式。決策記錄新增 [D5]（印表機輪詢 vs 事件觸發與歸戶前提）；第 7 節新增印表機歸戶前提待討論項、上傳觸發參數項補入 driveQuotaInterval |
| 2026-07-09 | v0.10 | **系統設計最後確認事項（四事項落定）**：(1) 4.4.2 綁定碼儲存機制——`binding_code` 持久化於後端 `BINDING_CODE` 表（5 分鐘 TTL、consumed/expired 狀態、防重放），ERD 同步新增 `BINDING_CODE` 實體與關聯；(2) 新增 4.5 QR Code 統一辨識模式（全系統單一 custom scheme URI、掃描一律開/用 App 依 host/path 分流、複用掃碼相機與 `app_links` 格式約定）；(3) 4.4.2 撤銷時機定案為「每次上傳夾帶、不另做心跳」，明訂離線撤銷延遲上界 ≈ maxAge；(4) 新增 4.4.4 集中配置參數（已定案）：bindingCodeTTL 5 分、Access 1h、Refresh 90 天不輪換、computerUsageRecordInterval 60s、driveQuotaInterval 24h、checkInterval 60s、thresholdCount 60、maxAge 24h、uploadBatchMax 720、printerPollInterval 待啟用、重試不設上限不退避。現況快照同步新增四列；第 7 節對應四項標記已決議、新增開機拉取配置待釐清項 |
| 2026-07-10 | v0.11 | **印表機（路徑 B）碳排歸戶前提定案**：新增備選「手動上傳用紙量」（Eco-Sensing App 內列印前後輸入上傳，定位為使用者主動感測、須搭誘因）；決議**優先開發**「個人專屬印表機感測（Eco-Agent SNMP 輪詢歸戶）」與「手動上傳用紙量（App）」，共用機的 **Print Server Log** 與 **Pull Printing API** 兩路徑列為技術可行、待實作與測試（於未來報告呈現）。更新 4.4 印表機路徑 B 規格段（改為分軌並排優先序）、修正 4.4「三條感測路徑」表格 B 列（限定為個人專屬機 SNMP 歸戶、註記共用機與手動上傳另有出路且手動上傳非 Agent 路徑）、新增決策記錄 [D6] 並收束 [D5]、調整 4.4.4 `printerPollInterval` 說明；現況快照新增「印表機歸戶」一列；第 7 節印表機歸戶前提項標記已決議、保留場域基礎設施待回填子項 |
| 2026-07-16 | v0.12 | **雲端查詢觸發模型修正**：`driveQuotaInterval`（24h）改為**非絕對計時器**——因 Eco-Agent 極可能不連續運行 24h（員工自行關機），沿用絕對計時器會與已否決的「每日 23:00 打包」犯同一錯（關機即錯過該次查詢）。改採**持久化時間戳 `lastDriveQuotaCheckAt` + 到期判斷（deadline check）**，掛在 `checkInterval`（60 秒巡檢）時判斷 `now() - lastDriveQuotaCheckAt >= driveQuotaInterval` 才查詢，與 4.4.3 [D3]「綁相對年齡而非絕對時刻」一致；冷啟動視為已到期、開機補查與 4.4.3「開機後檢查」自動合流；僅長區間狀態量（雲端；未來印表機 SNMP）需此模式，電腦路徑屬流量量不套用。更新 4.4「三條路徑的感測模式」路徑 C 條目與 4.4.4 `driveQuotaInterval` 列註記 |
| 2026-07-17 | v0.13 | **電腦能耗模型修正（路徑 A）**：棄「活躍時間 × TDP」，改**使用率加權** `P_idle + 使用率 ×(P_active − P_idle)`——舊式誘因錯位（懲罰使用電腦本身、促使少用而非節能）且 TDP 系統性高估 2–5 倍、對負載無感。新模型分 **active/idle 兩態**（歸戶可避免的 idle 浪費、對齊節能），sleep/關機期間 Agent 被掛起不計費、喚醒以時間戳差分辨識空白；CPU 使用率跨平台易取得（`gopsutil`）；**即時功耗（RAPL/powermetrics）作 fallback、預留不實作**。採**方案 b：Agent 純感測、後端計算**——Agent 只送原始量（active/idle 時數、平均 CPU 使用率、CPU 型號），能耗由後端算。更新 4.4 路徑 A 表格列與感測模式段、MQTT payload（`pc_active_hours`/`pc_idle_hours`/`pc_avg_cpu_util`/`cpu_model` 取代 `pc_tdp_w`）、碳排換算段；新增決策記錄 [D7]；現況快照 Eco-Agent 列補註；ERD `DIGITAL_USAGE` 新增 `pc_idle_hours`/`pc_avg_cpu_util`/`cpu_model` 欄位 |
