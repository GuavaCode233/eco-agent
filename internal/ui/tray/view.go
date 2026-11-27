package tray

import (
	"fmt"
	"time"

	"eco-agent/internal/agent"
)

// view 是系統匣在某個 agent 狀態下應呈現的樣子（純函式產出，方便測試）。
type view struct {
	Status     string // 狀態列（不可點）文字
	Tooltip    string
	Warn       bool   // true 時使用「需要注意」圖示（未綁定／已撤銷）
	OpenTitle  string // 「開啟狀態頁／開始綁定」項目文字
	CanFlush   bool   // 「立即上傳」可用
	CanUnbind  bool   // 「解綁…」可用
	ShowRebind bool   // 「重新綁定」顯示
}

// viewOf 依 agent 狀態產出系統匣呈現；now 用於判斷「今天」以決定時間顯示格式。
func viewOf(st agent.Status, now time.Time) view {
	v := view{OpenTitle: "開啟狀態頁"}
	switch st.State {
	case agent.StateBound:
		v.Status = fmt.Sprintf("已綁定・待上傳 %d 筆", st.QueueCount)
		if r := st.LastUpload; r != nil {
			result := ""
			if !r.OK() {
				result = " 未完成"
			}
			v.Status += "・上次上傳 " + shortTime(r.At, now) + result
		}
		v.CanFlush = true
		v.CanUnbind = true
	case agent.StateBinding:
		v.Warn = true
		v.OpenTitle = "開始綁定（顯示 QR Code）"
		switch {
		case st.BindingURI != "":
			v.Status = "尚未綁定・等待掃碼"
		case st.BindingError != nil:
			v.Status = "尚未綁定・無法連線伺服器"
		default:
			v.Status = "尚未綁定・正在產生 QR Code"
		}
	case agent.StateIdle:
		v.Warn = true
		v.OpenTitle = "開始綁定（顯示 QR Code）"
		v.Status = "尚未綁定"
	case agent.StateRevoked:
		v.Warn = true
		v.Status = "綁定已撤銷"
		if st.DiscardedOnRevoke > 0 {
			v.Status += fmt.Sprintf("・已捨棄 %d 筆", st.DiscardedOnRevoke)
		}
		v.ShowRebind = true
	default:
		v.Warn = true
		v.Status = "啟動中…"
	}
	v.Tooltip = "Eco-Agent：" + v.Status
	return v
}

// shortTime 今天只顯示時:分，其他日期加上月/日。
func shortTime(t, now time.Time) string {
	t, now = t.Local(), now.Local()
	if t.Year() == now.Year() && t.YearDay() == now.YearDay() {
		return t.Format("15:04")
	}
	return t.Format("1/2 15:04")
}
