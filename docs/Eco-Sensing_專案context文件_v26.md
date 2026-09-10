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
| 電梯 | 感測端採**被動 NFC tag**（各樓層電梯廳，不供電／不接觸電梯控制系統，主動式 ESP32 降級為未來增強路徑，[D5]）；手機掃描進出樓層、HTTPS 直送後端（不經 MQTT）；共乘採方案 B 固定單人分攤值（樓層差 × 上/下行單人係數，不感測人數、不拆總耗電），激勵帳與盤查帳分離 | 4.3 |
| Eco-Agent | Go 開發；方案 B 手機掃碼綁定＋雙 token；本地持久化佇列＋四重觸發上傳；集中配置參數已定案（4.4.4）；電腦路徑改使用率加權、Agent 純感測後端計算（4.4 [D7]）；雲端儲存量取 `usageInDrive`、`usageInDriveTrash` 拆作激勵任務（4.4 [D8]）；雲端 PUE 採 Google fleet-wide 均值、每GB儲存能耗強度以硬碟規格反推（4.4 [D9]，係數值待查證）；路徑 C 應用場景四類盤點、趨勢/教育可放心做、讀檔案清單類待隱私決策（4.4 [D10]）；印表機 SNMP 五參數隨綁定本地設定、不走全域下發（4.4.2、[D11]）；`DIGITAL_USAGE` 採一路徑一列、`path_type` 由 Agent 明送（[D12]，ERD 已補 `path_type`／`drive_trash_gb`）；**三路徑全改 HTTPS、Agent 不再連 MQTT Broker**（[D13]）；冪等去重定案——`collected_at` 勝出規則、鍵粒度依路徑分三組（電腦 per-device／印表機 per-printer／雲端 per-account）（[D14]，ERD 已補 `device_id`／`collected_at`／`printer_serial`／`DEVICE.display_name`）；**印表機路徑改送 SNMP 累計讀數、差分移至後端**（[D15]，ERD 已補 `printer_page_counter`） | 4.4 |
| 印表機歸戶 | 優先開發「個人專屬機（Eco-Agent SNMP 輪詢歸戶）」與「手動上傳用紙量（App 主動感測、須搭誘因）」；共用機的 Print Server Log 與 Pull Printing API 列為可行、待實作測試；**歸鍵以印表機序號（`printer_serial`）而非 `device_id`**，防同一台印表機被兩台裝置重複計算（[D14]）；**手動上傳用紙量落地定案（[D16]）**——走 `POST /api/digital-usages`（App 員工 `Bearer`、PostgREST，非 Agent 的 `agent/digital-usage/batch`）、以新欄位 `sensing_mode`（`auto`／`manual`）區分自動與手動、App 端彙總後端一天一列、兩管道互斥（專屬機走自動／共用機走手動，無雙重計算） | 4.4、7 |
| 綁定碼儲存 | 後端 `BINDING_CODE` 表持久化短效一次性碼（5 分鐘 TTL、消費即失效、過期即失效） | 4.4.2 |
| QR 辨識 | 全系統統一 custom scheme URI；掃描一律開/用 App，App 依 URI host/path 分流動作 | 4.5 |
| Agent 撤銷 | 每次上傳夾帶撤銷狀態（不另做心跳），`401/403` 即自清憑證；離線延遲上界 ≈ maxAge；上傳全走 HTTPS 後此回應通道原生成立（[D13]） | 4.4.2 |
| 後端 | FastAPI＋Supabase（PostgreSQL）；**資料存取分兩層——一般 CRUD 走 Supabase PostgREST（既有 `services/crud.py` 泛用層，含 App 手動上傳用紙量 `POST /api/digital-usages`），Eco-Agent 自動感測 `POST /api/agent/digital-usage/batch`（v26 由 `digital-usage/batch` 更名、收進 `/api/agent/*`）走 asyncpg 直連 connection pooler（6543）**（[D4]、[D16]）；寫入分兩軌——MQTT consumer 批次寫入（廢棄物）／HTTPS 批次端點（Eco-Agent）；`DIGITAL_USAGE` 冪等去重採應用層摺疊＋DB partial unique index（**v26 由三個增為四個，新增手動印表機路徑，並以 `sensing_mode` 述詞區分自動／手動**）；讀取端快取 | 5.1 |
| Controller | 自建輕量版，FastAPI 內管理模組（控制面／數據面邏輯分離）；配置經 MQTT retained＋HTTPS 夾帶雙通道下發 | 5.2 |
| Eco-Sensing App（前端應用層） | 員工端 Flutter＋Riverpod、企業端 Vue Web 後台；功能、實作狀態（✅／🟡／⚪）與 App 專屬決策集中於第 8 章（原《App 系統功能》文件已併入、封存） | 8 |
| App 身份認證 | **目前為純前端登入（不驗帳密、後端不知情）**；後端認證列為 P1 前置工作，`employee_id` 一律由憑證解出、不得由 client 於 body 指定（全系統統一原則，5.1 [D5]）；**憑證機制定案比照 Eco-Agent 採雙 token**——短效 App Access（1h）＋長效 App Refresh（30 天、存 `flutter_secure_storage`、後端存 hash 可撤銷），冷啟動以 Refresh 靜默續 Access 達成 §App1.0「自動判斷已登入」、過期判定權在後端（`401` 觸發）（5.1 [D5]、8.4） | 8.4、5.1、4.4.2 |
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
| 數位能耗監測（Desktop Agent） | Scope 3 Cat. 8 / 辦公行為 | 電腦使用、列印頁數、雲端儲存 | HTTPS / REST API |

---

## 3. 傳輸協定架構（跨模組共用決策）

混合協定架構，依資料特性分流：

### MQTT — 無人值守 IoT 硬體的資料上傳與配置下發
- 適用：廢棄物辨識（樹莓派 4B）
- 特性：輕量、低耗電、適合高頻事件推送（封包最小 2 bytes）
- Broker：Mosquitto（統一中介）
- 兼作控制面通道：`config/bin/{id}` retained message 對樹莓派下發配置（見 5.2）

### HTTPS / REST API — 需雙向往返者與外部服務呼叫
- 適用：差旅收據 OCR（GPT-4o）、Google Drive API v3、TDX 運輸 API、Google Maps API、電梯搭乘數據（手機 App → 後端）、**Eco-Agent 三條感測路徑全部**（v0.20 起，原走 MQTT 的路徑 A／B 一併改走 HTTPS，見 4.4 [D13]）
- 特性：加密傳輸、支援 OAuth 2.0、適合大型檔案（圖片）；**具原生回應通道**，可承載「已落地」確認（`200`）、撤銷狀態（`401/403`）與配置版本號夾帶

> **分流判準（v0.20 釐清）**：不以「是否為 IoT 裝置」分流，而以**是否需要後端的回應**分流。樹莓派為匿名單向推送（歸戶由後端配對 session 完成，不需回應），走 MQTT 得其輕量之利；Eco-Agent 需要「已落地確認 → 才可清本地佇列」「撤銷狀態」「配置版本號」三種回程資訊，走 HTTPS 才能一次滿足。詳見 4.4 [D13]。

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

> 依賴：App 既有 HTTPS 通道與登入身份｜`EMISSION_FACTOR`（電梯單人每層係數 `elevator_per_floor_up`／`elevator_per_floor_down`，內含台電電力係數換算）｜4.5 QR/NFC 統一 URI 格式約定（tag 內容編碼）｜實驗場域之電梯廳 tag 張貼許可（建物管理方／電梯公司，見第 7 節）

#### 規格（現行定案）

- **識別**：NFC（近場通訊）手機感應，採**被動式 NFC tag（定案，見 [D5]）**——於各樓層電梯廳張貼被動標籤，tag 內容寫死該樓層（採 4.5 統一 custom scheme URI，帶樓層參數），員工以手機掃描完成識別與樓層紀錄。**tag 為純被動貼紙：不供電、不連線、不與電梯電氣或控制系統接觸、不改裝任何機械結構、可隨時完整移除。**樓層資訊來源為「tag 貼在哪一層」，而非轎廂即時位置，故**本模組不需串接電梯控制系統**。
- **流程**：進電梯時手機掃描 NFC → 手機紀錄進入樓層 → 出電梯時手機再次掃描 → 手機計算樓層差 → 透過 HTTPS 上傳至後端 REST API。
- **資料上傳**：資料產生於手機端，由 App 既有的 HTTPS 連線直接送進後端，**不經過 MQTT Broker**。
- **Payload**：employee_id、timestamp_in/out、floor_in/out。
- **碳排歸戶模型（方案 B：固定單人分攤值，per-trip 標準值）**：本模組的碳排歸戶**不拆分電梯單趟實際總耗電、也不感測同梯人數**，而是為每一次搭乘行為記一個**預先算好的單人標準碳排**：
  - `co2e_kg = |floor_delta| × EF_dir`，其中 `floor_delta = floor_out − floor_in`、方向 `direction` 由 delta 正負決定（上行 up／下行 down），`EF_dir` 為對應方向的**單人每層係數**（`elevator_per_floor_up`／`elevator_per_floor_down`，單位 kgCO₂e/樓層·人），屬 `EMISSION_FACTOR`、後端維護、不寫死於前端。
  - **方向誘因內建於係數差**：上行係數高於下行係數（下行可設為極小值），使「搭電梯上樓」記較多碳、鼓勵改走樓梯；載重差異被吸收進上行/下行兩組係數，**不需真的量測該趟載重**（單人標準值已預設「一人份載重」）。
  - **不需要知道同梯幾人**：手機端只送進出樓層，無須感測人數、無須整合電梯秤重、無須處理併發，與 4.4 [D7]「端純感測、係數與計算全在後端」同一架構精神。
- **兩本帳分離（重要）**：本模組的個人歸戶帳**僅服務行為激勵**（GDT 視覺化與 Shared Savings 誘因），**不參與**公司對外 Scope 1/2 電梯排放的法定盤查申報。後者直接以電表（或中央監控系統）總耗電 × 台電係數計算，天生守恆、可稽核；兩帳用途不同、數字不共用（取捨依據見 [D4]）。
- **未掃出場的孤兒記錄處理**：被動 tag 依賴員工「進、出各掃一次」，員工可能忘掃出場而使該趟只有 `floor_in`。此類記錄由後端**逾時自動結算**（僅有進場掃描且超過逾時門檻者，標記為未完成、不計碳排歸戶，或依設定給予最小樓層差保守值），與 4.2 廢棄物 session「未點投入完畢由逾時自動結算孤兒事件」同一策略；逾時門檻屬 5.2 集中配置可下發參數。
- 可據此計算「步行替代搭乘」節碳量（= 該趟若不搭電梯所省下的單人標準碳排）。

#### 決策記錄（脈絡與依據）

- **[D1] 為何不經 MQTT Broker**：NFC 流程下資料主體為手機而非常駐 IoT 裝置，沿用 App 與後端的天然通道即可，無需繞經 Broker。
- **[D2] 識別方式由 BLE 改 NFC**（v0.2）：改以手機主動掃描 NFC 完成識別與樓層紀錄，傳輸同步改 HTTPS。
- **[D3] 多人共乘分攤：採方案 B（固定單人分攤值），棄方案 A（均分）與方案 C（邊際分攤）**（v0.14）：核心矛盾在於「電梯耗的是一趟的電、不是一個人的電」——N 人同梯電梯不會用 N 倍電，故「怎麼分」直接決定誘因對錯。三方案權衡：
  - **方案 A（E_trip ÷ N，同梯均分）**：方向正確（共乘者記較少、獨行者記較多），但個人碳排會因「剛好幾人同梯」而劇烈波動；而同梯人數是**員工不可控**的變數，與 Shared Savings「誘因須綁可控行為」原則衝突，且仍需感測人數或載重。
  - **方案 C（基礎能耗均分 + 載重增量各認）**：物理最精確，但需區分「基礎 vs 載重增量」，回頭要求載重量測與更細能耗模型；在「時間有限、技術有限」前提下精度提升的邊際效益過低，否決。
  - **方案 B（固定單人標準值，選定）**：不拆 E_trip、不感測人數，每次搭乘記一個標準碳排。優點：員工**完全可預期**（搭這段就是這麼多）、誘因清楚（改走樓梯就減這麼多）、實作最簡（NFC 進出樓層 × 係數即結束）、感測端最輕。代價是「個人歸戶總和 ≠ 電梯實際總耗電」（不守恆），此代價由 [D4] 說明為可接受。與 4.2 廢棄物「分攤感測的天生取捨」、4.4 [D7]「歸戶可避免的邊際浪費而非底噪」同一類定位。
- **[D4] 放棄守恆性（個人歸戶總和 ≠ 電表總量）的取捨聲明**（v0.14）：方案 B 下，若多人同梯各記單人標準值，其總和會超過該趟電梯實際耗電——此**不守恆為刻意設計，非疏漏**。理由是**兩本帳用途本就不同、不該共用同一組數字**：
  - **對外盤查帳（Scope 1/2）**：要守恆、可稽核、對得上電費單 → 直接用電表／中央監控系統總量 × 台電係數，本模組個人帳完全不介入，故不守恆對法定申報**零影響**。
  - **對內激勵帳（GDT／Shared Savings）**：要員工可預期、可控、方向誘因正確 → 用個人標準值。若硬求守恆反而得引入「同梯人數」這種不可控變數，破壞誘因。
  - 另註：idle/standby 等「不管有沒有人搭都在燒」的設施底噪，本質上無法也不該歸給任何個人，本模組只記**搭乘行為的標準值**，不將底噪攤入個人帳（與 4.4 [D7] 只歸戶可避免浪費、不歸戶必要底噪一致）。
  - **定位聲明（供答辯）**：個人歸戶本質為「合理分攤估算」而非「精確量測」；係數 `EF_dir` 的校準基準可另向電梯公司索取（額定功率、對重平衡比例、單趟實測耗電或 ISO 25745 量測報告），但模型本身不因缺乏精確載重而不成立。
- **[D5] NFC 感測端定案採「被動 NFC tag（各樓層電梯廳）」，主動式 ESP32（轎廂內）降級為未來增強路徑**（v0.19）：兩案的關鍵差異**不在成本，而在「樓層資訊從哪裡來」**：
  - **被動 tag：樓層資訊來自「tag 貼在哪一層」**。tag 不會移動，樓層寫死於 tag 內容即可，**完全不需要知道轎廂即時位置，故完全不需接觸電梯控制系統**。
  - **主動 ESP32：裝於轎廂內、隨轎廂移動，它自己不知道現在在幾樓**，必須另尋樓層來源——(a) 串接電梯控制系統：涉電梯安規（CNS／EN 81）、原廠保固與原廠工程師介入，並需轎廂取電施工，學生專題取得批准的機率極低；(b) 自行加裝氣壓／加速度感測推算樓層：15 層樓量級的氣壓差小、誤差累積嚴重、校準困難，可靠度不足以支撐碳排歸戶。
  - **選定被動 tag 的理由**：零施工、零供電、零改裝、對電梯零風險、可隨時完整移除，且**繞開「取得轎廂即時樓層」這個可能無解的前提**；對電梯公司的請求也從「串接貴公司控制系統」降為「在電梯廳張貼標籤」，審批摩擦大幅降低，實驗可行性顯著提高。
  - **代價（已於規格段處理）**：被動 tag 依賴員工主動掃描兩次，可能產生只有進場、無出場的孤兒記錄，以逾時自動結算處理（同 4.2 廢棄物 session）。ESP32 唯一的真實優勢是「自動偵測、不靠員工自覺」，但該優勢在無法取得轎廂樓層資訊的前提下**無法兌現**，故不構成選它的理由。
  - **定位與既有設計一致**：與 4.4 [D6] 印表機「優先開發不依賴場域基礎設施的路徑、依賴基礎設施者列為可行待測」同構——主動式 ESP32 列為「若未來能取得電梯控制系統樓層介面時的增強路徑」，不納入現階段開發。

### 4.4 數位能耗監測 Desktop Agent（Eco-Agent）（HTTPS）

> 依賴：App 登入身份與 QR 掃碼能力（4.4.2 綁定）｜5.1 HTTPS 批次上傳端點與冪等 upsert｜5.2 集中配置服務（參數下發）｜系統金鑰庫（Windows DPAPI／macOS Keychain）

#### 規格（現行定案）

背景常駐程式，三條感測路徑：

| 路徑 | 對象 | 方法 | 協定 |
|------|------|------|------|
| A | 電腦使用 | Windows `GetLastInputInfo()` / macOS `IOHIDSystem`、`HIDIdleTime` 判活躍/閒置＋跨平台 CPU 使用率（`gopsutil`），每固定區間 `computerUsageRecordInterval` 輪詢；**Agent 只送原始量（active/idle 時數、平均使用率、CPU 型號），能耗由後端以使用率加權模型計算**（見「電腦能耗模型」） | HTTPS |
| B | 印表機（個人專屬機） | SNMP（UDP 161）查詢 page counter OID `1.3.6.1.2.1.43.10.2.1.4` 取**壽命累計讀數**與序號 OID 取 `printer_serial`，**兩者原樣上送、差分由後端做**（[D15]）；以 Agent 綁定 employee_id 歸戶、以 `printer_serial` 歸鍵（[D14]）；此軌 `sensing_mode = auto`（[D16]）。共用機歸戶另循 Print Server Log／Pull Printing（可行、待實作測試）或改由 **App 手動上傳用紙量**（**非本 Agent 路徑**，屬使用者主動感測，落庫走 `POST /api/digital-usages`、`sensing_mode = manual`，見 [D6]、[D16]）——詳見決策記錄 [D6]、[D16] | HTTPS |
| C | 雲端儲存 | OAuth 2.0 授權，Google Drive API `about?fields=storageQuota`，取 `usageInDrive` 作為儲存量 × 每GB儲存能耗強度 × PUE（fleet-wide）（儲存量取值見 [D8]、係數取得見 [D9]） | HTTPS |

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
  - **儲存量取 `usageInDrive`，另拆 `usageInDriveTrash` 作激勵任務（v0.15 定案，見決策記錄 [D8]）**：`storageQuota` 回傳四值——`usage`（該帳號 Drive＋Gmail＋Photos 總佔用）、`usageInDrive`（僅 Drive 內容，含垃圾桶）、`usageInDriveTrash`（Drive 垃圾桶佔用，已內含於 `usageInDrive`）、`limit`（配額上限）。能耗公式的「儲存量」**取 `usageInDrive`**：不取 `usage`（含 Gmail／Photos，超出路徑 C 界定的 Drive SVS 範圍）、不取 `limit`（配額額度非實際佔用；且 Workspace pooled 模式下 `limit` 為機構共享總池、全員雷同，用於個人歸戶無區辨力）。另**單獨取出 `usageInDriveTrash`** 於 App／儀表板標為「可立即釋放的儲存能耗」，作為 i 減碳激勵任務（清空垃圾桶即減碳、以 EXP／碳幣回饋），呼應電梯上/下行係數差「行為誘因內建」之設計思路。
- **印表機（路徑 B）＝ 依印表機歸屬分軌，歸戶前提已定案（v0.11）**：能否用哪種感測形式取決於「列印能否歸戶到個人」——SNMP page counter 為累計值、無推播能力（故只能輪詢），且讀到的是**整台機器總頁數**而非個人用量。經團隊決議，路徑 B 分為以下幾軌（詳見決策記錄 [D5]、[D6] 與第 7 節）：
  - **優先開發**：
    - **個人專屬印表機（Eco-Agent 感測）**：可靠 Agent 綁定的 employee_id 直接歸戶，SNMP 中區間輪詢（`printerPollInterval`）即足夠。
    - **手動上傳用紙量（Eco-Sensing App）**：員工於列印前後在 App 內輸入並上傳用紙量，屬**使用者主動感測、須搭誘因**（比照 i 減碳任務以 EXP／碳幣激勵）；不依賴任何印表機基礎設施，為共用機環境的可行補位手段。
  - **未來實作、測試（列為可行但待實作）**：共用印表機要歸戶到人須改用帶 user 欄位的來源——**Print Server Log**（逐工作帶送出者身份，天生事件式，可訂閱 Windows PrintService/Operational Event ID 307）或 **Pull Printing API**（刷卡列印，如 PaperCut，釋放前刷證驗證使身份與工作在源頭綁定）。兩者技術上皆可行、且「事件觸發＋歸戶」同時成立，但受限於實驗場域基礎設施前提，列為待實作與測試項。

- **本地彙整與去識別化**：資料先寫入本機持久化佇列，於上傳前打包彙整——移除姓名/Email，僅保留員工 ID Token（符合個資合規）。上傳時機採多重觸發（不綁固定時刻），詳見 4.4.3。
- **上傳 Payload**（HTTPS `POST {base_url}/api/agent/digital-usage/batch`，`Authorization: Bearer <Agent Access Token>`，body 為筆陣列，單次筆數上限 `uploadBatchMax`）：**每筆為「某裝置某日某路徑」的一筆感測結果**，且為**扁平記錄**（共同欄位與量值同層，不另包 payload 物件）。共同欄位為 `event_id`（4.4.3 的穩定鍵，Agent 明送，後端可直接用作冪等鍵而不必自行重組）、`usage_date`、`path_type`（列舉 `computer`／`printer`／`drive`，**由 Agent 明送、不由後端推斷**，見 [D12]）、`collected_at`（Agent 端採集時間戳，UTC RFC3339Nano，供亂序抵達勝出判定，見 [D14]），其餘欄位依 `path_type` 而定：
  - `path_type = computer`：pc_active_hours、pc_idle_hours、pc_avg_cpu_util、cpu_model
  - `path_type = printer`：printer_serial、printer_page_counter（SNMP 壽命累計讀數，**非區間差值**；頁數由後端差分算出，見 [D15]）
  - `path_type = drive`：drive_usage_gb（取自 `usageInDrive`）、drive_trash_gb（取自 `usageInDriveTrash`，供激勵任務用，見 [D8]）
  （電腦路徑改送原始量——active/idle 時數、平均 CPU 使用率、CPU 型號——不再送 `pc_tdp_w`；能耗由後端計算。`factor_id`／`co2e_kg` 屬後端查係數計算後寫入，**不在 Agent payload 內**。）
  - **`employee_id` 與 `device_id` 皆不在 payload 內**：Agent 只持有 `id_token`（4.4.2，per-device 一枚），後端以 `id_token` 查 `DEVICE_BINDING` 即**同時解出 `employee_id` 與 `device_id`** 二者並落庫。故 [D14] 將 `device_id` 納入電腦路徑唯一鍵一事，對 Agent payload 零改動。
  - **惟 `printer_serial` 必須由 Agent 上送**：印表機序號是**本地網路事實**（同 [D11] 的 `HOST`／`OID`），後端無從得知某台桌機接的是哪台印表機，只能由 Agent 經 SNMP 讀出後隨資料上送。此為 [D14] 缺口二在路徑 B 上唯一需要動 payload 之處。
