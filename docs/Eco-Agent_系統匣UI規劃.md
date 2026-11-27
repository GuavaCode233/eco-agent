# Eco-Agent 系統匣 UI 規劃

> 目的：替 Eco-Agent 補上系統匣（Windows 通知區域／macOS 選單列）、綁定 QR 顯示，以及背景常駐所需的平台功能。規格依據見 `docs/Eco-Sensing_專案context文件_v33.md` §4.4 [D1]（Agent 定位為「無頭背景常駐程式（daemon / system tray），幾乎無 UI」）與 `.claude/CLAUDE.md` §3（四重觸發）。
>
> 本檔各階段完成後，請更新「開發步驟」表的狀態欄。

## 1. 現況與缺口

目前 `cmd/eco-agent` 是純命令列程式，有以下缺口：

| 缺口 | 現況 | 影響 |
|------|------|------|
| 無 UI／系統匣 | 只用 `slog` 印文字 log；未以 `-H windowsgui` 建置，一定會開主控台視窗 | 員工看不到狀態，也沒有操作入口 |
| 綁定 QR 只有文字 | `enroll_http.go` 的 `bindRealLocked` 只在 log 印出 `ecosensing://bind?code=...` | 員工無法直接用 App 掃碼 |
| 撤銷後程式結束 | 上傳回 401/403 → `ErrStopped` → `main` 結束 | 員工不知道已被撤銷，也無法重新綁定 |
| 關機前 hook 不完整 | 只接 Ctrl+C／SIGTERM，未攔截 Windows `WM_QUERYENDSESSION` | 改成無視窗程式後，關機時剩餘資料不會先送出（違反 §3「關機／登出前 hook」） |
| 喚醒後未補送 | 只有開機時補送 | 不符 §3「開機／喚醒後檢查」的「喚醒」部分 |
| 無開機自啟 | 未寫入登錄檔 `Run`，也沒有 LaunchAgent | 重開機後 Agent 不會自動執行 |
| 無單一實例保護 | 可同時執行多份 | 兩份同時寫同一個 SQLite，路徑 A 也會重複採樣 |

## 2. 已定案的設計決策

| 決策 | 選擇 | 理由 |
|------|------|------|
| 解綁時佇列有殘留資料 | **先送再丟**：解綁前先上傳一次；還有送不出去的就跳確認框，告知「將捨棄 N 筆」，員工同意後才清空佇列並解綁 | 上傳用的是批次當下的 `id_token`，殘留資料若留到換人重綁後才送，會算到新員工名下。代價是離線解綁時會遺失少量資料，但這只是已解綁員工的資料，且需員工明確同意 |
| 撤銷時佇列有殘留資料 | **與解綁同一規則，直接捨棄**：偵測到撤銷、停止三路徑後清空佇列，並記錄捨棄筆數（`Status.DiscardedOnRevoke`）供 UI 告知員工；不跳確認框 | 撤銷後後端已拒收這台裝置的舊綁定，殘留資料無論如何都送不出去，沒有可選的替代做法；若留到「重新綁定」後才送，會以新綁定的 `id_token` 歸戶，換人重綁時算到別人名下 |
| 綁定 QR 顯示方式 | **本機網頁**：Agent 在 `127.0.0.1` 開一個只限本機存取的小頁面，用預設瀏覽器打開 | 跨平台、不需 GUI 套件；綁定碼重抓時 QR 自動更新，核銷後頁面直接顯示「已綁定」 |
| 系統匣套件 | `fyne.io/systray` | 持續維護；Windows 不需 cgo；macOS 需要 cgo，與現有金鑰庫相同，不增加建置限制 |
| 綁定頁關閉時 | **暫停綁定流程**：停止輪詢③、不再索取新碼，進入「尚未綁定（閒置）」，直到下次開機或員工手動重新啟動綁定 | 原本未綁定時會無限換碼，每 5 分鐘在後端 `binding_code` 表新增一列（約 288 列／天）；改為只有頁面開著才產生新碼 |
| 閒置時提醒員工綁定 | **Windows toast 通知**：頁面關閉後**立即提醒 1 次**，**1 小時後再提醒 1 次**，之後不再提醒（直到下次開機或手動啟動綁定）。**每次關頁都重新開始一輪**；但距上次提醒未滿 **10 分鐘冷卻**時，略過「立即」那次、只保留 1 小時後那次。點通知直接開啟綁定頁 | 兩次就夠員工注意到；再多會讓員工直接關掉通知權限。冷卻避免員工短時間反覆開關頁面時每次都跳通知。開機時本就會自動開啟綁定頁，不另發通知 |
| 綁定碼逾時 | **立即換新碼**，不再等 30 秒；30 秒等待只用於後端錯誤（連不上、非 200） | 逾時代表後端正常、只是員工沒掃，多等 30 秒只會讓 QR 頁出現空窗 |

