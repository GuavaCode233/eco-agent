// Package remind 排程「尚未綁定」提醒通知（docs/Eco-Agent_系統匣UI規劃.md §2、U4.7）。
//
// 規則：
//   - 每次綁定頁關閉（agent 進入閒置）都重新開始一輪：立即提醒 1 次，Delay 後再提醒 1 次，之後
//     不再提醒（直到下次關頁）。
//   - 冷卻：距上次實際發出提醒未滿 Cooldown 時，略過「立即」那次（Delay 後那次照排）。
//   - 新一輪開始時取消上一輪尚未觸發的提醒，不會疊加。
//   - 延遲提醒觸發時若 agent 已不在閒置（已綁定、或員工已重新開頁），則不發送。
package remind

import (
	"sync"
	"time"
)

// Reminder 是提醒排程器。並行安全。
type Reminder struct {
	notify   func() error // 實際送出通知
	isIdle   func() bool  // agent 是否仍在「尚未綁定（閒置）」
	delay    time.Duration
	cooldown time.Duration
	onError  func(error)

	now       func() time.Time
	afterFunc func(d time.Duration, f func()) stopper

	mu       sync.Mutex
	lastSent time.Time
	pending  stopper
	round    int // 每輪遞增；延遲提醒觸發時比對，避免舊一輪在 Stop 競態下仍送出
}

type stopper interface{ Stop() bool }

// Option 客製化 Reminder。
type Option func(*Reminder)

// WithErrorHandler 設定送出失敗時的處理（例如記 log）。
func WithErrorHandler(f func(error)) Option { return func(r *Reminder) { r.onError = f } }

// withClock 注入時鐘與計時器（測試用）。
func withClock(now func() time.Time, afterFunc func(time.Duration, func()) stopper) Option {
	return func(r *Reminder) { r.now, r.afterFunc = now, afterFunc }
}

// New 建立提醒排程器。
func New(notify func() error, isIdle func() bool, delay, cooldown time.Duration, opts ...Option) *Reminder {
	r := &Reminder{
		notify:   notify,
		isIdle:   isIdle,
		delay:    delay,
		cooldown: cooldown,
		onError:  func(error) {},
		now:      time.Now,
		afterFunc: func(d time.Duration, f func()) stopper {
			return time.AfterFunc(d, f)
		},
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// PageClosed 於綁定頁關閉、agent 進入閒置時呼叫（web.WithOnPageClosed）。
func (r *Reminder) PageClosed() {
	r.mu.Lock()
	if r.pending != nil {
		r.pending.Stop()
	}
	r.round++
	round := r.round
	sendNow := r.lastSent.IsZero() || r.now().Sub(r.lastSent) >= r.cooldown
	if sendNow {
		r.lastSent = r.now()
	}
	r.pending = r.afterFunc(r.delay, func() { r.fireDelayed(round) })
	r.mu.Unlock()

	if sendNow {
		r.send()
	}
}

func (r *Reminder) fireDelayed(round int) {
	r.mu.Lock()
	if round != r.round {
		r.mu.Unlock()
		return
	}
	r.pending = nil
	if !r.isIdle() {
		r.mu.Unlock()
		return
	}
	r.lastSent = r.now()
	r.mu.Unlock()
	r.send()
}

// Stop 取消尚未觸發的提醒（程式結束時呼叫）。
func (r *Reminder) Stop() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.round++
	if r.pending != nil {
		r.pending.Stop()
		r.pending = nil
	}
}

func (r *Reminder) send() {
	if err := r.notify(); err != nil {
		r.onError(err)
	}
}