- **手動上傳用紙量（App 路徑，非本 Agent 上傳，v26 [D16] 定案）**：印表機路徑 B 的備選來源「手動上傳用紙量」由**員工在 Eco-Sensing App 內輸入**，**不經 Eco-Agent、不走 `agent/digital-usage/batch`**，而是走既有的 **`POST /api/digital-usages`**（PostgREST 泛用 CRUD、App 員工 `Bearer`，`employee_id` 由 App Access Token 解出、不得於 body 指定）。落庫仍進 `DIGITAL_USAGE`，但以 **`sensing_mode = manual`** 與 Agent 自動路徑（`sensing_mode = auto`）區隔（見 [D16]）：
  - **App 端彙總、後端一天一列**：員工當日可多筆記錄、每筆於送出前在 App 本地編輯；App 以某觸發機制上傳**當日彙總後的總用紙量一筆**，後端只把該筆 upsert 進「該員工×該日×`printer`×`manual`」那一列。多筆去重與編輯皆在 App 端（送出前）完成，後端不存明細、不需 `event_id` 冪等、不需 asyncpg 條件式 upsert（見 [D16]、5.1）。
  - **落庫唯一鍵**：（`employee_id`, `usage_date`, `path_type`, `sensing_mode`）——`path_type = printer`、`sensing_mode = manual`；`printer_serial` 為 NULL（手動上傳無序號),故不套 SNMP 自動路徑的 per-printer 鍵，避開 `printer_serial` 為 NULL 的去重陷阱（見 [D16]、5.1 冪等去重）。
  - **與自動路徑互斥（無雙重計算）**：手動上傳只用於**共用印表機**（無 Agent SNMP 之場域）；個人專屬印表機一律走 Agent SNMP（`auto`）。兩管道互斥、同一台機器不會同時有 `auto` 與 `manual` 兩列,故加總不重複（見 [D16]）。
  - **落庫後更正**：App 本地編輯僅限**送出前**；已落庫者若需更正,走既有 `PATCH /api/digital-usages/{id}`（同帶 App `Bearer`）——惟「更新語意」（App 重算總量重送覆蓋 vs 直接改庫值）尚待釐清，見第 7 節。
- **碳排換算（集中於後端）**：電力（電腦：使用率加權功率模型 `P_idle + 使用率 ×(P_active − P_idle)` × 時數 × 台電係數，`P_active` 由 CPU 型號查 TDP 表；未來可由 RAPL/powermetrics 即時功耗覆蓋）＋ 列印（頁數 × 紙張生命週期係數；頁數由後端以「當日最新 `printer_page_counter` − 前一日最新讀數」差分求得，見 [D15]）＋ 雲端（`usageInDrive` GB × 每GB儲存能耗強度（kWh/GB/年，未含 PUE）× PUE（Google fleet-wide ~1.1）× 電力係數 × 時間比例；`usageInDriveTrash` 另計為「可釋放能耗」供激勵，見 [D8]、[D9]）。TDP 對照表、P_idle 比例、每GB儲存能耗強度、PUE、各項係數皆屬 `EMISSION_FACTOR`／係數配置，後端維護、不寫死於 Agent。
- **傳輸協定（v0.20 定案）**：三條路徑**一律走 HTTPS 進後端 REST API，Eco-Agent 不再連線 MQTT Broker**（原路徑 A／B 走 MQTT 之設計於 v0.20 廢止，理由見 [D13]）。本專案 MQTT 因此僅存兩處用途：廢棄物樹莓派的資料上行（4.2）與 5.2 對樹莓派的 retained 配置下發。Eco-Agent 執行檔可移除 MQTT client 依賴（`paho.mqtt.golang`）。

##### 4.4.1 開發架構：Go

- Desktop Agent（代號 **Eco-Agent**）以 **Go** 開發（選型理由見決策記錄 [D1]）。
- **跨平台交叉編譯**：單機以 `GOOS` / `GOARCH` 環境變數即可編出 Windows / macOS / Linux 三平台執行檔。
- **BYOD 權限注意**：macOS 偵測使用者活動（`IOHIDSystem`、`HIDIdleTime`）需 Accessibility 授權；SNMP 查印表機需與印表機同網段——此兩點為 BYOD 部署的實際摩擦點，需在安裝引導中明確處理。

##### 4.4.2 裝置綁定機制（Device Enrollment）

Eco-Agent 為無人值守背景程式，身份綁定採「**一次綁定、長期常駐、雙向可解除**」的裝置註冊（device enrollment），不採傳統「每次開啟登入」模式。歸戶的 `employee_id` 由此機制建立。綁定方式採**方案 B（手機 App 掃碼綁定）**（選型理由見決策記錄 [D2]）。

**綁定階段流程**

1. 員工開啟 Eco-Agent → Agent 向後端索取一次性 `binding_code`（短效）。後端於 `BINDING_CODE` 表建立一筆記錄（`status=pending`、`created_at`、`expires_at = created_at + 5 分鐘`、`device_id` 指向索取的 Agent 裝置）。
   - **回應同時附帶 `device_secret`**：一枚僅於本次 HTTP 回應中出現、**不編入 QR Code**的隨機值，供步驟 5.5 領取 token 時證明「我就是當初索取這個碼的那台 Agent」。無此欄位時，領取 token 的唯一憑據是 `code` 本身，等同「誰持有 `code` 誰就能領走該裝置的 token」；`code` 雖只顯示於該台電腦螢幕、5 分鐘一次性，風險不高，但補上成本極低，P1 即應納入。
2. Agent 將 `binding_code` 編入 **QR Code** 顯示於畫面（QR 內容採全系統統一 custom scheme URI，見 4.5）。
3. 員工以**已登入的 Eco-Sensing App** 掃碼；App 依 URI host/path 判定為綁定動作。
4. App 把**已驗證身份 + binding_code** 送後端。
   - **身份的傳遞方式（v0.22 明訂）**：`employee_id` **僅由 App 登入憑證（`Authorization: Bearer <App session token>`）解出，不得出現在 request body**；request body 只帶 `code`。後端做兩件獨立的事再接起來——驗簽 App token 得 `employee_id`（人的一半）、查 `BINDING_CODE` 得 `device_id`（裝置的一半）——即「後端負責配對」的實質內容。`employee_id` 一旦可由 client 指定，[D2] 所否決的作弊後門即原封不動地回來（只是從「輸入員工 ID」換成「在 JSON 裡填員工 ID」）。**此為硬性約束，P1 以臨時憑證實作時亦須維持此介面形狀。**
5. 後端核對 `binding_code`（檢查 `status=pending` 且 `expires_at > now()`）→ 建立 `device_binding` → 回填 `BINDING_CODE.employee_id`、`device_binding_id`、`consumed_at`，並將 `status` 改為 `consumed`；發放 **Access Token（短期）+ Refresh Token（長期）**。過期或已消費的碼一律拒絕（防重放）。
5.5. **Agent 輪詢領取 token**（v0.23 補述）：Agent 定期查詢該 `code` 的核銷狀態，`status=consumed` 時取回步驟 5 所簽發的 Access + Refresh Token；查詢須同時帶 `code` 與步驟 1 取得的 `device_secret`。
   - **此步驟原缺漏於本流程**：v0.22 之前的六步驟由「後端發放 token」直接跳至「Agent 取得 token」，未說明 token 如何抵達 Agent。實際情境是 **QR 顯示於電腦螢幕、掃碼發生於手機**，後端與 Agent 之間在此刻**並不存在既有連線可供推送**（Agent 尚未持有任何憑證，亦無常駐連線）。故傳遞方式只能是 **Agent 端輪詢**（或 long polling），不能是後端推送。
   - 輪詢至 `expires_at` 為止；逾時未核銷即放棄該碼、回到步驟 1 重新索取（對應 4.4.4 測試項「Binding Code 過期／重放」）。
6. Agent 取得 token，**Refresh Token 存入系統金鑰庫**（Windows DPAPI／macOS Keychain，**不寫純文字檔**）→ 轉入背景常駐。

**後端須實作的四個端點（P1 範圍，v0.23 補列）**

| # | 端點 | 呼叫者／認證 | 後端動作 |
|---|------|------------|---------|
| ① | `POST /api/agent/binding-code` | Agent，**無認證** | 建 `DEVICE`（或依 Agent 本機持久化的 `device_uuid` upsert）＋ `BINDING_CODE`（`pending`、5 分鐘）；回 `code`、`device_secret`、`expires_at`（步驟 1） |
| ② | `POST /api/agent/bind` | **手機 App，帶 App 登入憑證** | 驗簽得 `employee_id` ＋ 查 `code` 得 `device_id` → 建 `DEVICE_BINDING`、簽發雙 token、回填並核銷 `BINDING_CODE`（步驟 4–5）。**body 只帶 `code`** |
| ③ | `GET /api/agent/binding-code/{code}/token` | Agent，帶 `device_secret` | 回該碼的核銷狀態；`consumed` 時交付 Access + Refresh Token（步驟 5.5） |
| ④ | `POST /api/agent/token/refresh` | Agent，帶 Refresh Token | 比對 `refresh_token_hash` 且 `status=active` → 發新 Access Token；已撤銷回 `401/403`（屬常駐階段，非綁定階段，但同屬本組端點且撤銷於此生效） |

> **`DEVICE` 列的重複建立**：Agent 每次啟動若都呼叫 ①，綁定失敗（員工未掃、逾時）的殘留 `DEVICE` 列會持續累積。建議 Agent 於本機持久化一枚 `device_uuid`（與 4.4.3 的 SQLite 佇列同檔），索取綁定碼時帶上，後端據以 upsert `DEVICE` 而非盲插。

> 安全核心：Agent 全程**只接觸一次性 `binding_code`，不接觸員工帳密或員工 ID**；真正的身份驗證在已登入的手機 App 上完成，後端負責配對，符合「純感測、不碰個資」原則。

**綁定碼儲存（`BINDING_CODE` 表）**

- `binding_code` **必須於後端持久化**（後端要能核對，就須先有記錄可比對），儲存於 Supabase `BINDING_CODE` 表（ERD 已納入）。
- 屬**短效一次性憑證**：`expires_at` 到期即失效（`bindingCodeTTL` = 5 分鐘）；App 掃碼核對成功即改 `status=consumed`，同碼不可重複使用。
- 過期記錄由背景排程或查詢時 lazy 判斷（`expires_at < now()` 視為 expired）。
- 表上另存 `device_secret`（或其 hash），供端點 ③ 比對；與 `code` 同生命週期，核銷或過期後即失效。
- P1 直接存 Supabase 一張表即可，不為此提前引入 Redis（與 5.1 [D3] 一致）；P3 若導入 Redis，一次性短效碼恰為 Redis TTL key 的典型用途，屆時可遷移。

**Session 與登出（雙向可解除）**

- **「永久有效」以雙 token 實作，而非永久 token**：
  - Access Token（短期，**1 小時**）→ 每次上傳資料用。**後端簽發策略參數、不落庫**（可由 Refresh Token 隨時換發，DB 不儲存），與 `id_token` 無關。
  - Refresh Token（長期，**90 天**）→ 安全存本機（金鑰庫），後端僅存 `refresh_token_hash`，專供換新 Access Token；Access Token 過期自動續期，使用者無感。
  - **輪換策略：不啟用**——Refresh Token 90 天固定不變、到期即需重走綁定流程（重新掃碼）。輪換（每次續期換發新 Refresh、舊的作廢）帶來的複雜度與離線誤判風險，於數十人專題規模 > 收益，列為 P3 資安強化備選；主要威脅已由「每次上傳夾帶撤銷」覆蓋。
- **員工端登出**（本機操作）：換機時於 Agent 點「解除綁定」→ 清本機憑證 + 通知後端標記解綁。
- **企業端遠端登出 / 撤銷**（Web 後台）：員工離職、裝置遺失或異常時，IT 將該裝置標記 `revoked`。
- **撤銷生效機制（採每次上傳夾帶，不另做心跳）**：Agent 每次 flush 上傳時，後端於回應夾帶有效性狀態；若已被撤銷則回 `401/403`，Agent 收到即**自我清除憑證（含金鑰庫 Refresh Token）、停止上傳**。此檢查搭既有上傳往返「搭便車」，與 4.4.3 上傳回應、5.2 配置版本號夾帶共用同一條回應通道，零額外通道。**（v0.20 註）**：本機制原即以 HTTP 語意書寫（`401/403`），在 v0.19 之前與「路徑 A／B 走 MQTT」相衝突——MQTT 無此回應語意，該通道實則不存在。v0.20 將三路徑全改 HTTPS 後（[D13]），此處敘述始為原生成立，無須另開回程通道。
  - **離線裝置撤銷延遲容忍度**：撤銷延遲 = 下次上傳觸發之前的時間，上界 ≈ 4.4.3 的 `maxAge`（24h，裝置有開機前提下）。離線期間裝置本就送不出資料，不造成資料正確性風險；真正要防的「重新上線後還能送」在其一上線 flush 即被 `401/403` 擋掉，故延遲可接受。
  - （備選，未採）開機/喚醒後先發輕量狀態查詢以提前撤銷檢查，列為 P3 韌性強化備選。

**身份標識與去識別化**

- 綁定時取得的身份標識即作為去識別化打包保留的**員工 ID Token**；Agent 全程只持有不可逆 token，不直接持有員工 ID，強化「純感測、不碰個資」定位。

**個人專屬印表機 SNMP 參數（隨綁定設定）**

路徑 B 的五個 SNMP 連線參數屬**單機環境事實**（各機不同、僅本地網路可知），故隨綁定流程於本機設定（`.env`），非由 5.2 全域下發（理由見決策記錄 [D11]）；主要需設 `HOST` 與（少數機種）`OID`：

| 參數 | 用途 | 預設 |
|------|------|------|
| `ECO_AGENT_PRINTER_HOST` | 印表機 IP／主機名，**唯一必填**；未設即視為本機無專屬印表機，路徑 B 優雅降級跳過 | 無（不填＝停用） |
| `ECO_AGENT_PRINTER_COMMUNITY` | SNMP v2c 唯讀 community | `public` |
| `ECO_AGENT_PRINTER_PORT` | SNMP 埠 | `161` |
| `ECO_AGENT_PRINTER_OID` | page counter 的 instance OID（`prtMarkerLifeCount`）；非標準 index 機種可覆寫，查不到時自動巡走該欄取第一筆 | `1.3.6.1.2.1.43.10.2.1.4.1.1` |
| `ECO_AGENT_PRINTER_SERIAL_OID` | 印表機序號 OID，供 [D14] 歸鍵用；未設則依序試 `prtGeneralSerialNumber` → `entPhysicalSerialNum` → `sysName`，全空則回退 `device_id` 並標記 | `1.3.6.1.2.1.43.5.1.1.17.1`（`prtGeneralSerialNumber`） |

- **HOST 填法**：只填 IP 主機位址，不含 `http://`、埠或路徑（例：印表機網頁介面為 `http://192.168.1.162:80/WebServices/Device`，HOST 僅填 `192.168.1.162`；該 HTTP/WSD 介面與 SNMP UDP 161 無關，PORT 仍為 161）。
- **讀值即累計頁數，差分在後端**（v0.23 [D15] 修訂）：SNMP page counter 為只增不減的壽命累計值。Agent **原樣上送該讀數**（`printer_page_counter`），不在本機相減；區間用量與「本次 < 上次視為計數器重置」的防呆一律由後端依歷史列判定（後端有完整歷史，判得更準且留得下稽核紀錄，並免除 baseline 只存於 Agent 本機、重裝即遺失的問題）。
- **序號讀取（供 [D14] 歸鍵）**：綁定時與每次輪詢各讀一次 `printer_serial`。三個候選 OID 依序試——`1.3.6.1.2.1.43.5.1.1.17`（`prtGeneralSerialNumber`，Printer-MIB，首選）、`1.3.6.1.2.1.47.1.1.1.1.11`（`entPhysicalSerialNum`，ENTITY-MIB，次選）、`1.3.6.1.2.1.1.5.0`（`sysName`，末選，管理員可改、不保證唯一）。低階機種常三者皆回空值，屬**待實測項**；全空時回退以 `device_id` 歸鍵並將該列標記為「印表機身份不明」。**不以 `PRINTER_HOST`（IP）當鍵**——DHCP 會變，且不同辦公室的同段私有 IP 會誤撞。
- 綁定時可將非敏感的 `HOST`／`OID` 上報一份供 Web 後台監控裝置對應的印表機，但**不由後端反向下發覆蓋本地**，避免蓋掉正常的本地設定。輪詢區間 `printerPollInterval` 仍屬全域策略，走 5.2 下發（見 4.4.4）。

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

**至少一次送達（At-least-once，端到端成立）**

- 佇列資料**僅在後端回 `200` 確認後才標記已上傳並清除**；上傳失敗（離線、後端不可用、逾時、`5xx`）則保留，下次觸發重試。
- **此合約成立的前提是全程 HTTPS（v0.20 [D13]）**：`200` 由**後端於批次落地（upsert commit）之後**才回出，非由中介代發，故「收到 200」與「資料已在資料庫」等價，不存在「Agent 已清佇列、資料卻仍在後端記憶體」的破口。**後端不得先回 `200` 再非同步落地**——該作法會重新打開此破口，屬實作上的硬性約束（見 5.1）。
- 每筆帶**唯一事件 ID**（`id_token + usage_date + path_type` 組出穩定鍵；其中「路徑類型」即 payload 的 `path_type`，**由 Agent 明送並落庫為 `DIGITAL_USAGE.path_type`**，見 [D12]）。後端以 `id_token` 查 `DEVICE_BINDING`，**同時解出 `employee_id` 與 `device_id`**，據以 upsert；落庫唯一鍵與事件 ID 的粒度對齊（見 [D14]）：
  - `path_type = computer`：（`employee_id`, `usage_date`, `path_type`, `device_id`）——一裝置一列，員工層碳排以加總取得。
  - `path_type = printer`：（`employee_id`, `usage_date`, `path_type`, `printer_serial`）——**一印表機一列**。測量主體是印表機而非裝置，若改以 `device_id` 分列，桌機與筆電同指一台專屬印表機時會各記一份同機頁數、加總即重複計算（見 [D14]）。
  - `path_type = drive`：（`employee_id`, `usage_date`, `path_type`）——雲端儲存為**帳號層級事實**，同員工多台裝置查得同一數值，若納入 `device_id` 會憑空重複計算。
  - 以上三組皆為 **Agent 自動感測路徑**（`sensing_mode = 'auto'`），走 `POST /api/agent/digital-usage/batch`。**App 手動上傳用紙量不在此列**：它不經 Agent、無 `id_token`，由 App 以員工憑證走 `POST /api/digital-usages`，落庫鍵為（`employee_id`, `usage_date`, `path_type='printer'`, `sensing_mode='manual'`），App 端已彙總當日總量、後端一天一列覆蓋（見 [D16]、5.1 冪等去重）。
- **亂序抵達的勝出規則**：每筆另帶 `collected_at`（Agent 端採集時間戳，UTC）。後端 upsert 僅當 `EXCLUDED.collected_at > digital_usage.collected_at` 時才更新，重送的舊封包不會蓋掉較新的值（見 [D14]）。原僅有 `usage_date`（日期粒度）不足以比較同日先後，故 v0.20 補此欄。手動路徑亦沿用此欄作**防亂序重送**保險（App 弱網重送同一筆彙總時不讓舊封包蓋新值），惟其「編輯」發生於 App 本地送出前、不涉落庫後覆蓋（見 [D16]）。
- 重複送達不重複計算由 5.1 之兩層冪等機制保證（應用層依鍵摺疊 ＋ DB partial unique index），詳見 5.1「`DIGITAL_USAGE` 冪等去重」。

**與既有設計的銜接**

- 與 4.2 廢棄物 session「逾時自動結算孤兒事件」同一思路：不假設 happy path，為中斷保留兜底。
- 與 4.4.2 撤銷機制天然整合：每次 flush 上傳都會收到後端回應，順帶夾帶撤銷狀態檢查（`401/403` 即自清憑證），一石二鳥。**此整合以 HTTPS 為前提**（[D13]）：同一條回應通道一次承載「已落地確認」「撤銷狀態」「配置版本號」（5.2）三件事，三者共用零額外通道。
- flush 間隔、累積量門檻、最長滯留時數等參數，屬 5.2 集中配置服務（`sensor_config`）可下發之 Eco-Agent 策略。

##### 4.4.4 集中配置參數（已定案）

以下參數由 5.2 集中配置服務（`sensor_config`）統一管理與下發（後端簽發策略類則由後端維護）。

**憑證效期（後端簽發策略，不下發）**

> 本組屬**後端簽發策略、由後端維護**，非 `sensor_config` 下發參數——後端簽 JWT 時直接寫入 `exp`，不經 MQTT／HTTPS 下發給任何端點，client 亦不得讀取後改變行為。放此表僅為與其他效期參數並列、便於統一治理（見 5.1 [D5] v0.24 對「歸類為後端簽發策略」的說明）。