## 3. 架構：先把啟動流程改成監督者迴圈

`main.go` 現在是直線流程（綁定 → 三路徑 → `up.Run` → 結束），無法處理「撤銷後留著系統匣」「從選單解綁再回到綁定」。另外 macOS 規定系統匣必須跑在 main goroutine，Agent 本體要移到別的 goroutine。

新增 `internal/agent` 套件：

```
main goroutine ── tray.Run（系統匣，macOS 規定須在 main）
                    │  呼叫
                    ▼
agent.Run(ctx) 監督者迴圈（另一個 goroutine）
  loop:
    1. 綁定（見下方「綁定狀態流程」；綁定碼經 handler 推給網頁）
    2. 開子 ctx → 啟動三路徑 + uploader.Run
    3. 等待以下其一：
         - 員工解綁     → 取消子 ctx → 回到 1
         - 偵測到撤銷   → 取消子 ctx → 狀態設為「已撤銷」，等 Rebind() → 回到 1
         - 關機訊號     → 取消子 ctx（uploader 搶送零頭）→ 結束
```

### 綁定狀態流程

| 狀態 | 進入條件 | 行為 |
|------|---------|------|
| **綁定中** | 開機時尚未綁定（自動開啟綁定頁）、系統匣按「開始綁定」、點擊提醒通知 | 頁面開著就持續換碼：每 2 秒輪詢③；綁定碼逾時**立即換新碼**；後端錯誤才等 30 秒再試 |
| **尚未綁定（閒置）** | 綁定頁關閉（約 15 秒未收到頁面心跳，見 U2.6） | 取消綁定 ctx，**完全不打後端**；系統匣顯示「尚未綁定」；立即發 1 次 toast，1 小時後再發 1 次（見 U4.7），之後不再提醒 |
| **已綁定** | 輪詢③收到 `consumed` | 頁面顯示「已綁定」；啟動三路徑與上傳 |
| **已撤銷** | 上傳回 401/403 | 停止三路徑與上傳，**捨棄佇列殘留**（記錄筆數），程式不結束；等員工按「重新綁定」 |

未綁定期間三路徑不啟動，不採集資料。

`agent.Agent` 對外介面：

| 方法 | 說明 |
|------|------|
| `Run(ctx) error` | 監督者迴圈 |
| `Status() Status` | 狀態快照：綁定狀態（綁定中／已綁定／已撤銷）、目前綁定 URI 與到期時間、佇列筆數、上次上傳時間／原因／結果、三路徑啟用與否 |
| `Unbind(ctx, confirm func(leftover int) bool) error` | 照「先送再丟」流程：① 停三路徑（避免新資料寫入）→ ② 手動上傳一次 → ③ 仍剩 N 筆則呼叫 `confirm(N)` → ④ 同意後 `queue.Purge` 並呼叫 `enr.Unbind`；不同意則恢復原狀 |
| `StartBinding()` | 從「尚未綁定（閒置）」回到「綁定中」；系統匣「開始綁定」、點擊通知、開啟綁定頁時呼叫 |
| `Rebind()` | 已撤銷時清除終止狀態，讓迴圈重新進入綁定 |
| `FlushNow()` | 選單「立即上傳」與喚醒後補送共用 |

