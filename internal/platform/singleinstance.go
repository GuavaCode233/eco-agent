package platform

import "errors"

// ErrAlreadyRunning 表示本機已有另一個 Eco-Agent 在執行（docs/Eco-Agent_系統匣UI規劃.md U4.3）。
var ErrAlreadyRunning = errors.New("platform: another Eco-Agent instance is already running")

// AcquireSingleInstance 取得「本機只能有一個 Eco-Agent」的鎖；已有其他實例時回傳
// ErrAlreadyRunning。release 於行程結束前呼叫（行程結束時作業系統也會自動釋放）。
//
// 兩個實例同時執行會同時寫同一個 SQLite 佇列與金鑰庫，路徑 A 也會重複採樣，因此所有模式
// （系統匣、headless、-unbind）都必須先取得此鎖。
func AcquireSingleInstance() (release func(), err error) { return acquireSingleInstance() }