| 中文 | 變數名／英文名 | 數值 |
|------|--------------|------|
| 短效綁定碼效期 | `bindingCodeTTL` | 5 分鐘 |
| **Eco-Agent** 短期上傳憑證效期 | Agent Access Token exp | 1 小時（不落庫、與 `id_token` 無關） |
| **Eco-Agent** 長期換發憑證效期 | Agent Refresh Token exp | 90 天（到期重走綁定，不輪換） |
| **App** 短期存取憑證效期 | App Access Token exp | 1 小時（App 每個歸戶請求用；不落庫，由 App Refresh Token 換發） |
| **App** 長期換發憑證效期 | App Refresh Token exp | 30 天（存 `flutter_secure_storage`；後端存 hash 可撤銷；到期重新登入，不輪換） |

**資料收集與上傳觸發（Eco-Agent）**

| 中文 | 變數名／英文名 | 數值 |
|------|--------------|------|
| 電腦使用量輪詢區間 | `computerUsageRecordInterval` | 60 秒 |
| 電腦待機時間門檻 | `idleThreshold` | 10 分鐘 |
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
  - 方案 B 複用 App 既有基礎設施：員工端 App 已有**登入 UI 與登入狀態保留**（`DemoAuthStorage`、`currentUserProvider`）與 QR 能力（`QRCodePopup` 產生、回收計重的掃碼相機流程），開發成本最低；公司電腦與 BYOD 皆適用。
    - **（v0.22 更正）**：本條原述為 App「已有完整登入身份系統」，屬事實誤述——依《App 系統功能》§1.0，目前登入頁**不驗證帳密**（選「員工端」即進入），`DemoAuthStorage` 僅以 `SharedPreferences` 保存一個「已登入」狀態，**後端對此一無所知**。既有的是登入**畫面與狀態保留**，不是可供後端驗證的身份系統。故 §4.4.2 步驟 4「App 把已驗證身份 + binding_code 送後端」的前提**目前尚不成立**，須先補 App 端後端認證（見 5.1 P1 工作項與第 7 節）。此更正不影響 [D2] 選擇方案 B 的結論——「身份驗證在已登入的 App 上完成、Agent 不碰員工 ID」的架構仍然正確，只是其依賴的前置條件比原先認知的多一項。
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
- **[D8] 雲端儲存（路徑 C）能耗模型的儲存量取值，與 `usageInDriveTrash` 拆作激勵任務**（v0.15）：Eco-Agent 已實作路徑 C 感測，`about?fields=storageQuota` 可取得 `usage`、`usageInDrive`、`usageInDriveTrash`、`limit` 四值。能耗公式 `儲存量 × PUE` 的「儲存量」須釐清取哪一項：
  - **四值定義**：`usage` = 該帳號在整個 Google 生態（Drive＋Gmail＋Photos）的總佔用；`usageInDrive` = 僅「我的雲端硬碟」內容（含垃圾桶那部分）；`usageInDriveTrash` = Drive 垃圾桶佔用（**已內含於 `usageInDrive` 與 `usage`**，非額外）；`limit` = 配額上限。
  - **儲存量取 `usageInDrive`**（選定）。否決另兩者的理由：
    - **不取 `limit`**：配額上限是「被允許存多少」的額度、非「實際存了多少」的佔用，能耗來自實際佔用的磁碟。且來源帳號屬 Google Workspace（機構）帳號——判準為 `limit` 達約 569 TB（遠超個人方案的 15GB／100GB／2TB 量級）；Workspace 於 **pooled storage（機構共用池）** 模式下 `limit` 回傳的是**全機構共享總池**、每個員工查到的值相同，用於個人歸戶會全員雷同、零區辨力，違反本專題（電梯 [D3]／電腦 [D7]）一貫的「歸戶須綁可區辨的個人行為」原則。
    - **不取 `usage`**：其含 Gmail／Photos，超出路徑 C 所界定的「Drive 儲存」SVS 範圍；個人信箱佔用混入會使「Drive 儲存行為」的歸戶失真。若未來有意把路徑 C 重新界定為「整個 Google 帳號雲端佔用」再議，現階段維持 Drive 範圍。
  - **`usageInDriveTrash` 單獨拆出作 i 減碳激勵任務（已決議落地）**：垃圾桶內容仍佔磁碟、仍耗能，故計入總能耗合理；但因其為「已刪除、可立即清空釋放」的佔用，另**單獨取出**於 App／儀表板標為「可立即釋放的儲存能耗」，作為可執行減碳任務（清空垃圾桶即減碳，以 EXP／碳幣回饋），讓員工看到清垃圾桶能直接減碳。此為「行為誘因內建於資料切分」的設計，與 4.3 電梯上/下行係數差、4.4.3 誘因對齊「節能而非少用」同一思路。**經團隊決議此激勵任務確定納入**：Agent 上傳 `usageInDriveTrash`、入庫 `DIGITAL_USAGE.drive_trash_gb` 欄位（ERD 已同步），納入 i 減碳任務清單；獎勵額度依 1.7 遊戲化機制設計。取 `usageInDrive` 作能耗儲存量之主決策不受此影響、已定案。
  - **待確認（實務）**：實驗場域員工帳號的 `usageInDrive` 量級是否合於「一般員工日常帳號」；若某帳號 `usage` 異常高（如本次取樣的約 342,884 GB），較可能為機構共享硬碟主帳號／備份／服務帳號，歸戶到「個人數位碳足跡」的意義會被稀釋，取樣時須排除或另行標註。（`usageInDriveTrash` 激勵任務是否落地一項**已決議納入**，見上條。）
- **[D9] 雲端能耗公式的 PUE 與「每 GB 儲存功耗」係數取得**（v0.16，討論中）：`usageInDrive × PUE` 這條式子實際上須拆成三段係數——「每 GB 儲存功耗（IT 設備耗電）× PUE（放大成含冷卻的機房總耗電）× 電力排放係數（換成 CO₂e）」。以下釐清各段的取得方式與可行性：
  - **PUE 取 Google fleet-wide 平均值（定案取值方式）**：個別機房的即時 PUE **原理上取不到**——(a) Google Drive API 只回傳配額數字，不含「資料存在哪座機房」；且 Google 儲存架構將單一檔案分片、跨多機房多副本備援並動態遷移，「具體機房」本無單一答案；(b) 即使知道機房，Google 不逐座公布 PUE、且 PUE 隨季節/負載/氣候波動。故**直接採 Google 公布的 fleet-wide 加權平均 PUE**（近年約 1.1 量級，正式值以當年度 Google 環境報告為準）。理由：資料確實存於 Google 機房，此值來源權威、可直接引用，較產業平均 PUE（含老舊自建機房、量級約 1.5–1.6）更貼合超大規模雲端業者實況。
  - **「每 GB 儲存功耗」為最難、最關鍵的係數（估法未定案）**：此係數量綱為**持續功率 W/GB 或 kWh/GB/年**（儲存的能耗特性是「資料只要還存著，承載硬碟就得持續通電」，非「寫入一次耗多少」），與 Agent 抓的 `usageInDrive`（當下存量 GB）量綱自洽。難估原因：硬碟冷/熱資料 W/GB 可差一個數量級、雲端多副本放大（1 GB 邏輯資料實佔 2–3 份物理儲存，倍數不公布）、儲存密度逐年改善（有時效性）、業界普遍不揭露（無如 PUE 的公開權威平均值）。
  - **採「硬碟規格反推」為主估法（透明、可防禦）**：以典型企業級資料中心硬碟公開規格直接算，例：一顆 ~18 TB HDD、運轉功耗取 ~7 W → 每 GB ≈ 7 W ÷ 18,000 GB ≈ 0.0004 W/GB ≈ 0.0034 kWh/GB/年（裸碟）；再乘一個保守的「基礎設施＋副本放大因子」（如 ×2～×3，補控制器/網路/陣列開銷與多副本），得約 ~0.006–0.01 kWh/GB/年（含機房內開銷、**未含 PUE**）作為儲存能耗強度。此法每個輸入皆可查、可在報告逐項交代來源，與電梯 [D4]、電腦 [D7]「合理分攤估算而非精確量測」定位一致。（替代來源：文獻/機構的雲端儲存能耗強度值，常見引用量級 ~0.005–0.02 kWh/GB/年，惟各研究邊界條件不一，採用時須挑明確標示「未含 PUE」者以免與 PUE 重複計算。）
  - **完整公式**：`雲端 CO₂e = usageInDrive(GB) × 每GB儲存能耗強度(kWh/GB/年，未含PUE) × PUE(fleet-wide ~1.1) × 台電電力係數 × 時間比例`。三段係數（儲存能耗強度、PUE、電力係數）皆屬 `EMISSION_FACTOR`／係數配置，後端維護、不寫死於 Agent；儲存能耗強度係數應標清 metadata：來源、年份、是否含 PUE、副本假設。
  - **待確認（實務）**：(1) 「每 GB 儲存功耗」係數的正式取值（硬碟型號 datasheet 功耗 vs 引用文獻）與副本放大因子，待查證當年度可引用來源後定值。(2) fleet-wide PUE 當年度實際數字待向 Google 最新環境報告查證。(3) 電力排放係數採台電當年度公告值。
- **[D10] 雲端路徑（路徑 C）的應用場景盤點與隱私分界**（v0.17，方向盤點）：先界定天花板——雲端儲存碳排量級相對電腦/電梯**偏小**（以 [D9] 係數粗估 `usageInDrive` 6.593 GB × ~0.008 kWh/GB/年 × 1.1 ≈ **0.058 kWh/年**，約一台電腦開數小時）。故此路徑的價值**不在減碳數字大，而在「行為可見、可執行、可教育」**。應用依此邏輯分四類，並依隱私成本分「可放心做」與「待決策」：
  - **（一）行為誘因類**（與 `usageInDriveTrash` 同家族，涉讀檔案清單、待隱私決策）：`storageQuota` 外，Drive API 可列檔案及 `size`／`mimeType`／`modifiedTime`，可做「前 N 大佔空間檔」「長期未動冷資料」清單、重複檔偵測、引導共享取代各自備份等「數位斷捨離」任務。把抽象 GB 變具體檔案、把「清垃圾桶」升級為「清冷資料」。即使單位碳排小，養成不囤積數位垃圾的習慣具教育與文化價值，且員工可一鍵執行。
  - **（二）趨勢與異常類**（零額外隱私成本，可放心做）：Agent 週期輪詢 `storageQuota` 天然累積 `usageInDrive` 時間序列。可做個人儲存成長趨勢線（把死數字變可追蹤趨勢）、異常暴增偵測（誤傳大批檔／備份亂同步／帳號被當儲存黑洞，對個人是提醒、對 IT/機構是揪異常帳號）。**此點接回 [D8] 待確認**：那個 342,884 GB 異常帳號，有時間序列即可判斷是「一直如此（疑機構/備份帳號）」或「突然暴增（疑異常）」，讓歸戶判斷更有依據。
  - **（三）機構層級洞察類**（彙總後才有量級，此路徑對企業最實質的價值）：單一員工 0.058 kWh/年很小，全機構加總才有意義——機構雲端儲存總碳排與成長率（數位資料只增不減，成長曲線對永續報告有敘事價值）、儲存效率 KPI（垃圾桶佔比／冷資料佔比，可跨部門比較設目標）、**ESG/永續報告素材**（「員工數位碳足跡」數據為 Scope 3／數位排放敘事的原始素材，可能是本路徑對企業最實質的價值，遠大於實際減下的電）。註：涉讀檔案清單的效率指標（如冷資料佔比）同屬待隱私決策。
  - **（四）教育與意識類**（零額外隱私成本，可放心做，且為本路徑真正強項）：雲端排放最「無感」（存著不佔桌面、看不到耗電），可視化教育意義最大——「你的雲端 = 一直開著的硬碟」具象化（GB 換算成硬碟持續運轉 X 小時，打破雲端免費/無限/零成本錯覺）、補齊「電腦＋列印＋雲端」完整數位碳足跡全貌（完整性即說服力）。
  - **隱私分界（須明確決策，比照印表機 [D5]/[D6]「可行但待前提確認」處理）**：（二）（四）（趨勢、教育）零額外隱私成本、最貼合本路徑核心價值，**可放心納入**；（一）（三）中凡涉「從讀配額數字跨到讀檔案清單/檔名/內容特徵」者，OAuth scope 需放大（唯讀配額 → 讀檔案 metadata 甚至內容）、員工授權意願與合規審查性質完全不同，**列為「可行但待隱私/scope 決策」**，不宜默默實作。此與專案一貫「歸戶對齊可控行為、同時尊重隱私」的張力直接相關。
- **[D11] 印表機 SNMP 五參數：隨綁定本地設定，不走 5.2 全域下發**（v0.18）：路徑 B 的 `HOST`／`COMMUNITY`／`PORT`／`OID` 屬**per-device 的區域環境事實**（各機 IP／機種不同、且僅本地網路可知），與 5.2 集中配置服務所管的**全域策略參數**（採樣頻率、批次、QoS）性質不同：後端根本不知道某台桌機接的印表機 IP，強行由 `sensor_config` 下發等於要後端發它沒有的資料，且會使該表從「策略表」退化為「逐機組態表」。故五參數隨 4.4.2 綁定於本機 `.env` 設定（實務上多數環境只需填 `HOST`，其餘用預設；非標準 index 機種另覆寫 `OID`），綁定時可將非敏感的 `HOST`／`OID` 上報供後台監控，但不由後端反向下發覆蓋本地。惟輪詢區間 `printerPollInterval`（多久讀一次）仍是全域策略，續走 5.2 下發（4.4.4）。判準與 [D8] 拒用 `limit`（全員雷同無區辨力）、[D3] 電梯「歸戶綁可控行為」一致：**per-device 本地事實不進 `sensor_config`，全域可調策略才進**。
  - **附註（參數調整時機與生效方式）**：五參數於綁定完成後由 Agent 讀取，與身份憑證解耦（不需 token／employee_id 即可填，掃碼前後填皆可）；且**可隨時修改**——改 `.env` 後重啟 Agent 即重讀生效（換印表機／填錯 IP 皆不需重走綁定），未來可選加 system tray 設定入口＋測試連線按鈕（存檔前跑一次 `snmpget` 驗證能否讀到 page counter）以降低 BYOD 填錯摩擦。惟 `printerPollInterval` 不在此列，仍走 5.2 全域下發。
- **[D12] `DIGITAL_USAGE` 採「一路徑一列」（方案 A），`path_type` 由 Agent 明送而非後端推斷**（v0.19）：三條感測路徑（A 電腦／B 印表機／C 雲端）觸發時機與欄位組各異，且 4.4.3 事件 ID 已定為 `id_token + 日期 + 路徑類型`。ERD 原 `DIGITAL_USAGE` 無路徑欄位，三路徑的列無從區分、亦無法建唯一鍵做 upsert，屬設計缺口。
  - **採方案 A（一路徑一列）**：新增 `path_type` 欄（列舉 `computer`／`printer`／`drive`，值域與 Agent 內部路徑識別一致，見下「列舉值取用 Agent 詞彙」），唯一鍵 =（`employee_id`, `usage_date`, `path_type`），三路徑各自成列、只填自身欄位組，其餘為 NULL。與 4.4.3 現行事件 ID 三段結構完全一致，改動最小。（否決方案 B「一天一列合併、以 partial update 各自 upsert 同列」：三路徑觸發時間不同會互相覆寫，需 partial update 語意，且與 4.4.3 事件 ID 定義不一致。）
  - **`path_type` 必須由 Agent 明送，不由後端從欄位樣態推斷**：(a) **零值與 NULL 難分辨**——`print_pages = 0`（當天沒列印但正常感測）、`drive_trash_gb = 0`（垃圾桶已清空，恰是激勵任務最想記錄的成功狀態）皆為合法資料，其欄位樣態與「該路徑未上傳」難以區分；(b) **推斷規則隨欄位演進而脆化**，新增路徑或欄位重疊時規則須跟改，形成隱性耦合；(c) **與冪等鍵不自洽**——`path_type` 既是唯一鍵組成，就必須在資料抵達時為確定明示值，用推導值當鍵等於讓去重正確性依賴推導規則不出錯；(d) **Agent 本就知道答案**——三路徑在 Agent 端是三個獨立採集器各自觸發，產生資料時百分之百知道自身路徑，丟棄該確定資訊再由後端猜回是把明確變模糊。
  - **欄位歸屬釐清**：Agent 上傳 = `id_token`（後端解析為 `employee_id`）＋ `usage_date` ＋ `path_type` ＋ 該路徑原始量；後端寫入 = `factor_id`、`co2e_kg`（依 5.1「Agent 純感測、碳排計算集中後端」與 [D7]，`factor_id` 係後端查 `EMISSION_FACTOR` 後才決定，Agent 無從得知，故不在 payload 內）。後端依 `path_type` **讀取明示值做分派**（決定查哪類係數），非推斷。
  - **列舉值取用 Agent 詞彙（`computer`／`printer`／`drive`，v0.20 修訂）**：本欄原訂為 `pc`／`printer`／`cloud`，與 Agent 內部路徑識別（`queue.PathType`）所用的 `computer`／`printer`／`drive` 不一致。後端資料庫當時已建置但尚無資料，故改以 Agent 值為準、DB 端調整，理由如下：
    - **避免引入翻譯層，與本決策自身的論證一致**：若兩端詞彙不同，Agent 送出前或後端落庫前必須有一層映射。本決策 (c) 點主張「用推導值當鍵等於讓去重正確性依賴推導規則不出錯」——一層詞彙映射同樣是「一條必須不出錯的規則」，只是換到另一個位置。`path_type` 既是冪等唯一鍵組成，映射一旦寫錯或漏改（例如新增路徑時），去重會**靜默失效**而非報錯。
    - **Agent 端該詞彙已是既成事實且不只用於 payload**：`computer`／`printer`／`drive` 同時是佇列 `events.path_type` 的列值、以及事件 ID 第三段（`id_token|usage_date|path_type`）的組成。改 Agent 等於同時改動事件 ID 值域與既有佇列檔內容；改 DB 端只是調整一個列舉約束，當時無資料、成本趨近零。
    - **兩套命名皆非完美，差異不足以支撐轉換成本**：`cloud` 較不綁定廠商，但欄位名本就是 `drive_usage_gb`／`drive_trash_gb`，`cloud` 與之並不一致；反之 `drive` 與欄位前綴一致，而 `computer` 與 `pc_*` 前綴不一致。兩者各有一處不對齊，語意上無歧義，故取「不必翻譯」者。
  - **附帶效益**：`path_type` 落庫後，[D10] 的分路徑趨勢分析、以及「某員工某路徑最近是否正常回報」的稽核查詢皆可直接查詢，不需由欄位樣態反推。

- **[D13] Eco-Agent 三條路徑一律改走 HTTPS，路徑 A／B 不再走 MQTT**（v0.20）：原設計路徑 A（電腦）／B（印表機）走 MQTT、路徑 C（雲端）走 HTTPS，屬同一顆 SVS 內的協定分裂。v0.20 統一為全 HTTPS，理由如下：
  - **「後端回 200 才清佇列」在 MQTT 上根本不成立**：4.4.3 的送達合約以 HTTP `200` 表述，但 MQTT 無此語意；QoS 1 的 PUBACK 由 **Broker** 而非後端發出。Agent 收到 Broker ack 即清本地佇列，資料卻可能仍在後端記憶體佇列未落地，此時後端崩潰即**永久遺失且 Agent 已無副本可重送**——「至少一次送達」在端到端層級有破口。改走 HTTPS 後，`200` 由後端於 commit 之後發出，收到即等價於已落地，破口自然消失。
  - **回程通道本來就是必需品，MQTT 給不了**：4.4.2 撤銷機制（`401/403` 自清憑證）與 5.2 配置版本號夾帶，兩者皆以 HTTP 回應語意書寫、且皆設計為「搭上傳往返的便車」。若 A／B 續走 MQTT，這條通道必須另行實作（另開一條 HTTPS，或走 MQTT 反向 topic 自建 request/response 語意）——等於為了保住 MQTT 而額外造一條 HTTPS，協定數量不減反增。
  - **MQTT 的優勢在此路徑用不上**：MQTT 的價值在極輕封包、高頻推送、大量無人值守裝置。而 Eco-Agent 依 4.4.3 實作註記為「狀態值輪詢／一天一筆累計事件」，**單一路徑單日僅佔佇列 1 筆**，上傳頻率極低、封包大小無關緊要；Agent 執行於有完整 TCP/TLS 堆疊的桌機而非受限硬體。以低頻上傳換取一條原生回應通道，取捨明顯。
  - **並非否定 MQTT 於本專案的地位**：廢棄物樹莓派續走 MQTT（4.2）——其為匿名單向推送、歸戶由後端配對 session 完成、不需要任何回程資訊，恰是 MQTT 的適用場景；5.2 對樹莓派的 retained 配置下發亦保留，且仍是本專案最具 SDN 特徵的控制通道。故混合協定架構（第 3 節）依然成立，只是**分流判準由「是不是 IoT 裝置」修正為「需不需要後端的回應」**，判準更清楚。
  - **否決的替代方案**：(a)「MQTT QoS 1 並延後 ack、由 Broker 保留未確認訊息」——技術上可行（consumer 關閉自動 ack、落庫後才回 PUBACK、Broker 開 persistence 與 persistent session），但只補上資料遺失，完全不解回程通道問題；且引入 inflight 窗口與批次門檻互鎖的陷阱（`max_inflight_messages` 若小於批次門檻，批次永遠湊不滿、永遠不 ack，直接卡死）。(c)「接受風險、將措辭降級為盡力而為」——碳排數據雖可容忍少量誤差，但本專題核心賣點即「填補員工行為數據採集的技術空白」，在送達保證上主動降級不利於論述。
  - **連帶影響**：第 2、3 節協定表與分流敘述、4.4 路徑表協定欄與 payload 段（topic → REST 端點）、4.4.2 撤銷機制（原生成立）、5.1 寫入策略（分 MQTT／HTTPS 兩軌）與 P2／P3 工作項、5.2 數據面職責與配置參數表、技術堆疊 Desktop Agent 列（移除 MQTT client 依賴）皆已同步更新。