## 4. 開發步驟

**狀態欄圖例**：⬜ 未開始 ／ 🟡 進行中 ／ ✅ 完成 ／ ⏸️ 暫緩

| 階段 | 內容 | 狀態 |
|------|------|------|
| U1 | Agent 核心重構（監督者迴圈，不含 UI） | ✅ |
| U2 | 本機狀態與綁定網頁 | ⬜ |
| U3 | 系統匣 | ⬜ |
| U4 | Windows 平台補齊（關機／喚醒、單一實例、開機自啟、log 檔、無視窗建置、綁定提醒通知） | ⬜ |

建議依 U1 → U4 順序。U1 完成後現有行為不變，可先單獨合併。

### U1 — Agent 核心重構

| # | 子項 | 說明 | 狀態 |
|---|------|------|------|
| U1.1 | `internal/agent` | 實作第 3 節的監督者迴圈、綁定狀態流程與對外介面 | ✅ |
| U1.1a | 綁定重試節奏 | 取代 `cmd/eco-agent` 現有 `ensureBoundWithRetry`：綁定碼逾時立即換新碼；後端錯誤才等 30 秒；頁面關閉時取消綁定 ctx 進入閒置 | ✅ |
| U1.2 | `internal/enroll` | 新增 `WithBindingCodeHandler(func(uri string, expiresAt time.Time))`，取代只印 log；新增 `ResetRevoked()` 供 `Rebind()` 使用 | ✅ |
| U1.3 | `internal/queue` | 新增 `Purge(ctx) (int, error)`，**僅限解綁與撤銷時捨棄殘留資料使用**（其餘情況仍只在後端回 200 後清除，at-least-once 不變） | ✅ |
| U1.4 | `internal/uploader` | 記錄上次上傳的時間、原因與結果，供 `Status()` 使用（`Flush` 已公開，可直接呼叫） | ✅ |
| U1.5 | `cmd/eco-agent` | 改用 `agent.Run`；保留 `-headless` 旗標（不開系統匣、log 印主控台，供開發與 demo）。**現況**：系統匣尚未實作，`cmd/eco-agent` 目前一律為 headless；`-headless` 旗標待 U3 加入系統匣時一併新增。CLI `-unbind` 改走「先送再丟」，有殘留時需加 `-force` 才捨棄 | ✅ |
| U1.V | 驗證 | 單元測試：解綁三情境（全部送出／有殘留且同意捨棄／有殘留但取消）、撤銷後 `Rebind`、監督者迴圈能重啟子 ctx、綁定碼逾時立即換碼（不等 30 秒）、進入閒置後不再打後端、`StartBinding` 能從閒置恢復；`-headless` 跑法維持持續換碼（無頁面可關） | ✅ |

### U2 — 本機狀態與綁定網頁（`internal/ui/web`）

| # | 子項 | 說明 | 狀態 |
|---|------|------|------|
| U2.1 | 本機 HTTP server | 只監聽 `127.0.0.1:0`（隨機埠），網址路徑帶隨機 token（`/<token>/`），其他程式猜不到網址；錯誤 token 回 404 | ⬜ |
| U2.2 | 頁面 | `embed` 打包的 HTML；JS 每 2 秒拉 `/<token>/status.json` | ⬜ |
| U2.3 | QR | `/<token>/qr.png` 以 `skip2/go-qrcode` 即時產生，附到期倒數；綁定碼重抓時自動換新 | ⬜ |
| U2.4 | 狀態呈現 | 綁定中 → QR；已綁定 → 佇列筆數、上次上傳時間、三路徑狀態；已撤銷 → 提示（含撤銷時捨棄的筆數 `DiscardedOnRevoke`）並引導至系統匣「重新綁定」 | ⬜ |
| U2.5 | 安全 | 頁面只露出綁定碼（5 分鐘過期、需 App 登入才能核銷）；`device_secret` 與所有 token 一律不輸出；回應加 `Cache-Control: no-store` | ⬜ |
| U2.6 | 頁面心跳 | 以頁面每 2 秒拉 `status.json` 作為心跳；約 15 秒未收到即判定頁面關閉 → 通知 agent 進入閒置。頁面（重新）開啟時呼叫 `StartBinding()` 恢復換碼 | ⬜ |
| U2.V | 驗證 | 測試：status JSON 內容、錯誤 token 回 404、回應中不含 `device_secret`／token、心跳逾時觸發閒置、重新開頁恢復綁定 | ⬜ |

