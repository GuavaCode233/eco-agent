# Eco-Agent

Eco-Sensing 專案的桌面能耗監測 Agent（Desktop Agent）。以 **Go** 實作的無人值守背景常駐程式，並行採集三條數位能耗感測路徑，寫入本機持久化佇列，再由四重觸發批次上傳到後端。

> 開發脈絡與規格見 [CLAUDE.md](CLAUDE.md)（單一任務脈絡，Claude Code 常駐讀取）；完整專案決策依據見 [docs/Eco-Sensing_專案context文件_v14.md](docs/)。

## 現階段狀態

| 路徑 | 內容 | 協定 | 狀態 |
|------|------|------|------|
| Step 0 | 地基：持久化佇列 + 配置 + 綁定 mock + 四重觸發上傳骨架 | — | ✅ |
| Step 1（路徑 A） | 電腦使用（active/idle 分態、CPU 使用率、後端計算能耗） | MQTT（mock） | ✅ |
| Step 2（路徑 C） | 雲端儲存（真串 Google Drive API v3 + 持久化時間戳觸發） | HTTPS（mock） | 🟡 2.1–2.5 完成，2.M 待做 |
| Step 3（路徑 B） | 印表機（SNMP 輪詢歸戶） | MQTT（mock） | 🟡 3.1 完成，3.2–3.M 待做 |

後端尚未完成：綁定、集中配置、上傳端點/token 以常數／mock 替代；佇列、觸發、冪等、去識別化為真做。所有「等後端」處以標記標出（見下方清單）。

## 建置與執行

```sh
go build ./...                      # 全專案建置
go test ./...                       # 全部單元測試

# 交叉編譯（單一靜態執行檔）
GOOS=windows GOARCH=amd64 go build -o dist/eco-agent.exe ./cmd/eco-agent
GOOS=darwin  GOARCH=arm64 go build -o dist/eco-agent     ./cmd/eco-agent
```

### 各步驟獨立 demo

```sh
go run ./cmd/eco-agent-demo         # Step 0：四重觸發 + at-least-once + 撤銷自清
go run ./cmd/computer-demo          # Step 1：路徑 A 電腦使用感測端到端
go run ./cmd/drive-demo authorize   # Step 2.1：Google Drive OAuth 一次性授權
go run ./cmd/drive-demo             # Step 2.1：真串 Drive API 取 storageQuota
go run ./cmd/drive-sensor-demo      # Step 2.2–2.4：持久化時間戳觸發（冷啟動/到期/開機補查）
go run ./cmd/printer-demo -mock     # Step 3.1：對本機 mock SNMP responder 取 page counter 增量
go run ./cmd/printer-demo           # Step 3.1：對真實印表機 SNMP 輪詢（需設 ECO_AGENT_PRINTER_HOST）
```

以 `ECO_AGENT_PROFILE=testing` 切換測試值（大幅縮短時間類參數，數分鐘內觀察完整流程）。

## 路徑 C：Google Drive API 憑證設定（§6）

路徑 C 真串 Google Drive API v3（`about?fields=storageQuota`）。需人工提供 OAuth 憑證，**勿硬編碼、勿提交版控**（.env 與 token 檔已列入 `.gitignore`）：

1. 於 [Google Cloud Console](https://console.cloud.google.com/) 建立專案，啟用 **Google Drive API**。
2. 建立 OAuth 2.0 用戶端憑證，應用程式類型選 **Desktop app**。
3. 複製 [`.env.example`](.env.example) 為 `.env`，填入下列三處：

| 環境變數 | 用途 |
|----------|------|
| `GOOGLE_OAUTH_CLIENT_ID` | OAuth 2.0 用戶端 ID（必填） |
| `GOOGLE_OAUTH_CLIENT_SECRET` | OAuth 2.0 用戶端密鑰（必填） |
| `GOOGLE_OAUTH_TOKEN_PATH` | refresh token 落地路徑（可選，預設使用者設定目錄下 `eco-agent/google_oauth_token.json`） |

授權範圍固定為只讀 metadata：`https://www.googleapis.com/auth/drive.metadata.readonly`（取 storageQuota 已足夠，最小權限）。

4. 首次執行一次性授權（loopback 流程，瀏覽器同意後自動導回本機並保存 refresh token）：

```sh
go run ./cmd/drive-demo authorize
go run ./cmd/drive-demo             # 之後即可取用量
```

未設憑證或未授權時，路徑 C 優雅降級（記 log、跳過），不使整個 Agent 崩潰。

## 路徑 B：個人專屬印表機 SNMP 設定（§2 Step 3）

路徑 B 本階段只做「個人專屬印表機 SNMP 輪詢歸戶」一軌（印表機與員工一對一，故 page counter 增量可直接歸給本機 ID Token）。共用機的 Print Server Log / Pull Printing API 為未來實作，不在範圍。

查 SNMP v2c（UDP 161）OID `1.3.6.1.2.1.43.10.2.1.4`（`prtMarkerLifeCount`，出廠以來累計輸出頁數），前後兩次相減得增量頁數。

| 環境變數 | 用途 |
|----------|------|
| `ECO_AGENT_PRINTER_HOST` | 印表機 IP 或主機名（必填；未設即視為本機無個人專屬印表機，路徑 B 跳過） |
| `ECO_AGENT_PRINTER_COMMUNITY` | SNMP v2c 唯讀 community（可選，預設 `public`） |
| `ECO_AGENT_PRINTER_PORT` | SNMP 埠（可選，預設 `161`） |
| `ECO_AGENT_PRINTER_OID` | page counter instance OID（可選，預設 `…43.10.2.1.4.1.1`；index 非 1.1 的機種可覆寫，未覆寫時程式會自動巡走該欄取第一筆） |

前提：Agent 所在機器須與印表機**同網段**且對方開啟 SNMP。BYOD 情境下常不成立——查不通時記 log、跳過，不使 Agent 卡住。

免真實印表機的驗證：`go run ./cmd/printer-demo -mock` 會在本機起一個 mock SNMP responder（`internal/sensors/printer/mocksnmp.go`，僅供開發驗證，不參與正式採集），每輪 +3 頁，可直接觀察增量換算與 ID Token 歸戶。

## 「等後端」標記清單（§7 / §8.6）

暫以常數/mock 代替、日後要接後端的位置統一加標記，可 grep 一次列出：

```sh
grep -rn "TODO(backend)\|TODO(secrets)\|TODO(platform)\|MOCK:" --include="*.go" | grep -v _test.go
```

| 標記 | 含義 | 現有數量 |
|------|------|---------|
| `TODO(backend)` | 等後端 API 就緒才能替換（綁定、配置下發、真實上傳端點、撤銷回應、`drive_trash_gb` 激勵欄位待確認、印表機位址下發） | 27 |
| `TODO(secrets)` | 需人工填入的憑證（Google OAuth） | 4 |
| `TODO(platform)` | 需補真實 OS 實作（金鑰庫 DPAPI/Keychain 等） | 2 |
| `MOCK:` | 目前回傳假值的函式/常數（mock token、mock 端點、mock sampler、mock SNMP responder） | 12 |

> 數量隨開發演進，以上為 Step 3.1 完成時的快照；實際以 grep 結果為準。