- **[D14] `DIGITAL_USAGE` 冪等去重定案：補 `collected_at` 勝出規則、唯一鍵粒度依路徑分三組**（v0.20 定案，v0.23 修正路徑 B）：[D12] 定的唯一鍵（`employee_id`, `usage_date`, `path_type`）有兩個缺口，v0.20 一併補齊；其中缺口二在路徑 B 上的解法於 v0.23 修正（原比照電腦路徑採 `device_id`，會使同一台印表機被兩台裝置重複計算）。
  - **缺口一：無法判定同日先後（對應原 (3)）**。三條路徑送的皆為**當日／壽命累計值**、後到覆蓋先到；重送的舊封包若晚於新封包抵達，會把較新的累計值蓋回舊值。原 payload 僅有 `usage_date`（日期粒度），同日兩筆無從比較。**解法**：payload 與 `DIGITAL_USAGE` 皆補 `collected_at`（Agent 採集時間戳，UTC），upsert 加條件 `WHERE EXCLUDED.collected_at > digital_usage.collected_at`。用 Agent 端時間戳而非後端接收時間，因為要比較的是「哪一次採集較新」而非「哪一個封包先到」——後者正是亂序問題本身。
  - **缺口二：鍵粒度與資料本質粒度錯位（對應原 (5)；路徑 B 之修正於 v0.23 補入）**。[D12] 的三段鍵第一段為 `employee_id`（**人**的粒度），而 4.4.3 事件 ID 第一段 `id_token` 依 4.4.2 屬**每台裝置一枚**（裝置粒度）——兩者錯位。但正確的鍵粒度並非一律補上 `device_id`，而須看**該路徑實際測量的對象是什麼**：

| 路徑 | 改動前資料粒度 | 問題 | 改動與改動後資料粒度 |
|------|--------------|------|-------------------|
| A 電腦 | per-employee（[D12] 三段鍵） | 一員工綁桌機＋BYOD 筆電（本專案 BYOD 定位下屬正常情境），同日各送 `computer`：Agent 端為兩個相異事件 ID、落庫卻撞同一鍵，後到者覆蓋先到者，**吃掉一台電腦的用量**（低估） | 鍵加 `device_id` → **per-device**。電腦本身即測量主體，兩台各自耗電，分列後於員工層加總正確 |
| B 印表機 | per-employee（[D12] 三段鍵） | 同樣會撞鍵；但若比照路徑 A 改以 `device_id` 分列，**桌機與筆電同指一台專屬印表機**時，兩個 Agent 各自回報同一台機器的頁數，加總即**重複計算**（高估）。倍率隨兩台開機重疊區間浮動（1x～Nx），無法事後除以裝置數修正 | 鍵加 **`printer_serial`**（SNMP 讀取，**非** `device_id`）→ **per-printer**。測量主體是印表機，裝置只是觀測者；同一台機器無論被幾台裝置觀測都只有一列 |
| C 雲端 | per-employee（[D12] 三段鍵） | **無撞鍵問題**——同員工多裝置查的是同一個 Google 帳號、回傳同一數值。反之若比照加 `device_id` 分列加總，會憑空多算一倍 | 鍵維持三段、不加任何裝置欄 → **per-account** |

  - **判準：裝置是「主體」還是「觀測者」**。路徑 A 的裝置是被測量的東西本身；路徑 B／C 的裝置只是拿感測器去讀「外部某個對象」（印表機／Google 帳號）的狀態。鍵粒度須跟著**被測量的對象**走，而非跟著觀測者走。v0.20 初訂時正確識別出 C 屬觀測者而排除 `device_id`，卻誤將 B 歸入 A 一組——當時行文已載明「路徑 B 為 per-printer，兩台電腦各接**不同**印表機則加總正確」，但選定的鍵並未去約束那個「不同」的前提，條件寫在文字裡、機制未兌現，v0.23 補正。
  - **附帶效益（路徑 B）**：以序號為鍵後，兩名員工的 Agent 若都指向同一台機器（違反「個人專屬」前提、原本完全偵測不到），後端會看見同一 `printer_serial` 掛在兩個 `employee_id` 底下，可主動告警。一個靜默錯誤因此變成可稽核的條件。

**實作細節**

  - **三組 partial unique index**：鍵粒度分三組，以三個 partial unique index 分別表達（SQL 見 5.1）。**不可合併為單一多段 constraint**——非該路徑的鍵欄位為 NULL，而 PostgreSQL 預設視 NULL **互不相等**，合併後雲端路徑的去重會**靜默失效**（不報錯、只重複計算）。（PG 15+ 的 `UNIQUE NULLS NOT DISTINCT` 可達同等效果，仍採 partial index，因其把鍵粒度差異顯式寫進 schema，可自我文件化。）
  - **`printer_serial` 取得與退化路徑**：SNMP 依序試 `prtGeneralSerialNumber` → `entPhysicalSerialNum` → `sysName`（OID 與退化規則見 4.4.2）。低階機種可能三者皆空，屬待實測項；全空時回退以 `device_id` 歸鍵並標記該列「印表機身份不明」。不以 IP 當鍵（DHCP 會變、跨辦公室私有 IP 會誤撞）。
  - **payload 影響**：`device_id` 不需 Agent 上送（後端以 `id_token` 查 `DEVICE_BINDING` 本就同時取得 `employee_id` 與 `device_id`）；但 **`printer_serial` 必須上送**——它是本地網路事實，後端無從得知（同 [D11] 之判準）。
  - **`employee_id` 保留於列上、不改為靠 join 動態推導**：加入 `device_id` 後 `employee_id` 看似可經 `DEVICE_BINDING` 推導而冗餘。但裝置可重新綁定給不同員工（離職轉交、換人使用），若歸戶靠 join 推導，裝置一轉手**歷史資料的歸戶會被追溯改寫**。存為快照才能凍結「當時算在誰頭上」。（與 4.2 `WASTE_EVENT` 靠 `session_id` join 才知是誰的作法相反，因 session 為一次性、不會轉手，取捨基礎不同。）
  - **下游聚合的連帶約束（P2 實作須注意）**：員工層查詢須先依該路徑的鍵欄位加總（電腦依 `device_id`、印表機依 `printer_serial`）。`co2e_kg`、時數、頁數可加總；`pc_avg_cpu_util`（平均值）與 `cpu_model`（字串）**不可直接加總或任取一筆**，須依 active 時數做加權平均，或規定此二欄僅在裝置層檢視。另多裝置下員工單日時數可能超過 24 小時（兩台機器各自耗電，物理上正確但介面上反直覺），建議 App／儀表板對員工層只顯示 `co2e_kg`，時數留待裝置分項展開。
  - **ERD 改動**：`DIGITAL_USAGE` 補 `device_id` FK、`collected_at`、`printer_serial`；`DEVICE` 補 `display_name`（綁定時由 Agent 送 hostname，或由員工自填如「辦公室桌機」）——裝置分項一旦對使用者可見，UUID 無法辨識是哪一台，此欄為必要而非選配。
  - **否決「明訂一員工限綁一裝置」**：(a) 與 BYOD 定位直接衝突；(b) 後果更嚴重——多裝置撞鍵是「後到覆蓋先到」（少算一台、數字偏低但仍有資料），限綁一台則是「第二台完全無從採集」（直接沒有資料）；(c) 並不省事——現況 `EMPLOYEE ||--o{ DEVICE_BINDING` 為一對多、ERD 本就允許多綁定，真要強制須加 `UNIQUE (employee_id) WHERE status='active'`，**兩條路都要動 ERD**，只是動的表不同。

- **[D15] 路徑 B 改送 SNMP 壽命累計讀數，區間差分移至後端**（v0.23）：原設計由 Agent 在本機以「本次讀數 − 上次讀數」算出 `print_pages` 後上送。v0.23 改為 Agent 原樣上送 `printer_page_counter`（壽命累計絕對值），頁數由後端以「當日最新讀數 − 前一日最新讀數」求得。
  - **主要理由：baseline 目前只活在 Agent 本機**。重裝 Agent、換機、佇列檔毀損即遺失 baseline——輕則整段用量憑空消失，重則從 0 重算、把整台印表機的壽命累計頁數一次記到某位員工頭上。把累計值送上來，差分基準便落在有完整歷史的後端。此問題與 [D14] 無關，屬獨立缺陷。
  - **與 [D14] 的配合**：[D14] 讓同一台印表機在同員工同日只有一列，但**多個觀測者各自從自己的 baseline 算出的 delta 根本不是同一個量**，取誰都不對，`collected_at` 最新者勝的規則在 delta 語意下失效（該規則的隱含前提是「同一個鍵只有一個觀測者」）。改送絕對讀數後，多個觀測者讀同一台機器得到的是**同一個值**，「最新者勝」重新成立。
  - **語意與路徑 C 對齊**：改完後路徑 B 與 C 皆為「送絕對狀態值、最新者勝、後端做換算」，三條路徑的模型一致。`print_pages` 亦順勢由 Agent 上送欄位改為**後端計算欄位**，與 `factor_id`／`co2e_kg` 同組，符合 [D7]「Agent 純感測、計算集中後端」原則。
  - **計數器重置防呆一併移至後端**：原 4.4.2 所訂「本次 < 上次視為碳粉計數器更換／韌體重置、該區間跳過」改由後端依歷史列判定。後端有完整歷史，判得更準，且判定結果留得下稽核紀錄。
  - **佇列模型不受影響**：仍是每印表機每日一列（狀態值輪詢、當日最新讀數覆蓋同一筆），4.4.3 關於 `thresholdCount` 之推論照舊。
  - **新引入的邊界情形（P2 實作須處理）**：整天無人開機則該日無讀數，下次讀到的差值會橫跨數日、被記在單一天。可選按日均攤或註記為「跨日補記」，屬實作細節，但須明訂——否則儀表板會出現無法解釋的尖峰。
  - **成本**：本項動到 payload 與 Agent 既有實作（Step 1.3 已在跑），非零成本。若時程吃緊，[D14] 的 `printer_serial` 歸鍵可單獨先做——重複計算即已消除，代價是「較晚開機那台的較短觀測窗」可能勝出而低估。低估比高估在報告論述上好交代，可作為過渡。
- **[D16] 手動上傳用紙量落地定案：走 `digital-usages`、以 `sensing_mode` 欄位區分手動／自動、App 端彙總一天一列、兩管道互斥**（v26）：[D6] 已把「手動上傳用紙量」列為印表機路徑 B 的優先開發備選，但其**落庫端點、來源識別方式、去重鍵、與 Agent 自動路徑的關係**一直未定案。本決策一次補齊，並與《驗證機制端點關係表》§3.1 點出的 `digital-usages`（複數）vs `agent/digital-usage/batch` 定位落差對齊。
  - **(1) 端點定位——`digital-usages` 正式保留作 App 手動補登管道**：`DIGITAL_USAGE` 的資料自此有**兩個並存來源**——Agent 自動感測（走 `agent/digital-usage/batch`、asyncpg 直連 pooler、裝置 `Bearer`）與 App 手動上傳（走既有 `POST /api/digital-usages`、PostgREST 泛用 CRUD、員工 `Bearer`）。兩者分屬 [D4] 分流表的兩側：手動上傳是使用者一次一筆的主動輸入，**無本地佇列重送、無亂序、無 per-device／per-printer 去重需求**，不需要條件式 upsert／交易控制,故落在 PostgREST 這一側,不必動用 asyncpg 直連。（否決「手動上傳也走 batch」：把不需要交易控制的路徑塞進為交易控制而生的端點，徒增耦合。）
  - **(2) 端點更名——Agent 寫入端點收進 `/api/agent/*` 命名空間**：原規劃的 `POST /api/digital-usage/batch`（單數）與既有 `POST /api/digital-usages`（複數）僅差一個複數 `s`，又同寫一張 `DIGITAL_USAGE` 表，命名極易混淆、易誤呼叫。定案將 Agent 那條更名為 **`POST /api/agent/digital-usage/batch`**，與 4.4.2 綁定鏈四端點（`agent/binding-code`、`agent/bind`…）同組，確立「凡 `/api/agent/*` 即 Agent 裝置認證體系（asyncpg、條件式 upsert）」的清楚邊界；App 手動路徑維持 `/api/digital-usages` 不動。改 Agent 那條而非改 App 那條的理由：Agent 端點**尚未實作**（一行未寫），現在正名零成本；`digital-usages` 已實作、已套 `get_current_employee`、且與其餘三大模組泛用 CRUD 命名一致（全為複數表名），動它牽連一整排。兩條路徑真正的區別不在「單筆 vs 批次」，而在**認證體系**（員工 vs 裝置）——命名應反映此差異，故用命名空間而非複數 `s` 承載。
  - **(3) 來源識別——新增獨立欄位 `sensing_mode`（`auto`／`manual`），而非於 `path_type` 加值**：手動上傳落 `DIGITAL_USAGE` 時,若記為 `path_type = printer` 卻缺 `printer_serial`,會踩中 [D14] 的 per-printer 唯一鍵在 `printer_serial` 為 NULL 時「NULL 互不相等、去重靜默失效」的陷阱。故須有一個欄位區分它與 SNMP 自動路徑。**採獨立欄位 `sensing_mode`（`auto`／`manual`）而非在 `path_type` 加 `printer_manual` 值**，理由是**正交性**：`path_type`（`computer`／`printer`／`drive`）描述**感測對象**，「手動 vs 自動」描述**感測方式**,兩者是獨立的軸；混進 `path_type` 會使值域「三個講對象、一個講對象＋方式」語意不齊,且未來若電腦、雲端也開手動補登,得為每個組合造 `computer_manual`／`drive_manual` 新值（對象×方式笛卡兒積、組合爆炸）,查「所有手動資料」得列舉一長串。拆獨立欄位則 `path_type` 恆三值、`sensing_mode` 恆兩值,自由組合,查驗（按來源方式篩選）乾淨（`WHERE sensing_mode='manual'`）,呼應本次「方便後續查驗感測資料」之目的。**現況取捨聲明**：本專案手動補登**目前只用於印表機**（其定位即「共用機／無 SNMP 場域的補位」）,不預期擴散；選 `sensing_mode` 拆欄**不是因為現在會擴散,而是保留擴充性、換取模型正交與查驗乾淨**,代價是唯一鍵須納入 `sensing_mode`（見 (4)）。（否決 `path_type` 加值:落地雖對既有鍵零擾動、最省,但值域語意不齊、擴充即膨脹,與本專案 [D12] 以來「來源資訊明確標記、不靠事後推斷」的一貫取向不合。）
  - **(4) 去重鍵與彙總——App 端彙總、後端一天一列 upsert**：採「App 端彙總、後端一天一列」（呼應本決策的最簡落地）——員工當日可多筆記錄、每筆於**送出前在 App 本地編輯**,App 以某觸發機制上傳**當日彙總後的總用紙量一筆**,後端 upsert 進唯一鍵 **（`employee_id`, `usage_date`, `path_type='printer'`, `sensing_mode='manual'`）** 那一列。因彙總與多筆去重、編輯皆在 App 端（送出前）完成,後端**不存明細、不需 per-筆 `event_id` 冪等、不需 asyncpg**——`sensing_mode` 進入唯一鍵後,PostgREST 的無條件 upsert 覆蓋當日那列即足夠;`collected_at` 勝出規則仍**沿用作防亂序重送的保險**（弱網逾時重送同一筆彙總時,不讓舊封包蓋新值）,與「編輯」無關（編輯已在本地完成,落庫後不再由 App 重送修改版）。連帶:SNMP 自動路徑的既有 per-printer partial index 須明確帶 `sensing_mode='auto'`,`DIGITAL_USAGE` 的 partial unique index 由三個增為四個（見 5.1 冪等去重）。
  - **(5) 兩管道互斥——無雙重計算**：`sensing_mode` 一分開,手動列與自動列各自成列、各自通過去重,若同一台個人專屬機**既有 Agent SNMP 又有員工手動補登**,加總會重複計算。故明訂**兩管道互斥前提**:**個人專屬印表機一律走 Agent SNMP（`auto`）,手動上傳只用於共用印表機（無 Agent SNMP 之場域）**。此與 [D6] 中手動上傳的原始定位（「共用機或缺乏集中列印設施場域的補位」）一致,互斥前提成立則同一台機器不會同時產生 `auto` 與 `manual` 兩列,雙重計算風險自然消解。
  - **對既有決策的影響**：不動 [D12]（`path_type` 三值與「Agent 明送不推斷」原則不變,`sensing_mode` 為新增的正交欄位）、不動 [D14] 的三組 Agent 自動路徑鍵（僅為其 partial index 補上 `sensing_mode='auto'` 述詞）、不動 [D4] 分流判準（手動上傳因不需交易控制而落 PostgREST 側,正是判準的正確套用）。ERD `DIGITAL_USAGE` 新增 `sensing_mode` 欄位。

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
| IoT 傳輸 | MQTT（Mosquitto Broker，**僅廢棄物樹莓派**上行與配置下發）、NFC（近場通訊）、SNMP（Eco-Agent 讀印表機 page counter 與序號） |
| 外部 API | TDX 運輸 API、Google Maps API、Google Drive API v3 |
| 後端 | **FastAPI（Python，async）** ＋ **Supabase（代管 PostgreSQL）**；資料存取雙層——**Supabase PostgREST**（一般 CRUD）＋ **asyncpg 直連 connection pooler**（`agent/digital-usage/batch`，v26 [D16] 更名；見 5.1 [D4]）；碳排運算引擎 ＋ 係數資料庫 ＋ MQTT consumer 批次寫入（廢棄物）＋ HTTPS 批次上傳端點與冪等 upsert（Eco-Agent）。部署：Docker Image → GitHub Actions → Hugging Face Spaces |
| 控制架構 | SD-IoT Controller（控制面／數據面分離；自建輕量版，實作為 FastAPI 內管理模組，詳見 5.2） |
| Desktop Agent | **Go**（單一靜態執行檔，跨平台交叉編譯）；HTTPS/SNMP/OAuth2 函式庫（v0.20 起不再需要 MQTT client，見 4.4 [D13]）；DPAPI／Keychain 憑證保護 |

### 5.1 後端框架與資料庫：FastAPI + Supabase

> 服務對象：四大模組全部的資料入庫、運算與查詢（橫切層，所有裝置經 FastAPI 進資料庫）。已由團隊成員完成初步部署，現有三段網址：託管平台（Space）、API base URL、Swagger 自動文件（`{base_url}/docs`，由 FastAPI 依路由與 Pydantic 模型自動生成）。

#### 規格（現行定案）

- 後端採 **FastAPI（Python，async）**；資料庫採 **Supabase（代管 PostgreSQL）**，ERD（`eco_sensing_erd.mmd`）直接對應建表。

**資料存取層分工（v0.21 定案，見 [D4]）**

後端對資料庫的存取**分兩層並存**，依「該端點需不需要完整 SQL 表達力」分流：

| 存取層 | 適用端點 | 實作 | 說明 |
|--------|----------|------|------|
| **Supabase PostgREST（HTTP）** | 一般 CRUD（`companies`／`departments`／`employees`／`emission-factors`／`travel-records`／`waste-*`／`devices`／`elevator-trips`／`digital-usages` 單筆）；**含 App 手動上傳用紙量**（`POST /api/digital-usages`、`sensing_mode='manual'`，見 4.4 [D16]） | 既有 `db/supabase.py` ＋ `services/crud.py` 泛用層 | table-agnostic，端點路徑即表名，開發成本趨近零；P1 直通版主力。手動上傳因 App 端已彙總、無條件式 upsert／交易控制需求，落此軌（[D16]） |
| **asyncpg 直連 connection pooler（PostgreSQL wire protocol）** | **僅 `POST /api/agent/digital-usage/batch` 一條**（Eco-Agent 自動感測；v26 [D16] 由 `digital-usage/batch` 更名、收進 `/api/agent/*` 命名空間） | 新增 `db/pool.py` ＋ `services/digital_usage.py` | 需要 `ON CONFLICT ... WHERE EXCLUDED.collected_at > ...` 條件式 upsert、partial index 作 conflict target、單一交易內完成後才回 `200`——三者皆超出 PostgREST 表達力 |

- 直連走 Supabase **connection pooler（Transaction mode，port 6543）**，避免 async 高併發耗盡資料庫連線數；transaction mode 下須設 `statement_cache_size=0`（asyncpg），且不可使用 session 層級功能（`LISTEN/NOTIFY`、session `SET`、server-side prepared statements）。
- 連線字串以新增環境變數 `SUPABASE_DB_URL` 提供（`postgresql://...:6543/...`），與既有 `SUPABASE_URL`／`SUPABASE_KEY`（PostgREST 用）**並存而非取代**；pool 於 FastAPI lifespan 建立、掛於 `app.state`。
- **既有 `services/crud.py` 與所有既有 router 不需改動**；本決策為「加一條路」而非「換一條路」。
- **架構分工原則**：Supabase 管「資料存哪裡」；FastAPI 管「資料進來後怎麼算」——請求驗證、排放係數查詢、CO₂e 計算、廢棄物 session 配對歸戶、獎勵（EXP／碳幣）發放。App、樹莓派、Eco-Agent 一律經 FastAPI 進資料庫，**不直接讀寫 Supabase**，維持商業邏輯集中與控制面／數據面分離。
- **資料寫入策略（v0.20 起依來源分兩軌）**：
  - **MQTT 軌（廢棄物樹莓派）**：MQTT Broker（Mosquitto）本身即為天然緩衝（訊息佇列）：後端 MQTT consumer 訂閱 topic → 記憶體佇列累積 → 定時／定量**批次寫入（batch insert）** Supabase。
  - **HTTPS 軌（Eco-Agent，4.4 [D13] 起三路徑全走此軌）**：批次緩衝改由 **Agent 本地持久化佇列**承擔（4.4.3），後端**不再為此路徑設記憶體佇列**——收到一批（`uploadBatchMax` 上限 720 筆）即於**單一交易內**完成去重與 upsert，**commit 之後才回 `200`**。後端不緩衝反而是此軌的正確設計：唯有如此「`200` = 已落地」才成立，4.4.3 的端到端至少一次送達才無破口。**嚴禁先回 `200` 再非同步落地。**
  - App 端 HTTPS 事件（差旅上傳、NFC 電梯、廢棄物 session 開啟／投入完畢）為低頻請求，即時直寫。**寫入端不設獨立 cache 層**（評估依據見決策記錄 [D3]）。
