package platform

// Confirm 顯示「是／否」確認框並回傳使用者是否選擇「是」（系統匣解綁確認，
// docs/Eco-Agent_系統匣UI規劃.md U3.3）。預設按鈕為「否」：解綁可能捨棄資料，誤按 Enter
// 不應直接執行。不支援的平台一律回 false（寧可不執行，也不默默同意）。
func Confirm(title, message string) bool { return confirm(title, message) }