### U3 — 系統匣（`internal/ui/tray`，`fyne.io/systray`）

| # | 子項 | 說明 | 狀態 |
|---|------|------|------|
| U3.1 | 選單 | 見下方選單表 | ⬜ |
| U3.2 | 首次啟動 | 未綁定時自動以預設瀏覽器開啟綁定頁 | ⬜ |
| U3.3 | 確認框 | 新增 `platform.Confirm(title, msg) bool`：Windows 用 `MessageBoxW`；macOS 用 `osascript display dialog` | ⬜ |
| U3.4 | 圖示 | `assets/icon.ico`（Windows）與 `assets/icon.png`（macOS），`go:embed` 打包；先放佔位圖示，**正式圖示待提供** | ⬜ |
| U3.V | 驗證 | 手動測試，見第 6 節 | ⬜ |

系統匣選單：

| 項目 | 行為 |
|------|------|
| 狀態列（不可點，動態更新） | 例：「已綁定・佇列 12 筆・上次上傳 10:32」；tooltip 同步 |
| 開啟狀態頁／開始綁定 | 預設瀏覽器開啟 U2 網頁；尚未綁定（閒置）時同時呼叫 `StartBinding()` |
| 立即上傳 | `FlushNow()` |
| 解綁… | 確認框 → `Agent.Unbind` |
| 重新綁定 | 僅「已撤銷」時顯示 → `Rebind()` 並開啟綁定頁 |
| 開機自動啟動（勾選框） | 見 U4.4 |
| 結束 | 取消 ctx，觸發關機前上傳 |

### U4 — Windows 平台補齊（`internal/platform`）

| # | 子項 | 說明 | 狀態 |
|---|------|------|------|
| U4.1 | 關機／登出 hook | `session_windows.go`：在固定 OS 執行緒（`runtime.LockOSThread`）建立**隱藏的一般視窗**（不可用 `HWND_MESSAGE`，它收不到廣播的 `WM_QUERYENDSESSION`）。收到 `WM_QUERYENDSESSION` → `ShutdownBlockReasonCreate`（關機畫面顯示「正在上傳能耗資料」）；收到 `WM_ENDSESSION` → 取消 ctx，**等關機前上傳完成（上限為現有 5 秒 `shutdownTimeout`）才返回**，因為返回後行程即被終止 | ⬜ |
| U4.2 | 喚醒後補送 | 同一視窗監聽 `WM_POWERBROADCAST`／`PBT_APMRESUMEAUTOMATIC` → `FlushNow()` | ⬜ |
| U4.3 | 單一實例 | named mutex `Local\EcoAgent`；第二份啟動時提示「已在執行」後退出。macOS 以 lock file 實作 | ⬜ |
| U4.4 | 開機自啟 | 寫入／刪除 `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`（`x/sys/windows/registry`，不需系統管理員權限）。macOS LaunchAgent 先放空殼，標 `// TODO(platform)` | ⬜ |
| U4.5 | log 檔 | `lumberjack` 寫到 `%AppData%\eco-agent\logs\agent.log`（macOS：`~/Library/Logs/eco-agent/`），單檔 10MB、保留 3 份；`-headless` 時仍印主控台 | ⬜ |
| U4.6 | 無視窗建置 | 正式版 `go build -ldflags "-H windowsgui" ./cmd/eco-agent`；README 補建置說明 | ⬜ |
| U4.7 | 綁定提醒 toast | 以 `go-toast/v2`（純 Go、經 COM 呼叫 WinRT，不另開 PowerShell）發送。每次進入閒置（關頁）都重新排一輪：立即發 1 次、1 小時後再發 1 次，之後不再發；期間若已綁定或重新進入綁定中則取消排程，不會疊加。**冷卻**：記錄上次實際發出提醒的時間，距今未滿 10 分鐘則略過「立即」那次（1 小時後那次照排）。1 小時間隔與 10 分鐘冷卻皆做成 `config` 常數，測試時可縮短。toast 設 `activationType="protocol"`、`launch` 為綁定頁網址，點擊即由瀏覽器開啟，頁面開啟後自動恢復換碼（U2.6）。Windows 勿擾模式會自動暫緩通知，不需自行處理 | ⬜ |
| U4.8 | AppUserModelID 註冊 | 寫入 `HKCU\Software\Classes\AppUserModelId\EcoSensing.EcoAgent`（`DisplayName`、`IconUri`），讓 toast 顯示 Eco-Agent 名稱與圖示而非 PowerShell；與 U4.4 共用登錄檔程式，不需系統管理員權限 | ⬜ |
| U4.9 | macOS 提醒（降級版） | 正式通知 API 需簽章 `.app` 程式包，暫以 `osascript display notification` 純提示（點擊無法開綁定頁，提示員工點選單列圖示），標 `// TODO(platform)` | ⬜ |
| U4.V | 驗證 | 手動測試，見第 6 節 | ⬜ |