- **`DIGITAL_USAGE` 冪等去重（兩層並用，v0.20 定案；設計依據見 4.4 [D14]）**：
  - **應用層——收批後先依鍵摺疊**：同一唯一鍵只留 `collected_at` 最新的一筆，再組 upsert 語句。此為**必要步驟而非最佳化**：PostgreSQL 不允許同一 `ON CONFLICT DO UPDATE` 語句內有兩列衝突到同一鍵（報 `command cannot affect row a second time`），而本模型「每次輪詢以當日累計覆蓋同一筆」使重送的舊封包與新封包極易落在同一批次視窗內、構成同鍵。
  - **DB 層——unique index 作最後防線**：`INSERT ... ON CONFLICT (...) DO UPDATE SET ... WHERE EXCLUDED.collected_at > digital_usage.collected_at`。應用層摺疊解決單批次內衝突、DB constraint 擋跨批次與（P3 多實例後）跨行程重複，兩層職責不同、不可互相取代。
  - **鍵依路徑分四組（v26 [D16] 新增手動路徑），以 partial unique index 表達**：

    ```sql
    -- 電腦：per-device，一裝置一列（電腦本身即測量主體），員工層碳排以加總取得
    CREATE UNIQUE INDEX uq_digital_usage_device ON digital_usage
      (employee_id, usage_date, path_type, device_id)
      WHERE path_type = 'computer' AND sensing_mode = 'auto';

    -- 印表機（自動，Agent SNMP）：per-printer，一印表機一列（裝置僅為觀測者）
    -- 以 device_id 分列會使桌機＋筆電同指一台印表機時重複計算，見 [D14]
    CREATE UNIQUE INDEX uq_digital_usage_printer ON digital_usage
      (employee_id, usage_date, path_type, printer_serial)
      WHERE path_type = 'printer' AND sensing_mode = 'auto';

    -- 印表機（手動，App 上傳）：per-employee-day，一員工一天一列
    -- 手動上傳無 printer_serial（NULL），故不套 per-printer 鍵、以 sensing_mode 分列，見 [D16]
    -- App 端已彙總當日總量，後端 upsert 覆蓋此列即可
    CREATE UNIQUE INDEX uq_digital_usage_printer_manual ON digital_usage
      (employee_id, usage_date, path_type, sensing_mode)
      WHERE path_type = 'printer' AND sensing_mode = 'manual';

    -- 雲端：per-account，同員工多裝置查得同值，納入裝置欄會重複計算
    CREATE UNIQUE INDEX uq_digital_usage_account ON digital_usage
      (employee_id, usage_date, path_type)
      WHERE path_type = 'drive' AND sensing_mode = 'auto';
    ```

    **不可合併為單一多段 unique constraint**：非該路徑的鍵欄位（雲端列的 `device_id`／`printer_serial`、電腦列的 `printer_serial`、手動列的 `printer_serial`）為 NULL，PostgreSQL 預設視 NULL 互不相等，合併後該路徑去重會**靜默失效**（不報錯、只重複計算）。PG 15+ 可改用 `UNIQUE NULLS NOT DISTINCT` 達同等效果，仍採 partial index，因其把鍵粒度差異顯式寫入 schema。
    **`sensing_mode` 述詞的作用**：四個 index 皆帶 `sensing_mode` 條件,使 Agent 自動路徑（`auto`）與 App 手動印表機路徑（`manual`）在同一台機器同員工同日各自成列、互不撞鍵——這正是 [D16] 兩管道並存所需;而兩管道**互斥於場域**（個人專屬機走自動、共用機走手動）,故實務上同一（`employee_id`, `usage_date`, `printer`）不會同時出現 `auto` 與 `manual` 兩列,不致雙重計算（見 [D16]）。
  - **實作註記（v0.21，v26 補述）**：以 partial index 作 conflict target 時，`ON CONFLICT` 子句**必須原樣重述該 index 的 `WHERE` 述詞**（含 `sensing_mode` 條件），PostgreSQL 才能匹配到對應 index。**此三句 upsert 屬 Agent 自動路徑**（`POST /api/agent/digital-usage/batch`，asyncpg 直連）：一批資料依鍵粒度拆成 `computer`／`printer`（`sensing_mode='auto'`）／`drive` **三句 upsert** 於**同一交易內**執行——此語法位置在 PostgREST 不存在，為 [D4] 分流至直連的直接原因。**App 手動印表機路徑（`sensing_mode='manual'`）不在此三句內**：它走 `POST /api/digital-usages`（PostgREST 泛用 CRUD），App 端已彙總當日總量、以無條件 upsert 覆蓋 `uq_digital_usage_printer_manual` 那一列即可（見 [D16]）。
  - **四個 partial unique index 尚未於 `schema.sql` 建立**（截至 2026-08，資料庫已建表但**無任何資料**；v26 由三個增為四個——新增 `uq_digital_usage_printer_manual`），列為 P1 優先工作項；未建立前 `ON CONFLICT ... WHERE` 會因找不到匹配的 unique constraint 直接報錯。
  - **員工層聚合須先依該路徑鍵欄位加總**（電腦依 `device_id`、印表機依 `printer_serial`）；`pc_avg_cpu_util` 與 `cpu_model` 為不可加總欄位，處理原則見 4.4 [D14]。
  - **`print_pages` 為後端計算欄位（v0.23 [D15]）**：Agent 送 `printer_page_counter`（壽命累計讀數），後端以「當日最新讀數 − 前一日最新讀數」差分求得 `print_pages` 並落庫；計數器重置（本次 < 上次）之防呆亦由後端依歷史列判定。
- **讀取端快取**：排行榜與企業端儀表板的聚合查詢採**讀取端快取**（TTL 約 5 分鐘），初期以 FastAPI 行程內記憶體快取實作，規模擴大後升級 Redis（sorted set 天生適合排行榜）。
- 容錯備註：**MQTT 軌（廢棄物）**之記憶體佇列於後端崩潰時可能遺失數秒內未落地資料（碳排數據可容忍）；如需強化，將 MQTT QoS 設為 1 並延後 ack（consumer 關閉自動 ack、落庫後才回 PUBACK），由 Broker 保留未確認訊息——惟須注意 `max_inflight_messages` 必須大於批次門檻，否則批次永遠湊不滿、永遠不 ack 而互鎖，且須開 Broker persistence 與 persistent session。**HTTPS 軌（Eco-Agent）不受此限**：其緩衝在 Agent 本地磁碟，後端崩潰時 Agent 收不到 `200`、資料仍在本機佇列，下次觸發自然重送（4.4.3）。

**分階段開發步驟**

| 階段 | 目標 | 主要工作項目 | 完成判準 | 狀態 |
|------|------|--------------|----------|------|
| P1 直通版 | API 跑通、資料落地 | **App 端後端認證（前置工作，見 [D5]）**：`EMPLOYEE` 補 `password_hash`、`POST /api/auth/login` 驗帳密後**簽發 App 雙 token（Access 1h＋Refresh 30 天）**、App 端換發端點（比對 `refresh_token_hash`）、FastAPI 驗證 dependency（`get_current_employee`）、App 端 `DemoAuthStorage` 改存真 token 於 `flutter_secure_storage`（Refresh）、冷啟動以 Refresh 靜默續 Access（對應 §1.0）；Supabase 依 ERD 建表（**含補建四個 partial unique index，見 5.1 冪等去重；v26 新增手動印表機路徑 `uq_digital_usage_printer_manual`**，並為既有三個補上 `sensing_mode` 述詞）；FastAPI 實作四大模組寫入／查詢 API（逐筆直寫，不加緩衝，走 PostgREST 泛用 CRUD 層；**含 App 手動上傳用紙量 `POST /api/digital-usages`、`sensing_mode='manual'`，見 [D16]**）；以 Swagger 測通全部端點；App 假資料改串真 API；**Eco-Agent 綁定鏈落地（索取 `binding_code` → App 掃碼核銷 → 建 `DEVICE_BINDING` → 發雙 token）** | 四大模組資料皆可經 API 寫入並查回；`employee_id` 全數由憑證解出而非 client 指定；Agent 可完成一次完整綁定 | ⬜ 未開始 |
| P2 批次與快取版 | 效能與穩定 | MQTT consumer ＋ 記憶體佇列批次寫入（**廢棄物**）；**Eco-Agent HTTPS 批次上傳端點與冪等 upsert**（新增 `db/pool.py` asyncpg 直連 pooler ＋ `services/digital_usage.py`，見 [D4]；應用層摺疊、partial unique index、`collected_at` 勝出規則、commit 後才回 `200`）；碳排運算引擎（`factor_id`／`co2e_kg` 後端計算）；排行榜／儀表板讀取端快取（TTL 5 分）；廢棄物 session 逾時結算與互斥鎖落地 | 批次寫入上線；重送同一批不產生重複列、亦不覆蓋較新值；儀表板重複查詢不重算 | ⬜ 未開始 |
| P3 擴充版（視規模啟用） | 大規模部署韌性 | 導入 Redis（排行榜 sorted set、跨實例共享快取）；MQTT QoS／重送策略（廢棄物軌）；基本監控與告警 | 多後端實例部署下快取結果一致；多實例並行寫入時 DB unique index 仍擋住重複 | ⬜ 未開始 |

#### 決策記錄（脈絡與依據）

- **[D1] FastAPI 選型理由**（v0.5）：async 非同步 I/O 適合大量裝置並發上傳的 IoT 場景；Pydantic 自動驗證請求欄位與型別，減少手寫防呆；自動生成 OpenAPI／Swagger 互動文件，前後端分工對接與測試成本低；與 Edge AI（YOLOv8n）、OCR 前處理腳本同為 Python，工具鏈一致。
- **[D2] Supabase 選型理由**（v0.5）：免自架、免管備份的雲端 PostgreSQL；附帶 Auth、Row Level Security、Realtime、Storage，未來可漸進採用。
- **[D3] 寫入端不設獨立 cache 層的評估**（v0.5）：專題規模（實驗場域數十名員工）之寫入頻率低——Desktop Agent 以本地佇列批次上傳（累積達量／關機前／開機後／最長滯留觸發，詳見 4.4.3）、其餘模組皆為事件驅動——寫入端不需獨立 cache 層，避免過早最佳化增加故障點。讀取端才是快取重點。
- **[D4] 資料存取層採「PostgREST 為主、Agent 批次單條直連 pooler」的混合作法**（v0.21；該直連端點於 v26 [D16] 由 `digital-usage/batch` 更名為 `agent/digital-usage/batch`，以下敘述沿用定案時原名）：團隊成員已完成的初步後端（`eco_sensing_backend`）採 **Supabase PostgREST** 存取資料庫——`services/crud.py` 為 table-agnostic 的泛用 CRUD 層，端點路徑即表名，所有 router 共用同一組 `list/get/create/update/delete`。此與 5.1 原文「FastAPI 一律走 connection pooler」的字面敘述不同，v0.21 就此正式定案為**兩層並存**，理由如下：
  - **分歧只發生在一條路徑上**。PostgREST 的設計邊界在於「以 query string 表達查詢」，對 4.4 [D14] 所要求的三件事無法表達：(a) 條件式 upsert——`Prefer: resolution=merge-duplicates` 只能「撞鍵即無條件覆蓋」，沒有可以寫 `WHERE EXCLUDED.collected_at > digital_usage.collected_at` 的語法位置，而無條件覆蓋**正是 [D14] 缺口一明確要防的行為**（重送的舊封包會蓋掉較新的累計值）；(b) **partial unique index 作 conflict target**——`?on_conflict=` 無法附帶 index 的 `WHERE` 述詞，三種鍵粒度（v0.23 [D14]）無從區分；(c) **「單一交易內完成摺疊與 upsert、commit 之後才回 `200`」**——PostgREST 一請求一交易，但無法把應用層摺疊後的三句條件式 upsert（電腦／印表機／雲端各一句，見 [D14]）組進同一請求。其餘二十餘個 CRUD 端點完全不觸及這三點。
  - **此非效能取捨，而是表達力取捨**。PostgREST 自身亦是連著 pooler 的程式，兩者不在同一層次、不互斥；「改走直連」買到的是完整 SQL，不是更快的連線。
  - **`200` 的語意是地基，不可讓步**。4.4.3 的端到端至少一次送達完全建立在「收到 `200` = 已落地」上（[D13]），5.1 亦明訂「嚴禁先回 `200` 再非同步落地」。若為了遷就 PostgREST 而改用無條件覆蓋或事後補償，等同把 [D13]／[D14] 兩項 v0.20 定案一起推翻，代價遠大於多開一條存取路徑。
  - **否決方案 B（PostgreSQL function ＋ PostgREST `/rpc/`）**：可保持單一存取層，但把去重與勝出規則整套下沉進 SQL function——邏輯離開 Python 測試框架、變更須走 migration、除錯困難；且冪等正確性是本模組最需要單元測試覆蓋的部分，反向操作不划算。
  - **否決方案 C（整體改回直連、重寫 `crud.py`）**：既有泛用 CRUD 層運作正常且開發成本已付出，為一條路徑重寫二十餘個端點屬純粹浪費；且 P1 的完成判準是「四大模組資料皆可經 API 寫入並查回」，PostgREST 正是達成此判準的最短路徑。
  - **代價（已知並接受）**：專案內存在兩種資料存取風格，新進成員需理解分界。以「**是否需要條件式 upsert／交易控制**」為單一判準劃線，並於 `services/` 目錄結構上顯性分開（`crud.py` vs `digital_usage.py`），使分界可自我文件化。未來若再有端點需要同等 SQL 表達力（如 P2 的批次查詢聚合），沿用同一判準加入直連側，不另立新規。
- **[D5] App 端後端認證列為 P1 前置工作（原為未列出的隱藏依賴）**（v0.22）：釐清一項跨模組的共同前提——**目前 App 的登入是純前端的**（見 4.4.2 [D2] v0.22 更正），後端沒有任何可據以判定「這個請求是誰發的」的機制。此缺口不只影響 Eco-Agent 綁定：
  - **四大模組全部撞上同一件事**。4.3 電梯 payload 含 `employee_id`、4.2 廢棄物開 session 帶 `employee_id`、4.1 差旅記錄綁 `employee_id`——這些欄位目前若由 client 於 body 指定，等同任何人都能把碳排寫到任何人名下，[D2] 為 Eco-Agent 堵住的後門會從其餘三個模組原樣打開。故**「`employee_id` 由憑證解出而非 client 指定」應是全系統統一原則，非 Eco-Agent 專屬**。
  - **與 Shared Savings 的關係**：本專案的個人歸戶帳直接連動 EXP／碳幣與利潤分享（1.7、4.3 [D4] 之「激勵帳」），歸戶可偽造即誘因機制失效，性質與碳排數字精度無關、不可用「專題規模小」折抵。
  - **P1 最小落地範圍**：`EMPLOYEE` 補 `password_hash`（bcrypt／argon2）、`POST /api/auth/login` 驗證後簽發 JWT（payload 帶 `employee_id`）、FastAPI 一支 `get_current_employee` dependency 供所有需歸戶的端點注入、App 端 `DemoAuthStorage` 改存真 token。~~Refresh 機制~~、密碼重設、帳號鎖定等列 P3。**（v0.24 更新）**：此處「簽發 JWT」原設想單枚，v0.24 定案 App 端**比照 Eco-Agent 採雙 token**，故 `login` 改為一次簽發 App Access ＋ App Refresh、並新增換發端點，**Refresh 機制自 P3 提前至 P1**（其餘密碼重設、帳號鎖定仍列 P3）；詳見本條下方 v0.24 決議子段。
  - **臨時替代作法（若須先跑通綁定鏈）**：可先讓 App 送寫死的 dev token（如 `Bearer dev:<employee_uuid>`），由同一支 dependency 解析。**要點是介面形狀必須正確**——`employee_id` 仍只從 header 來、body 仍只有 `code`；日後換真 JWT 只需替換該 dependency 的實作，router 與 service 零改動。反之若圖方便讓 body 帶 `employee_id`，P1 測通的將是一個之後必須整段拆掉重接的假流程，且被跳過的恰好是唯一需要驗證的那一段。
  - **App 端憑證機制定案：比照 Eco-Agent 採雙 token（v0.24）**。此即回填第 7 節原「App token 儲存位置與效期策略／是否比照雙 token」待設計議題。
    - **決議**：App 端採**短效 Access Token ＋ 長效 Refresh Token**，運作方式與 4.4.2 Eco-Agent 雙 token 同構——Access 每個歸戶請求用、過期時 App 以 Refresh 向後端換發新 Access（使用者無感）；Refresh 也過期／被撤銷時才回登入頁重新驗帳密。效期見 4.4.4（App Access 1h／App Refresh 30 天、不輪換）。
    - **為何是雙 token 而非單枚 JWT**：直接由「App 冷啟動時自動判斷是否已登入」需求推得（該需求見本文件 8.1 §App1.0，原出《App 系統功能》§1.0）。該需求要的是「打開即進、長期免登入」的**體驗**；單枚短效 JWT 為控風險把 `exp` 設短，冷啟動幾乎必然已過期、只能頻繁重登，與需求對撞；若反過來把單枚 JWT 的 `exp` 拉長（如 30 天）以免登入，則因 JWT 先天無法即時撤銷、又無 Eco-Agent「每次上傳夾帶撤銷」那條回應通道兜底，等於把唯一風險上界放到最大，落在誘因機制（EXP／碳幣／Shared Savings）最不能被冒用的那條線上。雙 token 把「證明身份」（短 Access、風險窗口小）與「保持登入」（長 Refresh、給體驗）拆開，同時取得短風險窗口、長期免登入、可撤銷三者，正是單枚 JWT 無法兼得者。
    - **冷啟動流程（對應 §1.0）**：App 冷啟動 → 自 `flutter_secure_storage` 讀 Refresh Token → 有效且未撤銷則背景靜默換發新 Access、直接進主畫面（員工無感，即「自動判斷已登入」）；讀不到／已過期／已撤銷則導向登入頁。冷啟動前 App 多半已關閉甚久、舊 Access 幾必過期，故直接走「以 Refresh 換 Access」最乾脆，不需先解舊 Access 的 `exp`。須處理「換發時無網路」——先讓使用者進 App 看快取、待有網路再補換，不因無網卡在登入頁。
    - **過期判定權在後端，非 App 自解 `exp`**：與全 [D5] 立論一致——App 是不可信一方，且無簽章密鑰無法真正驗簽。故「token 還能不能用」一律由後端 `get_current_employee` 每請求驗簽＋驗 `exp`、過期回 `401` 為準；App 讀 `exp` 僅作 UX 預判（提前引導重登），實際續期／重登以**收到後端 `401`** 觸發。
    - **儲存位置**：Refresh Token 為長效敏感憑證，**存 `flutter_secure_storage`（Android Keystore／iOS Keychain），不得放 `SharedPreferences`**（明文、可被他 App／root 裝置讀取），與 4.4.2 Eco-Agent「Refresh Token 存系統金鑰庫、不寫純文字檔」同一原則。現況 `DemoAuthStorage` 存布林值須改為存真 token 於 secure storage。
    - **撤銷**：後端存 `refresh_token_hash`，員工離職／裝置遺失時標記 `revoked`；手上 Access 至多撐到自身過期（≤1h，窗口可接受，論述同 4.4.2 Eco-Agent 撤銷延遲），Access 一過期即無法再以已撤銷的 Refresh 換發，換發端點回 `401/403`、App 被導回登入。
    - **效期取值脫鉤「月結算」**：App Refresh 訂 30 天係取「長期免登入體驗」與「風險窗口」之折衷，**非**為對齊每月結算功能——資料聚合排程作用於已落庫資料、與身份憑證效期無關，兩者同以「月」為單位僅屬巧合，不構成設計關聯。
    - **不啟用 Refresh 輪換**：比照 Eco-Agent，Refresh 30 天固定、到期重新登入；輪換之複雜度與離線誤判風險於本專案規模 > 收益，列 P3 備選。
    - **P1 落地影響**：`POST /api/auth/login` 驗帳密後改**一次簽發 App Access ＋ App Refresh**（原 [D5] 僅簽單枚 JWT 之敘述以此為準）；新增 App 端換發端點（比照 Eco-Agent `POST /api/agent/token/refresh` 之邏輯，比對 `refresh_token_hash` 且 active 才發新 Access）；`EMPLOYEE`／`DEVICE_BINDING` 之外，App Refresh 的 hash 儲存位置於 P1 實作時定（可比照 `DEVICE_BINDING.refresh_token_hash` 之作法）。臨時 dev token 作法（上一點）仍適用於「先跑通介面形狀」階段，換上真雙 token 時 router／service 零改動。

### 5.2 SD-IoT Controller：FastAPI 內建管理模組（自建輕量版）

> 服務對象：四大模組全部的註冊、配置、策略與監控（橫切層）。

