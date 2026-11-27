package remind

import (
	"sort"
	"sync"
	"testing"
	"time"
)

// fakeClock 以手動推進的時間驅動 afterFunc。
type fakeClock struct {
	mu     sync.Mutex
	t      time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	at      time.Time
	f       func()
	stopped bool
}

func (t *fakeTimer) Stop() bool { was := !t.stopped; t.stopped = true; return was }

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *fakeClock) afterFunc(d time.Duration, f func()) stopper {
	c.mu.Lock()
	defer c.mu.Unlock()
	tm := &fakeTimer{at: c.t.Add(d), f: f}
	c.timers = append(c.timers, tm)
	return tm
}

// advance 推進時間並依序觸發到期的計時器。
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	var due []*fakeTimer
	rest := c.timers[:0]
	for _, tm := range c.timers {
		if !tm.stopped && !tm.at.After(c.t) {
			due = append(due, tm)
		} else if !tm.stopped {
			rest = append(rest, tm)
		}
	}
	c.timers = rest
	c.mu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].at.Before(due[j].at) })
	for _, tm := range due {
		tm.f()
	}
}

type harness struct {
	clock *fakeClock
	r     *Reminder
	sent  []time.Time
	idle  bool
}

func newHarness() *harness {
	h := &harness{clock: &fakeClock{t: time.Date(2026, 11, 27, 9, 0, 0, 0, time.UTC)}, idle: true}
	h.r = New(
		func() error { h.sent = append(h.sent, h.clock.now()); return nil },
		func() bool { return h.idle },
		time.Hour, 10*time.Minute,
		withClock(h.clock.now, h.clock.afterFunc),
	)
	return h
}

func (h *harness) expectSent(t *testing.T, n int) {
	t.Helper()
	if len(h.sent) != n {
		t.Fatalf("sent %d reminders (%v), want %d", len(h.sent), h.sent, n)
	}
}

// 關頁：立即 1 次，1 小時後 1 次，之後不再提醒。
func TestImmediateThenOneHourThenStop(t *testing.T) {
	h := newHarness()
	h.r.PageClosed()
	h.expectSent(t, 1)
	h.clock.advance(59 * time.Minute)
	h.expectSent(t, 1)
	h.clock.advance(time.Minute)
	h.expectSent(t, 2)
	h.clock.advance(24 * time.Hour)
	h.expectSent(t, 2)
}

// 冷卻期內再次關頁：略過立即那次，只保留 1 小時後那次；上一輪的延遲提醒被取消、不疊加。
func TestCooldownSkipsImmediateAndRoundsDoNotStack(t *testing.T) {
	h := newHarness()
	h.r.PageClosed() // 09:00 立即
	h.expectSent(t, 1)

	h.clock.advance(5 * time.Minute)
	h.r.PageClosed() // 09:05，冷卻中 → 不立即；09:00 那輪的 10:00 被取消
	h.expectSent(t, 1)

	h.clock.advance(55 * time.Minute) // 10:00：舊一輪不應觸發
	h.expectSent(t, 1)
	h.clock.advance(5 * time.Minute) // 10:05：新一輪觸發
	h.expectSent(t, 2)
}

// 超過冷卻後再關頁：立即提醒。
func TestAfterCooldownSendsImmediately(t *testing.T) {
	h := newHarness()
	h.r.PageClosed()
	h.clock.advance(11 * time.Minute)
	h.r.PageClosed()
	h.expectSent(t, 2)
}

// 延遲提醒觸發時已不在閒置（已綁定或重新開頁）→ 不發送。
func TestDelayedSkippedWhenNoLongerIdle(t *testing.T) {
	h := newHarness()
	h.r.PageClosed()
	h.idle = false
	h.clock.advance(time.Hour)
	h.expectSent(t, 1)
}

// 延遲提醒計入冷卻：剛發完延遲提醒後立刻關頁，不會連發。
func TestDelayedReminderCountsForCooldown(t *testing.T) {
	h := newHarness()
	h.r.PageClosed()
	h.clock.advance(time.Hour) // 延遲提醒
	h.expectSent(t, 2)
	h.clock.advance(time.Minute)
	h.r.PageClosed()
	h.expectSent(t, 2)
}

func TestStopCancelsPending(t *testing.T) {
	h := newHarness()
	h.r.PageClosed()
	h.r.Stop()
	h.clock.advance(2 * time.Hour)
	h.expectSent(t, 1)
}