## 5. 新增依賴

| 套件 | 用途 |
|------|------|
| `fyne.io/systray` | 系統匣 |
| `github.com/skip2/go-qrcode` | 產生綁定 QR PNG |
| `gopkg.in/natefinch/lumberjack.v2` | log 檔輪替 |
| `git.sr.ht/~jackmordaunt/go-toast/v2` | Windows toast 綁定提醒（U4.7） |

## 6. 驗證流程（Windows，testing profile + 本機 mock 後端）

1. 首次啟動（未綁定）→ 自動開啟 QR 頁 → 模擬核銷 → 頁面變成「已綁定」。
1a. 首次啟動後不掃碼：綁定碼逾時 → 立即換新碼（QR 無空窗）。關閉綁定頁 → 約 15 秒後系統匣顯示「尚未綁定」、立即跳出 1 次 toast、mock 後端不再收到①③請求 → 1 小時後（測試時可縮短）再跳 1 次 → 之後不再跳。點 toast → 瀏覽器開啟綁定頁並恢復換碼。
1b. 冷卻：關頁（跳通知）→ 5 分鐘內重新開頁再關 → **不**立即跳通知，只在 1 小時後跳 1 次；超過 10 分鐘後再關頁 → 立即跳通知。
2. 系統匣狀態列隨佇列筆數更新；按「立即上傳」→ mock 端點收到資料。
3. 後端離線時按「解綁…」→ 確認框顯示的殘留筆數正確；取消 → 維持綁定、資料仍在；同意 → 佇列清空、回到綁定頁。
4. mock 回 403 → 系統匣顯示「已撤銷」，程式不結束；按「重新綁定」→ 回到 QR 頁。
5. 勾選「開機自動啟動」→ 登錄檔 `Run` 有寫入；取消勾選 → 已刪除。
6. 再啟動第二份 → 被擋下並提示「已在執行」。
7. 登出或關機 → log 出現 `reason=shutdown` 的上傳紀錄。
8. 睡眠後喚醒 → log 出現喚醒補送紀錄。
9. `-H windowsgui` 建置版執行時無主控台視窗，log 寫入 `%AppData%\eco-agent\logs\agent.log`。

## 7. 不在本次範圍

- macOS 的 `NSWorkspaceWillPowerOffNotification` 關機 hook（現仍靠 SIGTERM）與 LaunchAgent 實作：先留 `TODO(platform)`。
- 安裝程式（MSI／pkg）與程式碼簽章。
- 正式圖示與視覺設計（待提供素材）。