#### 規格（現行定案）

**定位總結（一句話）**

> SD-IoT Controller ＝ FastAPI 內的管理模組（裝置註冊／綁定撤銷 ＋ 配置與策略下發 ＋ 係數庫 ＋ 健康監控），透過 MQTT retained topic（樹莓派）與 HTTPS 回應夾帶（Eco-Agent／App）兩條通道對數據面施加控制；數據面 ＝ 四顆 SVS 的資料上行、批次寫入與碳排運算引擎。

- Controller 實作為 FastAPI 後端內的一個邏輯模組（例如 `controller/` package），**不另建獨立服務**：控制面與數據面的「分離」是**邏輯分離**（模組邊界、職責劃分），而非部署分離（獨立行程／主機）。

**控制面／數據面職責劃分**

| 層面 | 職責 | 在 Eco-Sensing 中的對應 |
|------|------|------------------------|
| 數據面（Data Plane） | 實際資料流與運算 | 四顆 SVS 的資料上行（廢棄物走 MQTT topic，其餘走 HTTPS 資料端點，見 4.4 [D13]）、MQTT consumer 批次寫入（廢棄物）與 HTTPS 批次冪等 upsert（Eco-Agent）、碳排運算引擎、廢棄物 session 配對歸戶 |
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
| Eco-Agent | `thresholdCount`、`idleThreshold`、`maxAge`、`checkInterval`、`computerUsageRecordInterval`、`driveQuotaInterval`、`uploadBatchMax`、`printerPollInterval`（已定案值見 4.4.4） |
| MQTT consumer（廢棄物軌） | batch flush 間隔、批量上限、QoS 等級 |

**配置下發通道（依裝置性質分流，呼應混合協定架構）**

- **樹莓派**：MQTT **retained message** 發佈至 `config/bin/{id}` 類 topic——裝置一連線即取得最新配置，Controller 改參數即時推送；Mosquitto 原生支援，實作成本極低，為最具 SDN 特徵的控制通道。
- **Eco-Agent／App**：走 HTTPS——開機拉取一次，之後每次上傳時後端回應**夾帶配置版本號**，版本不符再拉取；複用 4.4.2 既有的「撤銷狀態夾帶於上傳回應」機制，一石二鳥。**（v0.20 註）**：Eco-Agent 上傳全改 HTTPS 後（4.4 [D13]），此通道與資料上行合流為同一條連線，無須為配置另闢通道；同一條回應一次承載「已落地確認」「撤銷狀態」「配置版本號」三件事。

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
| 第三步 | 建立伺服器 | Supabase（PostgreSQL）依 ERD 建表、FastAPI 服務與碳排運算引擎、寫入雙軌（廢棄物 MQTT consumer 批次寫入／Eco-Agent HTTPS 批次冪等 upsert）（依 5.1 分階段 P1→P2 推進） | 🟡 進行中 |
| 第四步 | 串接 API | 同步後端與前端數據；串接大語言模型 API | ⬜ 未開始 |
| 第五步 | 測試與實驗 | 系統穩定性、防呆機制測試；導入學校/中小企業實驗環境並記錄成果 | ⬜ 未開始 |

> 狀態欄位請隨進度更新：⬜ 未開始 / 🟡 進行中 / ✅ 完成

---

## 7. 待釐清議題（Open Questions）

> 標籤格式：`[歸屬章節][狀態]`。狀態：待討論／待設計／待實測／待安排／已決議。

- [5.1][已決議] ~~後端框架與資料庫選型尚未定案~~ → **FastAPI + Supabase（PostgreSQL），詳見 5.1**。
- [5.1][待實測] 後端批次寫入參數（flush 間隔、批量上限）與排行榜快取 TTL，待實測調整；Redis（P3）的導入門檻（部署規模）待定。
- [5.1][已決議] ~~既有後端採 PostgREST 與 5.1「一律走 connection pooler」之分歧~~ → **採兩層並存：一般 CRUD 維持 PostgREST 泛用層，`digital-usage/batch` 單條改走 asyncpg 直連 pooler（6543）**，判準為「是否需要條件式 upsert／交易控制」，詳見 5.1 [D4]。
- [5.1][待實作] `schema.sql` 補建 `uq_digital_usage_device`／`uq_digital_usage_printer`／`uq_digital_usage_printer_manual`／`uq_digital_usage_account` **四個** partial unique index（**目前尚未建立**，列 P1 優先項；v26 [D16] 由三增為四、新增手動印表機路徑,且四者皆帶 `sensing_mode` 述詞;鍵粒度見 4.4 [D14]、[D16]）；資料庫現無任何資料，schema 調整無須遷移成本。
- [4.4/5.1][已決議] ~~`id_token` 於 ERD 中同時出現於 `EMPLOYEE` 與 `DEVICE_BINDING`，語意易混淆，需釐清 `EMPLOYEE.id_token` 是否冗餘~~ → **`EMPLOYEE.id_token` 確認冗餘、刪除**。4.4.3 事件 ID 與 4.4.2 綁定所用者一律為 **`DEVICE_BINDING.id_token`（裝置粒度，per-device 一枚）**，[D14] 之鍵粒度論證即依賴此點；`EMPLOYEE.id_token` 無實際用途，刪除以消除同名混淆。ERD（`eco_sensing_erd.mmd`）已同步移除該欄。
- [5.1][待實作] Eco-Agent 專用端點於既有後端尚不存在（現況僅有各表泛用 CRUD）：`agent/binding-code` 索取與核銷、雙 token 簽發與 refresh、`agent/digital-usage/batch`（v26 [D16] 更名）、Bearer 認證與撤銷回應（`401/403`）、碳排運算引擎（`factor_id`／`co2e_kg`）。
- [共用][待討論] 碳排係數資料庫的更新機制與來源權威性如何維護？
- [5.2][已決議] ~~SD-IoT Controller 的具體實作方式（自建 vs 既有框架）？~~ → **自建輕量版，實作為 FastAPI 內管理模組（控制面／數據面為邏輯分離），配置經 MQTT retained message 與 HTTPS 回應夾帶下發，詳見 5.2**。
- [5.2][待設計] `sensor_config`（policy）表的欄位設計與配置版本號機制（全域版本 vs 分裝置版本），待 P2 實作時訂定。
- [專案][待安排] 實驗環境的取得（學校場域或合作企業）與受測員工招募。
- [5.1/共用][已決議] ~~App 登入身份可否供後端判定請求來源~~ → **不可，目前為純前端登入**；後端認證（`password_hash`＋`POST /api/auth/login` 簽發 JWT＋`get_current_employee` dependency）列為 **P1 前置工作**，且確立全系統原則：**`employee_id` 一律由憑證解出，不得出現在 request body**。詳見 5.1 [D5]、4.4.2 [D2]（v0.22 更正）。
- [共用][已決議] ~~App 端 token 的儲存位置與效期策略（`SharedPreferences` vs `flutter_secure_storage`；是否比照 Eco-Agent 採雙 token）~~ → **比照 Eco-Agent 採雙 token**：App Access 1h ＋ App Refresh 30 天（不輪換）；Refresh 存 `flutter_secure_storage`、後端存 hash 可撤銷；冷啟動以 Refresh 靜默續 Access 達成《App 系統功能》§1.0「自動判斷已登入」；過期判定權在後端（`401` 觸發），App 讀 `exp` 僅作 UX 預判。詳見 5.1 [D5]（v0.24）、4.4.4 憑證效期表。
- [共用][已決議] ~~App Refresh 的 `refresh_token_hash` 儲存位置（塞 `EMPLOYEE` vs 獨立表；v0.24 [D5] 遺留「於 P1 實作時定」）~~ → **開獨立表 `APP_SESSION`（一列一枚 Refresh）**，不塞 `EMPLOYEE`。理由：Refresh 是有生命週期、可撤銷、單員工可多枚（多裝置）的實體，非員工屬性；獨立表天然支援多裝置與個別撤銷，與 `DEVICE_BINDING` 對 Eco-Agent 所做者同構。欄位含 `employee_id` FK、`refresh_token_hash`、`status`、`created_at`／`expires_at`／`last_used_at`／`revoked_at`。ERD（`eco_sensing_erd.mmd`）已同步新增 `APP_SESSION` 實體與 `EMPLOYEE ||--o{ APP_SESSION` 關係。同名欄位 `refresh_token_hash`（`DEVICE_BINDING` 與 `APP_SESSION` 各一枚）不需處理——不同表、SQL 恆帶表名限定、語意一致。
- [共用][待討論] 個資與資安：NFC 樓層追蹤、Desktop Agent 監測的員工知情同意與合規邊界。
- [共用][待設計] GDT 與 Shared Savings 的量化模型與回饋機制設計細節。
- [4.3][已決議] ~~電梯多人共乘的碳排分攤方式~~ → **採方案 B（固定單人分攤值）**：每次搭乘記單人標準碳排 `|floor_delta| × EF_dir`，不感測人數、不拆單趟總耗電；上/下行分兩組係數內建方向誘因。刻意放棄「個人歸戶總和 = 電表總量」的守恆性，激勵帳與 Scope 1/2 盤查帳分離（後者用電表總量）。詳見 4.3 [D3]、[D4]。
- [4.3][已決議] ~~NFC 樓層感測端形式（被動 tag vs 主動 ESP32）~~ → **採被動 NFC tag（各樓層電梯廳）**：樓層資訊來自 tag 貼附位置而非轎廂即時位置，故不需串接電梯控制系統、不供電、不改裝、可完整移除；主動式 ESP32 因「轎廂樓層資訊取得」門檻極高（安規／保固／取電施工，或氣壓推算誤差過大）降級為未來增強路徑。詳見 4.3 [D5]。
- [4.3][待實測] 電梯單人每層係數 `elevator_per_floor_up`／`elevator_per_floor_down` 的校準值，待向電梯公司索取（額定功率、對重平衡比例、單趟實測耗電或 ISO 25745 量測報告）後定值。
- [4.3][待安排] 實驗場域電梯廳張貼被動 NFC tag 之許可與窗口確認（權責可能屬建物管理方／管委會而非電梯公司）；tag 數量與張貼位置（每層電梯廳一枚 vs 每電梯口一枚、是否設冗餘）待場域勘查後定案。
- [4.3][待設計] 未掃出場之孤兒記錄逾時門檻與結算策略（不計歸戶 vs 給予保守最小值），沿用 4.2 session 逾時思路，參數屬 5.2 可下發配置。
- [4.2][待實測] 廢棄物模組 session 逾時秒數（孤兒事件自動結算門檻）與互斥鎖（方案 C）是否依部署規模啟用，待實測決定。
- [4.4][已決議] ~~Eco-Agent 綁定碼短效時長、Access/Refresh Token 期限與輪換策略~~ → **`bindingCodeTTL` 5 分鐘、Access Token 1 小時、Refresh Token 90 天（到期重綁、不輪換），詳見 4.4.4**。綁定碼儲存於 `BINDING_CODE` 表（詳見 4.4.2、ERD）。
- [4.4][已決議] ~~Eco-Agent 撤銷狀態的回傳時機與離線撤銷延遲容忍度~~ → **採每次上傳夾帶（不另做心跳）；離線撤銷延遲上界 ≈ `maxAge`（24h），延遲期間裝置本就送不出資料，可接受，詳見 4.4.2**。
- [4.4][已決議] ~~Eco-Agent 上傳觸發參數（computerUsageRecordInterval、driveQuotaInterval、累積量門檻、最長滯留時數）~~ → **已定案值詳見 4.4.4**（本地佇列儲存選型 SQLite vs append-only 檔仍待實測；唯一事件 ID 組成鍵見 [D12]，後端冪等去重策略已於 v0.20 與 5.1 對齊完畢，見下方 [4.4/5.1][已決議] 項）。
- [4.4][已決議] ~~`DIGITAL_USAGE` 如何區分三條感測路徑、`path_type` 由誰決定~~ → **採方案 A「一路徑一列」**：新增 `path_type`（`computer`／`printer`／`drive`），唯一鍵 =（`employee_id`, `usage_date`, `path_type`）（**[D14] 已將鍵粒度依路徑分三組：`computer` 加 `device_id`、`printer` 加 `printer_serial`、`drive` 維持三段**）；`path_type` **由 Agent 明送、不由後端從欄位樣態推斷**（零值與 NULL 難分辨、推斷規則脆化、與冪等鍵不自洽、Agent 本就知道）。`factor_id`／`co2e_kg` 屬後端寫入、不在 Agent payload。詳見 4.4 決策記錄 [D12]；ERD 已同步補 `path_type`、`drive_trash_gb`。
- [4.4/5.1][已決議] ~~後端冪等去重策略與 5.1 批次寫入對齊（原五項待釐清）~~ → **五項於 v0.20 全數定案**，4.4.3 與 5.1 的交接介面已明訂。逐項結果：
  - **(1) 去重層級** → **兩層並用**：應用層收批後依鍵摺疊，DB 層以 unique index 作最後防線（P3 多實例後尤為必要）。規格見 5.1「`DIGITAL_USAGE` 冪等去重」。
  - **(2) 單一批次內同鍵衝突** → **後端收批後先在記憶體依鍵摺疊（同鍵只留 `collected_at` 最新一筆）再組 upsert 語句**，避開 PostgreSQL `command cannot affect row a second time`。列為必要步驟而非最佳化。
  - **(3) 同鍵衝突勝出規則** → **補 `collected_at`（Agent 採集時間戳，UTC）於 payload 與 `DIGITAL_USAGE`**，upsert 加條件 `WHERE EXCLUDED.collected_at > digital_usage.collected_at`，重送舊封包不覆蓋較新值。詳見 4.4 [D14]。
  - **(4) 「200 才清佇列」在 MQTT 上不成立** → **採「上傳全改 HTTPS」**：Eco-Agent 三條路徑一律走 HTTPS，`200` 由後端於 commit 後發出，「收到 200」與「已落地」等價，破口消失；同時一併解決 4.4.2 撤銷與 5.2 配置版本號所需的回程通道（原需另闢）。否決 (a)「MQTT QoS 1 延後 ack」（只補資料遺失、不解回程通道，另引入 inflight 窗口與批次門檻互鎖陷阱）與 (c)「降級為盡力而為」。詳見 4.4 [D13]。
  - **(5) 多裝置撞鍵** → **唯一鍵粒度依路徑分三組**（v0.23 修正）：電腦加 `device_id`（per-device）、印表機加 `printer_serial`（per-printer，**非** `device_id`，否則桌機＋筆電同指一台印表機會重複計算）、雲端維持三段（per-account）；以三個 **partial unique index** 分別表達，避開「合併鍵下 NULL 互不相等導致該路徑去重靜默失效」的 PostgreSQL NULL 陷阱。`employee_id` 保留於列上作歸戶快照。否決「一員工限綁一裝置」（與 BYOD 衝突、後果更嚴重、且同樣須動 ERD）。詳見 4.4 [D14]。
  - **連帶改動**：ERD `DIGITAL_USAGE` 新增 `device_id` FK 與 `collected_at`、新增關係 `DEVICE ||--o{ DIGITAL_USAGE : reports`、`DEVICE` 新增 `display_name`；第 2／3 節協定表與分流判準、4.4 路徑表與 payload 段、4.4.2、4.4.3、5.1、5.2、技術堆疊皆已同步。
  - **留待 P2 實作時處理（非設計缺口）**：`uploadBatchMax`（720）與單一交易大小的實測調參；員工層聚合查詢中 `pc_avg_cpu_util`／`cpu_model` 之不可加總處理（加權平均 vs 僅裝置層檢視）；多裝置下員工單日時數可超過 24 小時的介面呈現方式（建議員工層只顯示 `co2e_kg`）。
- [4.4][已決議] ~~雲端儲存（路徑 C）能耗模型的儲存量取值~~ → **取 `usageInDrive`**（否決 `limit`、`usage`），詳見 4.4 決策記錄 [D8]。
  - [4.4][已決議] ~~`usageInDriveTrash` 激勵任務是否落地~~ → **確定納入**：Agent 上傳 `usageInDriveTrash`、入庫 `DIGITAL_USAGE.drive_trash_gb`（ERD 已同步）、納入 i 減碳任務清單；獎勵額度依 1.7 遊戲化機制設計。詳見 4.4 [D8]。
  - [4.4][待確認] 實驗場域員工帳號 `usageInDrive` 量級是否合於一般日常帳號，異常高者疑為機構共享／服務帳號、取樣須排除或標註。
- [4.4][討論中] 雲端能耗公式的 PUE 與「每 GB 儲存功耗」係數取得 → **PUE 採 Google fleet-wide 平均值**（個別機房 PUE 原理上取不到：Drive API 不含機房位置、資料跨機房多副本動態遷移；即使知道機房 Google 亦不逐座公布 PUE）；**每 GB 儲存能耗強度以「硬碟規格反推」為主估法**（裸碟 W/GB × 基礎設施＋副本放大因子，未含 PUE），詳見 4.4 決策記錄 [D9]。
  - [4.4][待查證] 「每 GB 儲存功耗」係數正式取值（硬碟型號 datasheet vs 引用文獻）與副本放大因子；fleet-wide PUE 當年度數字（Google 最新環境報告）；電力排放係數（台電當年度公告）——皆待查證可引用來源後定值。
- [4.4][方向盤點] 雲端路徑（路徑 C）應用場景 → 四類：(一)行為誘因（數位斷捨離：大型/冷/重複檔清理，涉讀檔案清單)、(二)趨勢與異常（`usageInDrive` 時間序列，成長趨勢/暴增偵測，零額外隱私成本)、(三)機構層級洞察（總碳排/成長率、儲存效率 KPI、ESG 報告素材，此路徑對企業最實質價值)、(四)教育與意識（「雲端＝一直開著的硬碟」具象化、補齊完整數位碳足跡，零額外隱私成本)。天花板：雲端碳排量級偏小（單人 ~0.058 kWh/年），價值在「可見/可執行/可教育」非減碳數字。詳見 4.4 決策記錄 [D10]。
  - [4.4][待決策] 隱私分界：(二)(四) 可放心納入；(一)(三) 中涉「讀檔案清單/檔名/內容特徵」者需放大 OAuth scope、性質不同，列為「可行但待隱私/scope 決策」，不默默實作（比照 [D5]/[D6]）。
- [4.4/5.2][待釐清] Eco-Agent 開機是否強制拉取配置一次，以及拉取失敗的容錯處理（沿用上一版配置 vs 阻擋上傳 vs 重試），待設計。
- [4.4][已決議] ~~印表機（路徑 B）碳排的歸戶前提~~ → **分軌並依基礎設施前提排優先序**：新增備選「手動上傳用紙量」（App 內列印前後輸入上傳,使用者主動感測、須搭誘因）；**優先開發**「個人專屬印表機感測（Eco-Agent SNMP 輪詢歸戶）」與「手動上傳用紙量（Eco-Sensing App）」；共用機的 **Print Server Log** 與 **Pull Printing API** 技術上可行,於未來報告列為「可行但待實作、測試」,不納入現階段優先開發。詳見 4.4 決策記錄 [D5]、[D6]。
  - 承上,實驗場域印表機屬個人或共用、以及共用機是否具備集中列印伺服器 / pull printing 系統,待場域確定後回填,以決定兩條共用機路徑的實作可行性。
- [4.4][已決議] ~~桌機與筆電同指一台專屬印表機時頁數重複加總~~ → **路徑 B 歸鍵改用 `printer_serial`（per-printer）而非 `device_id`**，同一台印表機無論被幾台裝置觀測都只有一列；並**改送 SNMP 壽命累計讀數、差分移至後端**（順帶修掉 baseline 只存於 Agent 本機、重裝即遺失的獨立缺陷）。詳見 4.4 [D14]（缺口二表格）、[D15]。
  - [4.4][待實測] 實驗場域印表機之序號 OID 支援度：`prtGeneralSerialNumber`／`entPhysicalSerialNum`／`sysName` 三者是否至少一個回傳非空值。低階機種常三者皆空，屆時該裝置回退以 `device_id` 歸鍵並標記「印表機身份不明」，重複計算風險於該場域仍存在。
  - [4.4/5.1][待設計] 路徑 B 跨日補記的處理：整天無人開機則該日無讀數，下次讀數的差值橫跨數日、會被記在單一天。按日均攤 vs 註記為「跨日補記」待定，屬 P2 實作細節但須明訂，否則儀表板出現無法解釋的尖峰。
- [4.4/5.1][已決議] ~~手動上傳用紙量的落庫端點、來源識別、去重鍵與 Agent 自動路徑的關係（《驗證機制端點關係表》§3.1 之 `digital-usages` vs `digital-usage/batch` 定位落差）~~ → **v26 [D16] 定案**：(1) `POST /api/digital-usages`（PostgREST、App 員工 `Bearer`）正式保留作 App 手動補登管道，與 Agent 自動路徑並存為 `DIGITAL_USAGE` 兩來源；(2) Agent 寫入端點更名 `POST /api/agent/digital-usage/batch`、收進 `/api/agent/*` 命名空間以消混淆；(3) 新增獨立欄位 `sensing_mode`（`auto`／`manual`）區分自動／手動（正交於 `path_type`、保留擴充性，不於 `path_type` 加值）；(4) App 端彙總、後端一天一列 upsert，唯一鍵（`employee_id`, `usage_date`, `path_type='printer'`, `sensing_mode='manual'`）；(5) 兩管道互斥（專屬機走自動、共用機走手動），無雙重計算。詳見 4.4 [D16]、5.1 冪等去重（partial index 由三增為四）。ERD `DIGITAL_USAGE` 新增 `sensing_mode`。
  - [4.4/5.1][待釐清] **手動上傳落庫後的更正語意**：App 本地編輯僅限送出前；已落庫者若需更正,走既有 `PATCH /api/digital-usages/{id}`(帶 App `Bearer`)——惟「更新語意」尚未定：(a) App 重算當日總量後**重送覆蓋**該列,或 (b) 直接 **PATCH 改庫值**。兩者行為不同（前者 App 端重算、後者後端直接改),須擇一明訂,否則同一列的更新來源不一致。屬 P2 實作細節,不阻塞 [D16] 主決策。
  - [4.4][待確認] 手動上傳的**觸發機制**（App 於何時把當日彙總上傳:每筆記錄即送/定時/收工手動送出）與**誘因設計**（比照 i 減碳任務以 EXP／碳幣激勵,鼓勵員工主動記錄),屬 App 端（§8）與遊戲化（1.7）待細化項。

