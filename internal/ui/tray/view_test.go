package tray

import (
	"errors"
	"testing"
	"time"

	"eco-agent/internal/agent"
	"eco-agent/internal/uploader"
)

func TestViewOf(t *testing.T) {
	now := time.Date(2026, 11, 27, 15, 0, 0, 0, time.Local)
	today := time.Date(2026, 11, 27, 10, 32, 0, 0, time.Local)
	yesterday := time.Date(2026, 11, 26, 9, 5, 0, 0, time.Local)

	cases := []struct {
		name string
		st   agent.Status
		want view
	}{
		{
			name: "bound, never uploaded",
			st:   agent.Status{State: agent.StateBound, QueueCount: 3},
			want: view{Status: "已綁定・待上傳 3 筆", OpenTitle: "開啟狀態頁", CanFlush: true, CanUnbind: true},
		},
		{
			name: "bound, uploaded today",
			st: agent.Status{State: agent.StateBound, QueueCount: 0,
				LastUpload: &uploader.Result{At: today, Sent: 2, Uploaded: 2}},
			want: view{Status: "已綁定・待上傳 0 筆・上次上傳 10:32", OpenTitle: "開啟狀態頁", CanFlush: true, CanUnbind: true},
		},
		{
			name: "bound, last upload failed yesterday",
			st: agent.Status{State: agent.StateBound, QueueCount: 5,
				LastUpload: &uploader.Result{At: yesterday, Sent: 5, Uploaded: 0}},
			want: view{Status: "已綁定・待上傳 5 筆・上次上傳 11/26 09:05 未完成", OpenTitle: "開啟狀態頁", CanFlush: true, CanUnbind: true},
		},
		{
			name: "binding with QR",
			st:   agent.Status{State: agent.StateBinding, BindingURI: "ecosensing://bind?code=X"},
			want: view{Status: "尚未綁定・等待掃碼", Warn: true, OpenTitle: "開始綁定（顯示 QR Code）"},
		},
		{
			name: "binding, backend unreachable",
			st:   agent.Status{State: agent.StateBinding, BindingError: errors.New("dial")},
			want: view{Status: "尚未綁定・無法連線伺服器", Warn: true, OpenTitle: "開始綁定（顯示 QR Code）"},
		},
		{
			name: "binding, requesting code",
			st:   agent.Status{State: agent.StateBinding},
			want: view{Status: "尚未綁定・正在產生 QR Code", Warn: true, OpenTitle: "開始綁定（顯示 QR Code）"},
		},
		{
			name: "idle",
			st:   agent.Status{State: agent.StateIdle},
			want: view{Status: "尚未綁定", Warn: true, OpenTitle: "開始綁定（顯示 QR Code）"},
		},
		{
			name: "revoked with discarded",
			st:   agent.Status{State: agent.StateRevoked, DiscardedOnRevoke: 4},
			want: view{Status: "綁定已撤銷・已捨棄 4 筆", Warn: true, OpenTitle: "開啟狀態頁", ShowRebind: true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := viewOf(c.st, now)
			c.want.Tooltip = "Eco-Agent：" + c.want.Status
			if got != c.want {
				t.Fatalf("viewOf =\n  %+v\nwant\n  %+v", got, c.want)
			}
		})
	}
}
