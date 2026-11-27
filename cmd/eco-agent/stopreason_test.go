package main

import (
	"sync"
	"testing"
)

// 先記先贏：登出時工作階段視窗與系統匣視窗都會收到 WM_ENDSESSION，只保留最先的原因。
func TestStopReasonFirstWins(t *testing.T) {
	var r stopReason
	if got := r.String(); got != stopReasonUnknown {
		t.Fatalf("empty reason = %q, want %q", got, stopReasonUnknown)
	}
	r.Set(stopReasonSessionEnd)
	r.Set("session_end_tray")
	r.Set(stopReasonTerminate)
	if got := r.String(); got != stopReasonSessionEnd {
		t.Fatalf("reason = %q, want %q", got, stopReasonSessionEnd)
	}
}

func TestStopReasonConcurrentSet(t *testing.T) {
	var r stopReason
	var wg sync.WaitGroup
	for _, s := range []string{"a", "b", "c", "d"} {
		wg.Add(1)
		go func(s string) { defer wg.Done(); r.Set(s) }(s)
	}
	wg.Wait()
	switch r.String() {
	case "a", "b", "c", "d":
	default:
		t.Fatalf("reason = %q, want one of the set values", r.String())
	}
}