---

## 8. Eco-Sensing App（前端應用層）

> 本章集中呈現 Eco-Sensing App 自身的功能快照、實作狀態與 App 專屬技術決議，使其獨立於 §4 四大功能模組（感測與核算領域）與 §5 橫切層。四大模組是「感測什麼、如何核算」，本章是「員工／企業如何在介面上使用與被驗證」。
>
> 內容承接自原《Eco-Sensing App 系統功能》文件（`Eco-Sensing_App_系統功能_v04.md`，**自本版起併入本章、原檔封存不再維護**）：其「員工端／企業端功能」整理為 8.1／8.2 規格與 8.3 實作狀態表，其「版本紀錄」併入本章 8.5 決策記錄。App 專屬的身份驗證決議（雙 token）由 5.1 [D5] 收斂於此呈現，[D5] 仍為跨模組原則（`employee_id` 由憑證解出）之出處。
>
> 依賴：後端認證與 `get_current_employee`（5.1 [D5]）｜Eco-Agent 綁定流程與雙 token（4.4.2）｜QR 統一辨識（4.5）｜電梯 NFC deep link（4.3、`app_links`）｜遊戲化與 Shared Savings（1.7）
>
> 技術現況：員工端為 **Flutter + Riverpod**（feature-first 架構）；企業端為獨立 **Vue 3 Web 後台**（Composition API、Vite、純 SVG／CSS 自繪圖表）。兩端目前資料皆為前端模擬（mock），尚未串接後端 API、OCR 引擎與碳排計算模型。

### 8.1 員工端功能（Flutter App）規格

- **登入與身份切換（8.3 對應 §App1.0）**：登入頁提供員工端／企業端身份選擇；郵箱、密碼欄位（可切換顯示）。**目前不驗證帳密**（選「員工端」即進入），後端認證列為 P1 前置工作（見 5.1 [D5]、本章 8.4）。登入狀態保留於本機，**App 冷啟動時自動判斷是否已登入**（此需求為 8.4 雙 token 決議的關鍵依據）。儀表板與「個人 > 設定」皆提供登出。
- **主框架**：`EmployeeHomePage` 採五分頁底部導覽——儀表板、i 減碳、掃描、排行榜、個人。
- **碳排儀表板（首頁）**：Hero 指標（較上月、月目標）、使用者卡片（顯示名稱／等級／碳幣）、經驗值進度條（每級 500 EXP）、每日減碳建議輪播、月度碳排組成（差旅／廢棄物圓餅圖與週趨勢）、近期碳排紀錄。規劃中：能源活動統計（電梯、數位碳足跡）整合呈現與數據分享。
- **i 減碳**：月度碳排獎勵彈窗、減碳成果統計與月目標進度、任務類別篩選（交通／廢棄物／能源／飲食／辦公）、減碳任務列表（含 EXP／碳幣／預估減碳量）。任務資料涵蓋商務差旅、大眾運輸、自行車通勤、共乘、廢棄物、電梯、餐飲、無紙化。
- **掃描**：三軌單據掃描（OCR 流程，UI 完整、辨識為模擬）——票據類型／日期／起訖／金額／碳足跡皆可編輯，確認後走「AI 辨識中→完成獎勵」；掃描垃圾桶（智慧回收 demo，QR→投入→AI 計算→結果動畫）；**手動上傳用紙量**（印表機路徑備選，對應 4.4 [D6]，使用者主動感測、須搭誘因；**落地定案見 4.4 [D16]**——當日可多筆記錄、每筆送出前於 App 本地編輯，App 彙總後走 `POST /api/digital-usages`（`sensing_mode='manual'`）上傳，後端一天一列）。
- **排行榜**：部門排行（各部門對應不同指標與單位——業務差旅減碳、研發用電節省、人資用紙減少、行銷廢棄物減量、物流車輛碳減）、個人排名卡、頒獎台與排名列表、名次獎勵說明。
- **個人資料**：資料／成就／設定三分頁——基本資料編輯、等級與獎勵、修改密碼（前端流程完成待串接）；成就解鎖與展示櫃；組織資訊、外觀語言、通知、帳戶（分享碳排檔案／下載數據／登出／刪除帳號）、關於。另有**我的 QR Code 彈窗**（`QRCodePopup`，產生員工識別 QR 供掃碼辨識身份）。
- **遊戲化與獎勵機制（跨頁面，規劃中）**：EXP／等級（每級 500 EXP）、碳幣、成就與展示櫃、排行榜名次獎勵、減碳任務、月度結算彈窗。最終規則（經驗值曲線、碳幣兌換、與 Shared Savings／GDT 連動）尚待確認（見 1.7）。
- **IoT 感測整合（背景偵測）**：電梯搭乘追蹤（NFC，以 `app_links` 接收深層連結、支援冷啟動與背景喚醒、未登入先暫存待辦樓層、兩次掃描計算樓層差與獎勵）；智慧垃圾桶 QR（整合於掃描頁）。

### 8.2 企業端功能（Vue Web 後台）規格

- **登入與後台框架**：管理員登入頁（**目前不驗證帳密**，填寫即進 `/admin/dashboard`）；左側固定側欄 ＋ 右側內容區（`AdminLayout`），七項導覽對應七條路由。
- **總覽儀表板**：資料來源橫幅（標示感測器即時同步）、KPI 卡片（年度總碳排、差旅、廢棄物、感測在線率）、年度趨勢 SVG 圖、排放類型雷達圖、近期活動、季度比較、部門排行、達成率儀表盤。
- **碳排放管理**：範疇一／二／三卡片、月度趨勢柱狀圖、範疇占比圓餅圖（CSS conic-gradient）、主要排放來源清單、範疇明細表。
- **感測器管理**：設備健康度、摘要卡（總數／在線／異常／今日筆數）、狀態分布環圈圖、即時監測摘要、感測設備清單表（涵蓋智慧垃圾桶、能源監測、環境感測、NFC 電梯標籤）。
- **部門管理**：摘要卡、部門碳排排行橫條圖、部門排放明細表。
- **報告與排行**：**一鍵生成 ESG 報告**（`EsgReportDocument`，支援新分頁預覽／下載 HTML／列印存 PDF；含執行摘要、範疇一～三、部門排行、ESG 目標、感測狀態、管理層結論）；排行榜三檢視（部門碳排／排放別／員工節能）、統計期間篩選、SVG 長條圖與排行表。
- **ESG 目標**：摘要卡、永續目標進度條、里程碑時間軸（2025 盤查→2030 減 30%→2040 綠電→2050 淨零）。
- **系統設定**：組織資訊、ESG 參數（基準年／減碳目標／淨零年／範疇三開關）、IoT 感測設定（同步頻率／CO₂ 門檻／保留天數／異常通知）、報告匯出設定、介面偏好。表單目前僅前端綁定、尚未持久化。

### 8.3 實作狀態表

> 圖例：✅ 已實作（含 UI 與互動）／🟡 已實作 UI 但用模擬（假）資料、尚未串接後端或真實演算法／⚪ 規劃中／待開發。狀態承自原《App 系統功能》文件，隨開發演進更新。

**員工端（Flutter）**

| # | 功能 | 狀態 | 備註 |
|---|------|------|------|
| App1.0 | 登入與身份切換 | ✅🟡 | 登入 UI 與狀態保留完成；**不驗帳密、後端不知情**；冷啟動自動判斷已登入 ✅。後端認證＋雙 token 為 P1（8.4） |
| App1.1 | 底部導覽列（主框架） | ✅ | 五分頁 |
| App1.2 | 碳排儀表板（首頁） | ✅🟡 | 指標與圖表多為固定／假資料 |
| App1.3 | i 減碳 | ✅🟡 | 任務與獎勵為假資料 |
| App1.4 | 掃描（單據／垃圾桶／手動用紙量） | ✅🟡⚪ | OCR、垃圾桶辨識為模擬；手動上傳用紙量 ⚪ 待開發 |
| App1.5 | 排行榜 | ✅🟡 | 依部門指標排名，假資料；排名分享 ⚪ |
| App1.6 | 個人資料（資料／成就／設定） | ✅🟡 | 修改密碼待串接；QR Code 產生 🟡；檔案分享 ⚪ |
| App1.7 | 遊戲化與獎勵機制 | ⚪ | 現行雛形，最終規則（含與 Shared Savings／GDT 連動）待定 |
| App1.8 | IoT 感測整合（電梯 NFC／垃圾桶 QR） | ✅🟡 | NFC deep link 流程完成；離開樓層與碳排係數 🟡 待補 |

**企業端（Vue Web 後台）**

| # | 功能 | 狀態 | 備註 |
|---|------|------|------|
| App2.0 | 登入與後台框架 | ✅🟡 | 不驗帳密即進後台 |
| App2.1 | 總覽儀表板 | ✅🟡 | 全假資料 |
| App2.2 | 碳排放管理 | ✅🟡 | 全假資料 |
| App2.3 | 感測器管理 | ✅🟡 | 全假資料 |
| App2.4 | 部門管理 | ✅🟡 | 全假資料 |
| App2.5 | 報告與排行（一鍵 ESG 報告） | ✅🟡 | 報告可生成／預覽／下載／列印；資料為 mock |
| App2.6 | ESG 目標 | ✅🟡 | 全假資料 |
| App2.7 | 系統設定 | ✅🟡 | 表單僅前端綁定、未持久化 |

> 全端共通現況：所有數據皆為前端模擬（mock），尚未串接後端 API 與真實感測資料流。串接真 API 為 Roadmap 第四步、後端 P1 起（見 §6、5.1）。

### 8.4 App 身份驗證：雙 token 機制（規格·現行定案）

> 出處與跨模組原則見 5.1 [D5]；本節為 App 端落地形狀的集中呈現。與 4.4.2 Eco-Agent 雙 token **同構**（差別僅在觸發情境：Agent 背景常駐、App 冷啟動）。

- **現況**：App 目前為**純前端登入**（不驗帳密、後端不知情，`DemoAuthStorage` 僅存登入狀態）。後端認證列為 **P1 前置工作**。
- **全系統原則（承 [D5]）**：`employee_id` **一律由憑證解出，不得由 client 於 request body 指定**；此為四大模組共同約束，非 App 或 Eco-Agent 專屬。
- **憑證機制：比照 Eco-Agent 採雙 token**：
  - **App Access Token（短效，1 小時）**：每個需歸戶的請求帶 `Authorization: Bearer`；不落庫，由 App Refresh Token 換發。
  - **App Refresh Token（長效，30 天，不輪換）**：存 `flutter_secure_storage`（Android Keystore／iOS Keychain，**不放 `SharedPreferences`**）；後端僅存 `refresh_token_hash`，供換發與撤銷；到期需重新登入。
- **冷啟動流程（對應 8.1 §App1.0「自動判斷已登入」需求）**：App 冷啟動 → 讀 secure storage 中的 Refresh Token → 有效且未撤銷則**背景靜默換發新 Access、直接進主畫面（員工無感）**；讀不到／過期／已撤銷 → 導向登入頁。換發時無網路則先進 App 看快取、待有網再補換，不卡在登入頁。
- **過期判定權在後端**：token 是否有效一律由後端 `get_current_employee` 每請求驗簽＋驗 `exp`、過期回 `401` 為準；App 讀 `exp` 僅作 UX 預判（提前引導），實際續期／重登以**收到 `401`** 觸發（App 為不可信一方、且無簽章密鑰，不能自行判定有效性）。
- **撤銷**：員工離職／裝置遺失時後端將該 Refresh 標記 `revoked`；手上 Access 至多撐到自身過期（≤1h，窗口可接受），之後無法再以已撤銷 Refresh 換發（換發端點回 `401/403`）、被導回登入。
- **效期取值脫鉤「月結算」**：App Refresh 訂 30 天係「長期免登入體驗」與「風險窗口」之折衷，**與每月結算功能無關**（資料聚合排程作用於已落庫資料、與身份憑證效期無關，同以月為單位僅屬巧合）。
- **參數**（與 4.4.4「憑證效期（後端簽發策略，不下發）」表同源，集中複列於此便於 App 開發者查閱）：

| 項目 | 值 | 儲存／備註 |
|------|-----|-----------|
| App Access Token exp | 1 小時 | 不落庫，Refresh 換發 |
| App Refresh Token exp | 30 天（不輪換） | `flutter_secure_storage`；後端存 hash 可撤銷；到期重登 |

- **員工過期後應做的事**：Refresh 未過期 → 多半無感（背景自動續 Access）；Refresh 亦過期／被撤銷 → 回登入頁重新輸帳密。過期若發生在操作中途，App 應暫存當前操作、重登後復原（避免丟失填寫內容）。已在 Eco-Agent 佇列中的感測資料不受 App token 過期影響（「至少一次送達」由 Agent 本機佇列保證，見 4.4.3）。

### 8.5 決策記錄（脈絡與依據）

> 本節前半承自原《Eco-Sensing App 系統功能》文件之版本紀錄（App 端演進脈絡）；後半為併入本 Context 文件後、與後端對齊所生的 App 專屬決策。

**App 系統功能文件版本紀錄（繼承）**

| 日期 | 版本 | 變更摘要 |
|------|------|----------|
| 2026-03-23 | App v0.1 | 初版，整理員工端與企業端系統功能架構 |
| 2026-06-19 | App v0.2 | 員工端功能依 Flutter App 實作（登入、儀表板、i 減碳、掃描、排行榜、個人、遊戲化、NFC/QR IoT 整合）重整並標註實作狀態；企業端維持初始規劃 |
| 2026-06-19 | App v0.3 | 企業端功能依 Vue Web 後台 Demo（`eco-sensing-web`）實際內容重寫（2.0–2.7，含一鍵 ESG 報告）；1.7 遊戲化改標規劃中（⚪），待與組員討論更新 |
| 2026-07-10 | App v0.4 | 1.4 掃描新增「手動上傳用紙量（印表機路徑備選）」，標記待開發（⚪）：使用者主動感測、須搭誘因，為共用機環境碳排歸戶補位；對應 context 文件 4.4 [D6] |
| 2026-08-13 | 併入本章 | 《App 系統功能 v0.4》併入本 Context 文件第 8 章（功能→8.1／8.2、實作狀態→8.3、版本紀錄→本表）；原檔封存，後續 App 功能演進於本章維護 |

**App 專屬決策（併入後）**

- **[A1] App 端憑證機制採雙 token（承 5.1 [D5]、v0.24 定案）**：決議 App 端比照 Eco-Agent 採短效 App Access（1h）＋長效 App Refresh（30 天、不輪換）。**主要依據為 8.1 §App1.0「App 冷啟動時自動判斷是否已登入」需求**——該需求要「打開即進、長期免登入」的體驗，單枚短效 JWT 冷啟動幾必過期而須頻繁重登、與需求對撞；若改拉長單枚 JWT 的 `exp`（如 30 天），則因 JWT 無法即時撤銷、又無 Eco-Agent「每次上傳夾帶撤銷」通道兜底，等於把唯一風險上界放到最大，且落在誘因機制（EXP／碳幣／Shared Savings）最不能被冒用處。雙 token 拆開「證明身份（短 Access、風險窗口小）」與「保持登入（長 Refresh、給體驗）」，同時取得短風險窗口、長期免登入、可撤銷三者。細節（冷啟動流程、後端判定權、儲存位置、撤銷、效期脫鉤月結算）見 8.4 與 5.1 [D5]。
- **[A2] 純前端登入為隱藏依賴，後端認證列 P1（承 [D5]）**：原以為 App「已有完整登入身份系統」屬事實誤述——登入頁不驗帳密、`DemoAuthStorage` 僅存布林狀態、後端不知情。故 Eco-Agent 綁定步驟「App 送已驗證身份」之前提尚不成立，須先補 App 端後端認證（`EMPLOYEE.password_hash`、`POST /api/auth/login` 簽發雙 token、`get_current_employee` dependency、App 端改存真 token 於 secure storage）。列 P1 前置工作。
- **[A3] `employee_id` 由憑證解出為全系統原則**：四大模組（差旅／廢棄物 session／電梯／Eco-Agent）payload 皆含 `employee_id`，若由 client 於 body 指定即等同任何人可把碳排寫到任何人名下；歸戶又直接連動 EXP／碳幣與 Shared Savings，可偽造即誘因失效，不可用「專題規模小」折抵。此原則對 App 各端點同樣適用（body 只帶業務參數、身份只從 header 憑證來）。

---

## 9. 版本紀錄

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
| 2026-07-19 | v0.14 | **電梯（4.3）多人共乘分攤定案**：採**方案 B（固定單人分攤值）**——每次搭乘記單人標準碳排 `\|floor_delta\| × EF_dir`（上/下行分兩組係數、內建方向誘因），不感測同梯人數、不拆單趟總耗電、不需量測載重，ESP32／NFC 端純送進出樓層。新增決策記錄 [D3]（方案 A/B/C 比較與選 B）、[D4]（**刻意放棄守恆性**之取捨聲明：激勵帳 vs Scope 1/2 盤查帳分離，後者用電表總量、不受影響；idle/standby 底噪不攤入個人帳）。更新 4.3 規格段（碳排歸戶模型改寫、NFC 端註記 ESP32）、依賴宣告（`EMISSION_FACTOR` 新增電梯單人每層係數）、現況快照電梯列；第 7 節新增 4.3 已決議與係數校準待實測項。ERD `ELEVATOR_TRIP` 新增 `direction`、`floor_delta` 欄位 |
| 2026-07-21 | v0.15 | **雲端儲存（路徑 C）能耗模型儲存量取值定案**：Eco-Agent 已實作路徑 C 感測，`storageQuota` 可取 `usage`／`usageInDrive`／`usageInDriveTrash`／`limit` 四值。能耗公式的「儲存量」**取 `usageInDrive`**——否決 `limit`（配額額度非實際佔用；且 Workspace pooled 模式下為機構共享總池、全員雷同無區辨力）與 `usage`（含 Gmail／Photos，超出 Drive SVS 範圍）；另**單獨拆出 `usageInDriveTrash`** 標為「可立即釋放的儲存能耗」，作 i 減碳激勵任務（清空垃圾桶即減碳、以 EXP／碳幣回饋），呼應行為誘因內建設計。新增決策記錄 [D8]。更新 4.4 路徑 C 表格列、感測模式段（新增儲存量取值條目）、MQTT payload（`drive_usage_gb` 註記取自 `usageInDrive`、新增 `drive_trash_gb`）、碳排換算段；現況快照 Eco-Agent 列補註。（待確認：(1) 實驗場域員工帳號 `usageInDrive` 量級是否合於一般日常帳號，異常高者疑為機構共享／服務帳號、取樣須排除或標註；(2) `usageInDriveTrash` 激勵任務是否落地——是否納入 i 減碳任務清單、是否入庫 `drive_trash_gb` 欄位並同步 ERD、獎勵額度，皆待確認，取 `usageInDrive` 之主決策不受影響。） |
| 2026-07-21 | v0.16 | **雲端能耗公式 PUE 與「每 GB 儲存功耗」係數取得（討論中）**：釐清 `usageInDrive × PUE` 實須拆為三段係數（每 GB 儲存能耗強度 × PUE × 電力係數）。**PUE 定案採 Google fleet-wide 平均值**（近年約 1.1 量級）——個別機房 PUE 原理上取不到（Drive API 不回傳機房位置、資料跨多機房多副本動態遷移、Google 不逐座公布 PUE 且 PUE 隨季節/負載波動），fleet-wide 值來源權威、貼合超大規模雲端。**「每 GB 儲存功耗」採「硬碟規格反推」為主估法**（例：18TB HDD ~7W → ~0.0034 kWh/GB/年裸碟，乘基礎設施＋副本放大因子 ×2～3 得 ~0.006–0.01 kWh/GB/年、未含 PUE），透明可引用、與 [D4]/[D7]「合理估算非精確量測」定位一致（替代：文獻值 ~0.005–0.02 kWh/GB/年，須挑明示未含 PUE 者）。新增決策記錄 [D9]；更新 4.4 路徑 C 表格列、碳排換算段（雲端項改為三段係數）、現況快照 Eco-Agent 列；第 7 節新增 PUE/儲存功耗係數項（討論中）與係數正式取值待查證子項。（待查證：每 GB 功耗係數與副本因子、fleet-wide PUE 當年度值、台電電力係數，皆待可引用來源定值。） |
| 2026-07-21 | v0.17 | **雲端路徑（路徑 C）應用場景盤點與隱私分界**：先界定天花板——雲端碳排量級偏小（[D9] 係數粗估單人 `usageInDrive` 6.593 GB ≈ ~0.058 kWh/年），故本路徑價值在「行為可見、可執行、可教育」而非減碳數字大。盤點四類應用：(一)行為誘因（數位斷捨離：大型/冷/重複檔清理，`usageInDriveTrash` 為其一)、(二)趨勢與異常（`usageInDrive` 時間序列做成長趨勢與暴增偵測，並接回 [D8] 異常帳號判斷)、(三)機構層級洞察（總碳排/成長率、儲存效率 KPI、ESG 報告素材——對企業最實質價值)、(四)教育與意識（「雲端＝一直開著的硬碟」具象化、補齊完整數位碳足跡)。**隱私分界**：(二)(四) 零額外隱私成本可放心納入；(一)(三) 中涉「讀檔案清單/檔名/內容特徵」者需放大 OAuth scope，列為「可行但待隱私/scope 決策」、不默默實作（比照印表機 [D5]/[D6]）。新增決策記錄 [D10]；更新現況快照 Eco-Agent 列；第 7 節新增應用場景盤點項與隱私分界待決策子項。 |
| 2026-07-22 | v0.18 | **印表機 SNMP 五參數納入綁定流程**：`ECO_AGENT_PRINTER_HOST`／`COMMUNITY`／`PORT`／`OID`（＋既有 `printerPollInterval`）中，前四者屬 per-device 環境事實，決議隨 4.4.2 綁定於本機 `.env` 設定（主要填 `HOST`，少數機種覆寫 `OID`），不走 5.2 全域下發；綁定時可上報非敏感的 `HOST`／`OID` 供後台監控、但不反向下發覆蓋本地。`printerPollInterval`（全域策略）續走 5.2。4.4.2 新增「個人專屬印表機 SNMP 參數」小節（含五參數表、HOST 只填 IP、page counter 前後相減與重置防呆）；新增決策記錄 [D11]（per-device 本地事實不進 `sensor_config`、全域策略才進，判準同 [D8]/[D3]）；現況快照 Eco-Agent 列補註 |
| 2026-07-23 | v0.19 | **電梯（4.3）NFC 感測端形式定案：採被動 NFC tag，主動式 ESP32 降級**：關鍵差異不在成本而在「樓層資訊從哪裡來」——被動 tag 的樓層來自 tag 貼附樓層（寫死於 tag 內容），**完全不需知道轎廂即時位置、不需接觸電梯控制系統**；主動 ESP32 隨轎廂移動、自身不知樓層，須串接電梯控制系統（涉 CNS／EN 81 安規、原廠保固、轎廂取電施工，專題取得批准機率極低）或以氣壓／加速度推算（誤差累積過大、不足以支撐歸戶）。故定案被動 tag：零施工／零供電／零改裝／對電梯零風險／可完整移除，且將對電梯公司的請求由「串接控制系統」降為「電梯廳張貼標籤」，審批摩擦大幅降低。更新 4.3 規格段「識別」條目（改述被動 tag 與不接觸控制系統聲明）、新增「未掃出場的孤兒記錄處理」（逾時自動結算，同 4.2 session 思路）、依賴宣告（新增 4.5 URI 格式約定與場域張貼許可）；新增決策記錄 [D5]；現況快照電梯列更新；第 7 節新增 4.3 已決議（感測端形式）、待安排（張貼許可與窗口、tag 數量與位置）、待設計（孤兒記錄逾時門檻）三項，並自原待實測項移除 ESP32 選型。另產出附件《電梯公司詢問清單》供場域接洽使用 |
| 2026-07-30 | v0.20 | **Eco-Agent 上傳協定統一為 HTTPS，冪等去重五項全數定案**。(一)**[D13] 路徑 A／B 由 MQTT 改走 HTTPS**：原「後端回 200 才清佇列」合約在 MQTT 上不成立（PUBACK 由 Broker 而非後端發出，Agent 清佇列時資料可能仍在後端記憶體未落地，崩潰即永久遺失且無副本可重送）；且 4.4.2 撤銷（`401/403`）與 5.2 配置版本號夾帶皆需 HTTP 回應語意，續走 MQTT 反須額外再開一條 HTTPS。又 Eco-Agent 依 4.4.3 為「單一路徑單日僅 1 筆」的極低頻上傳、執行於桌機而非受限硬體，MQTT 的輕量優勢用不上。故三路徑統一 HTTPS；廢棄物樹莓派續走 MQTT（匿名單向、不需回程），混合協定架構的分流判準由「是不是 IoT 裝置」修正為「需不需要後端的回應」。否決 (a) MQTT QoS 1 延後 ack、(c) 措辭降級為盡力而為。(二)**[D14] 冪等去重定案**：payload 與 `DIGITAL_USAGE` 補 `collected_at`（Agent 採集時間戳）作亂序抵達勝出規則；`DIGITAL_USAGE` 補 `device_id` FK 並納入唯一鍵以修正「事件 ID 為裝置粒度、落庫鍵為人粒度」的錯位（一員工綁桌機＋BYOD 筆電即撞鍵），**惟雲端路徑不納入**（帳號層級事實，多裝置查得同值，加總即重複計算），以兩個 partial unique index 分別表達，避開四段鍵下 NULL 互不相等導致雲端去重靜默失效的陷阱；`employee_id` 保留為歸戶快照（裝置轉手不追溯改寫歷史）；對 Agent payload 零改動（後端由 `id_token` 查 `DEVICE_BINDING` 即同時取得 `employee_id` 與 `device_id`）。否決「一員工限綁一裝置」。應用層摺疊＋DB constraint 兩層並用，upsert 加 `EXCLUDED.collected_at >` 條件。更新第 2 節協定表、第 3 節協定架構（新增分流判準說明）、4.4 標題／依賴／路徑表／payload／協定注意、4.4.2 撤銷註記、4.4.3 至少一次送達段（改寫為端到端成立）、5.1 寫入策略（分 MQTT／HTTPS 兩軌）與冪等去重規格（含 SQL）、容錯備註、P2／P3 工作項、5.2 定位總結／數據面／配置參數表／下發通道、技術堆疊三列、現況快照三列；新增決策記錄 [D13][D14]；第 7 節原 [4.4/5.1][待設計] 五項改列已決議。**ERD 同步**：`DIGITAL_USAGE` 新增 `device_id` FK 與 `collected_at`、新增關係 `DEVICE ||--o{ DIGITAL_USAGE : reports`（補上與 `WASTE_EVENT` 對稱的感測硬體關係）、`DEVICE` 新增 `display_name`（裝置分項對使用者可見後 UUID 無法辨識）。 |
| 2026-08-06 | v0.21 | **後端資料存取層分工定案（[D4]）**：釐清團隊成員已完成之初步後端（`eco_sensing_backend`）實際採 **Supabase PostgREST**（`services/crud.py` table-agnostic 泛用 CRUD 層）存取資料庫，與 5.1 原文「一律走 connection pooler」字面不同，v0.21 正式定案為**兩層並存**：一般 CRUD 維持 PostgREST（開發成本趨近零、為 P1「API 跑通」最短路徑），**僅 `POST /api/digital-usage/batch` 一條改走 asyncpg 直連 connection pooler（Transaction mode 6543）**。分流判準為「**是否需要條件式 upsert／交易控制**」。理由：PostgREST 對 4.4 [D14] 所需三事皆無法表達——(a) `Prefer: resolution=merge-duplicates` 只能無條件覆蓋，無處可寫 `WHERE EXCLUDED.collected_at > digital_usage.collected_at`，而無條件覆蓋正是 [D14] 缺口一要防的行為；(b) `?on_conflict=` 無法附帶 partial index 的 `WHERE` 述詞，多種鍵粒度無從區分；(c) 無法把應用層摺疊後的兩句條件式 upsert 組進單一交易、於 commit 後才回 `200`（[D13] 端到端至少一次送達之地基）。強調此為**表達力取捨而非效能取捨**（PostgREST 自身亦連著 pooler，兩者不在同一層次）。否決方案 B（PG function ＋ `/rpc/`，去重邏輯下沉 SQL 難測試）與方案 C（整體重寫 `crud.py`，為一條路徑重寫二十餘端點）。連帶更新：現況快照後端列、5.1 規格段新增「資料存取層分工」表（含 `SUPABASE_DB_URL` 環境變數、`statement_cache_size=0` 等 transaction mode 約束）、5.1 冪等去重段新增實作註記（partial index 作 conflict target 時 `ON CONFLICT` 須原樣重述 `WHERE` 述詞、一批須拆兩句 upsert 於同一交易內）、P1／P2 工作項、技術堆疊後端列（補部署鏈 Docker → GitHub Actions → Hugging Face Spaces）。**現況記錄**：兩個 partial unique index 尚未於 `schema.sql` 建立，列 P1 優先項；Agent 端 `path_type` 已更新為 `computer`／`printer`／`drive`；資料庫已建表但**無任何資料**，schema 調整無遷移成本。第 7 節新增已決議一項、待實作三項（partial index 補建、Eco-Agent 專用端點清單、`id_token` 於 `EMPLOYEE` 與 `DEVICE_BINDING` 之語意釐清）。 |
| 2026-08-06 | v0.22 | **App 端後端認證列為 P1 前置工作，並更正 4.4.2 [D2] 之事實誤述**。(一)**[D2] 更正**：原述員工端 App「已有完整登入身份系統」屬誤讀——依《App 系統功能》§1.0，登入頁**不驗證帳密**（選「員工端」即進入）、`DemoAuthStorage` 僅以 `SharedPreferences` 存一個布林登入狀態、**後端對此一無所知**；既有的是登入畫面與狀態保留，非可供後端驗證的身份系統。故 4.4.2 步驟 4「App 把已驗證身份 + binding_code 送後端」的前提目前尚不成立。此更正**不影響 [D2] 選擇方案 B 的結論**（「身份驗證在已登入 App 完成、Agent 不碰員工 ID」的架構仍正確），只是其前置條件多一項。(二)**4.4.2 步驟 4 明訂身份傳遞方式**：`employee_id` **僅由 `Authorization: Bearer <App session token>` 解出，不得出現在 request body**，body 只帶 `code`；後端驗簽得人的一半、查 `BINDING_CODE` 得裝置的一半，兩者相接即「後端配對」的實質內容。列為硬性約束，P1 以臨時憑證實作時亦須維持此介面形狀。(三)**新增 5.1 [D5]**：釐清此缺口非 Eco-Agent 專屬——4.1 差旅、4.2 廢棄物 session、4.3 電梯 payload 皆含 `employee_id`，若由 client 於 body 指定，[D2] 為 Agent 堵住的作弊後門會從其餘三模組原樣打開；且個人歸戶直接連動 EXP／碳幣與 Shared Savings，歸戶可偽造即誘因機制失效，不可用「專題規模小」折抵。故確立**全系統統一原則：`employee_id` 一律由憑證解出**。P1 最小落地範圍：`EMPLOYEE` 補 `password_hash`、`POST /api/auth/login` 簽發 JWT、`get_current_employee` dependency、App 端改存真 token；Refresh／密碼重設／帳號鎖定列 P3。並載明臨時替代作法（dev token）及其唯一要點——介面形狀須正確，否則 P1 測通的是日後必須整段拆掉的假流程，且被跳過的恰是唯一需驗證的那一段。連帶更新：現況快照新增「App 身份認證」一列、P1 工作項與完成判準、第 7 節新增已決議一項與待設計一項（App token 儲存位置與效期策略）。 |
| 2026-08-07 | v0.23 | **路徑 B（印表機）鍵粒度修正與感測值形式改變**。(一)**[D14] 缺口二修正（改表格呈現）**：v0.20 為修正多裝置撞鍵而將 `device_id` 納入唯一鍵，惟路徑 B 比照電腦路徑處理有誤——**桌機與筆電同指一台專屬印表機**時，兩個 Agent 各自回報同一台機器的頁數、分列後加總即重複計算，誤差方向由低估翻為高估且倍率隨開機重疊區間浮動（1x～Nx）、無法事後修正。根因為「裝置是主體」與「裝置是觀測者」混淆：路徑 A 的電腦本身即被測量對象（per-device 正確），路徑 B／C 的裝置僅為觀測者，測量對象分別是印表機與 Google 帳號。故**路徑 B 歸鍵改用 `printer_serial`（SNMP 讀取）**、鍵粒度依路徑分三組（電腦 per-device／印表機 per-printer／雲端 per-account），以三個 partial unique index 分別表達。附帶效益：同一序號掛在兩個 `employee_id` 底下即可告警，把「兩人共用同一台『個人專屬』印表機」這個原本無法偵測的靜默錯誤變成可稽核條件。[D14] 缺口二改為表格（路徑／改動前資料粒度／問題／改動與改動後資料粒度），實作細節（三組 index、序號 OID 與退化路徑、payload 影響、`employee_id` 快照、下游聚合約束、ERD 改動、否決限綁一裝置）簡化列於表後。(二)**新增 [D15]：路徑 B 改送 SNMP 壽命累計讀數、差分移至後端**——原由 Agent 本機相減後上送 `print_pages`，baseline 只活在 Agent 本機，重裝／換機／佇列毀損即遺失（輕則用量消失、重則從 0 重算把整台機器壽命頁數記到某員工頭上），屬獨立於 [D14] 的缺陷；且多觀測者各自從自身 baseline 算出的 delta 不是同一個量，`collected_at` 最新者勝在 delta 語意下失效。改送絕對讀數後多觀測者讀得同值、該規則重新成立，語意亦與路徑 C 對齊（送絕對狀態值、最新者勝、後端換算），`print_pages` 順勢改為後端計算欄位（符合 [D7]），計數器重置防呆一併移至後端。更新：現況快照 Eco-Agent／印表機歸戶兩列、4.4 路徑表 B 列與 payload（`print_pages` → `printer_serial` ＋ `printer_page_counter`）、碳排換算列印項、4.4.2 SNMP 參數表（新增 `ECO_AGENT_PRINTER_SERIAL_OID`）與讀值／序號兩條、4.4.3 落庫鍵拆三組、5.1 index SQL 改三組與實作註記（拆三句 upsert）、5.1 聚合約束、[D4] 鍵粒度數、技術堆疊 SNMP 列；第 7 節新增已決議一項與待實測（序號 OID 支援度）、待設計（跨日補記）兩子項。**ERD 同步**：`DIGITAL_USAGE` 新增 `printer_serial` 與 `printer_page_counter`（`print_pages` 保留為後端計算欄位）。(三)**4.4.2 綁定流程補漏（同版追加）**：補上原缺漏的**步驟 5.5「Agent 輪詢領取 token」**——原六步驟由「後端發放 token」直接跳至「Agent 取得 token」，未說明 token 如何抵達 Agent；實際情境為 QR 顯示於電腦螢幕、掃碼發生於手機，後端與 Agent 之間此刻並無既有連線可推送（Agent 尚未持有任何憑證），故只能為 Agent 端輪詢。連帶：步驟 1 回應新增 `device_secret`（不編入 QR，供 ③ 證明身分，避免「誰持有 `code` 誰就能領 token」）、`BINDING_CODE` 表補存該欄、新增「後端須實作的四個端點」對照表（①建立綁定碼／②核銷並配對／③領取 token／④換發 token，含呼叫者與認證方式），並註記 `DEVICE` 列重複建立問題（建議 Agent 持久化 `device_uuid` 供後端 upsert）。 |
| 2026-08-13 | v0.24 | **App 端憑證機制定案：比照 Eco-Agent 採雙 token**，回填 v0.22 遺留的「App token 儲存位置與效期策略／是否比照雙 token」待設計議題。決議 App 端採短效 App Access（1h）＋長效 App Refresh（30 天、不輪換），運作與 4.4.2 Eco-Agent 雙 token 同構（Access 每請求用、過期以 Refresh 無感換發，Refresh 亦過期／撤銷才重登）。**主要依據為《App 系統功能》§1.0「App 冷啟動時自動判斷是否已登入」需求**：該需求要「打開即進、長期免登入」的體驗，單枚短效 JWT 冷啟動幾必過期而須頻繁重登、與需求對撞，若改拉長單枚 JWT 的 `exp`（如 30 天）則因 JWT 無法即時撤銷、又無 Eco-Agent「每次上傳夾帶撤銷」通道兜底，等於把唯一風險上界放到最大且落在誘因機制（EXP／碳幣／Shared Savings）最不能被冒用處；雙 token 拆開「證明身份（短 Access）」與「保持登入（長 Refresh）」，同時取得短風險窗口、長期免登入、可撤銷三者。載明：冷啟動以 `flutter_secure_storage` 中 Refresh 靜默續 Access（無網時先進 App 看快取、待網補換）；**過期判定權在後端**（`get_current_employee` 每請求驗簽＋驗 `exp`、回 `401`），App 讀 `exp` 僅作 UX 預判、以收到 `401` 觸發續期／重登；Refresh 存 secure storage 不放 `SharedPreferences`；後端存 `refresh_token_hash` 供撤銷（離職／遺失標 `revoked`，Access 至多撐≤1h）；效期取值**脫鉤「月結算」**（30 天為體驗與風險之折衷，與資料聚合排程無關、同以月為單位僅屬巧合）；不啟用 Refresh 輪換（列 P3）。連帶更新：現況快照「App 身份認證」列、4.4.4 憑證效期表（改標題為「憑證效期（後端簽發策略，不下發）」並補 App Access／App Refresh 兩列，加註本組屬後端簽發策略非 `sensor_config` 下發參數）、5.1 [D5] 新增 v0.24 決議子段（含冷啟動流程、後端判定權、儲存位置、撤銷、效期脫鉤月結算、P1 落地影響——`login` 改一次簽發雙 token、新增 App 換發端點）、P1 工作項、第 7 節該待設計議題標記已決議。 |
| 2026-08-13 | v0.25 | **新增第 8 章「Eco-Sensing App（前端應用層）」，將 App 相關內容自 §4／§5 抽出獨立呈現，並併入《App 系統功能 v0.4》**。動機：App 自身的功能、實作狀態與身份驗證決議屬「前端應用層」，性質有別於 §4 四大感測／核算模組與 §5 橫切層，散置各處不利檢視，故集中為獨立章節。新章節結構：8.1 員工端功能（Flutter）規格、8.2 企業端功能（Vue Web 後台）規格、8.3 實作狀態表（原《App 系統功能》「實作狀態」✅／🟡／⚪ 獨立拉出，員工端 App1.0–1.8 與企業端 App2.0–2.7 兩表）、8.4 App 身份驗證雙 token 機制（集中呈現雙 token 規格與 App Access 1h／App Refresh 30 天數值，與 4.4.4、5.1 [D5] 同源）、8.5 決策記錄（**原《App 系統功能》版本紀錄繼承為本章決策記錄**，另加 [A1] 雙 token、[A2] 純前端登入隱藏依賴、[A3] employee_id 由憑證解出三條 App 專屬決策）。章節編號：新章節插為 §8，原 §8 版本紀錄順移為 §9；§6 Roadmap、§7 Open Questions 位置與所有「第 7 節」交叉引用不變。**檔案治理**：《Eco-Sensing_App_系統功能_v04.md》自本版起封存、不再維護，App 功能演進改於本章第 8 章更新（8.5 版本紀錄末列已註記併入）。5.1 [D5] 與 4.4.4 效期表維持為雙 token 之權威出處，8.4 為 App 落地形狀之集中複述。 |
| 2026-08-21 | v26 | **手動上傳用紙量落地定案（新增 [D16]）**，收斂《驗證機制端點關係表》§3.1 點出的 `digital-usages`（複數）vs `digital-usage/batch`（單數）定位落差。五點定案：(1)**端點定位**——`POST /api/digital-usages`（PostgREST 泛用 CRUD、App 員工 `Bearer`）正式保留作 App 手動補登管道，與 Agent 自動路徑並存為 `DIGITAL_USAGE` 兩來源；手動上傳因 App 端已彙總、無條件式 upsert／交易控制需求，落 PostgREST 側（[D4] 判準的正確套用），不動用 asyncpg。(2)**Agent 端點更名**——`POST /api/digital-usage/batch` 更名為 `POST /api/agent/digital-usage/batch`、收進 `/api/agent/*` 命名空間（與 4.4.2 綁定鏈同組），以命名空間承載「員工 vs 裝置」認證體系差異、消除與複數 `digital-usages` 的混淆；改未實作的 Agent 端而非已實作的 App 端，正名零成本。(3)**來源識別**——`DIGITAL_USAGE` 新增獨立欄位 `sensing_mode`（`auto`／`manual`）而非於 `path_type` 加 `printer_manual` 值,理由為正交性（`path_type` 描述感測對象、`sensing_mode` 描述感測方式）,避開 [D14] per-printer 鍵在 `printer_serial` 為 NULL 時的去重陷阱;**現況取捨聲明**:手動補登目前只用於印表機、不預期擴散,拆欄是為保留擴充性與模型正交/查驗乾淨,非因當前會擴散。(4)**去重鍵與彙總**——採 App 端彙總、後端一天一列:員工當日多筆記錄、每筆送出前於 App 本地編輯,App 上傳當日彙總總量一筆,後端 upsert 唯一鍵（`employee_id`, `usage_date`, `path_type='printer'`, `sensing_mode='manual'`）;後端不存明細、不需 per-筆 `event_id` 冪等、不需 asyncpg;`collected_at` 沿用作防亂序重送保險（與編輯無關,編輯在本地送出前完成）。(5)**兩管道互斥**——個人專屬機走 Agent SNMP（`auto`）、共用機走 App 手動（`manual`）,同一台機器不同時產生兩列,無雙重計算。連帶更新:現況快照印表機歸戶列與後端列、4.4 路徑表 B 列、4.4 payload 段（Agent 端點更名＋新增手動路徑獨立說明）、4.4.3 落庫鍵段（補手動路徑不屬 Agent 事件 ID 段之註記與 `collected_at` 防亂序用途）、5.1 資料存取層分工表兩列、5.1 冪等去重 index SQL（三個 partial index 增為四個、皆帶 `sensing_mode` 述詞、新增 `uq_digital_usage_printer_manual`）與實作註記、P1 工作項、8.1 手動上傳描述;新增決策記錄 [D16];第 7 節新增已決議一項（§3.1 定位落差）與待釐清子項（落庫後更正語意 `PATCH` vs 重送覆蓋、手動上傳觸發機制與誘因設計）。**ERD 同步**:`DIGITAL_USAGE` 新增 `sensing_mode` 欄位。不動 [D12]（`path_type` 三值與 Agent 明送原則）、[D14]（三組 Agent 自動路徑鍵,僅補 `sensing_mode='auto'` 述詞）、[D4]（分流判準不變）。 |
