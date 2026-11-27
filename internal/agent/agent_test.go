package agent

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"eco-agent/internal/enroll"
	"eco-agent/internal/queue"
	"eco-agent/internal/uploader"
)

// --- fakes ---

// fakeBinder 模擬 enroll.Enroller。bindFn 於未綁定時被 EnsureBound 呼叫；回 nil 即綁定成功。
type fakeBinder struct {
	mu          sync.Mutex
	bound       bool
	bindFn      func(ctx context.Context, attempt int) error
	attempts    int32
	unbinds     int32
	resetRevoke int32
}

func (b *fakeBinder) EnsureBound(ctx context.Context) error {
	b.mu.Lock()
	if b.bound {
		b.mu.Unlock()
		return nil
	}
	fn := b.bindFn
	b.mu.Unlock()
	n := int(atomic.AddInt32(&b.attempts, 1))
	if err := fn(ctx, n); err != nil {
		return err
	}
	b.mu.Lock()
	b.bound = true
	b.mu.Unlock()
	return nil
}

func (b *fakeBinder) IsBound() (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.bound, nil
}

func (b *fakeBinder) Unbind(context.Context) error {
	atomic.AddInt32(&b.unbinds, 1)
	b.setBound(false)
	return nil
}

func (b *fakeBinder) ResetRevoked() { atomic.AddInt32(&b.resetRevoke, 1) }

func (b *fakeBinder) setBound(v bool) {
	b.mu.Lock()
	b.bound = v
	b.mu.Unlock()
}

// fakeUploader：Run 阻塞到 ctx 取消或 revoke() 被呼叫（回 ErrStopped）；Flush 執行 flushFn。
type fakeUploader struct {
	revokeCh chan struct{}
	flushFn  func(ctx context.Context) error
	flushes  int32
}

func (u *fakeUploader) Run(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-u.revokeCh:
		return uploader.ErrStopped
	}
}

func (u *fakeUploader) Flush(ctx context.Context, _ uploader.Reason) error {
	atomic.AddInt32(&u.flushes, 1)
	if u.flushFn != nil {
		return u.flushFn(ctx)
	}
	return nil
}

func (u *fakeUploader) LastResult() (uploader.Result, bool) { return uploader.Result{}, false }

// fakeSensors 記錄啟動次數與目前執行中的 goroutine 數。
type fakeSensors struct {
	starts  int32
	running int32
}

func (f *fakeSensors) start(ctx context.Context, wg *sync.WaitGroup) map[queue.PathType]bool {
	atomic.AddInt32(&f.starts, 1)
	atomic.AddInt32(&f.running, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		<-ctx.Done()
		atomic.AddInt32(&f.running, -1)
	}()
	return map[queue.PathType]bool{queue.PathComputer: true}
}

type harness struct {
	a       *Agent
	b       *fakeBinder
	q       *queue.Queue
	sensors *fakeSensors

	mu  sync.Mutex
	ups []*fakeUploader
	// flushFn 套用到之後建立的每個 fakeUploader。
	flushFn func(ctx context.Context) error
}

func newHarness(t *testing.T, bindFn func(ctx context.Context, attempt int) error, opts ...Option) *harness {
	t.Helper()
	q, err := queue.Open(context.Background(), filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { q.Close() })
	h := &harness{b: &fakeBinder{bindFn: bindFn}, q: q, sensors: &fakeSensors{}}
	newUp := func() Uploader {
		h.mu.Lock()
		defer h.mu.Unlock()
		u := &fakeUploader{revokeCh: make(chan struct{}), flushFn: h.flushFn}
		h.ups = append(h.ups, u)
		return u
	}
	base := []Option{
		WithLogger(slog.New(slog.NewTextHandler(io.Discard, nil))),
		WithRetryInterval(time.Hour),
		WithSessionCheckInterval(10 * time.Millisecond),
	}
	h.a = New(h.b, q, newUp, h.sensors.start, append(base, opts...)...)
	return h
}

// run 於背景執行 Agent.Run，測試結束時取消並等待其返回。
func (h *harness) run(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.a.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Agent.Run did not return after cancel")
		}
	})
}

func (h *harness) uploaders() []*fakeUploader {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]*fakeUploader(nil), h.ups...)
}

func (h *harness) enqueue(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		e := queue.Event{
			ID:          queue.EventID("idt", "2026-11-27", queue.PathType([]string{"computer", "drive", "printer"}[i%3])),
			PathType:    queue.PathComputer,
			UsageDate:   "2026-11-27",
			Payload:     map[string]any{"v": i},
			CollectedAt: time.Now(),
		}
		if err := h.q.Enqueue(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for: %s", what)
}

func bindOK(context.Context, int) error { return nil }

// blockUntilCancel 模擬「綁定碼有效期間持續輪詢，直到被取消」。
func blockUntilCancel(ctx context.Context, _ int) error { <-ctx.Done(); return ctx.Err() }

func (h *harness) waitState(t *testing.T, s State) {
	t.Helper()
	eventually(t, "state "+string(s), func() bool { return h.a.State() == s })
}

// --- 綁定重試節奏 ---

// 綁定碼逾時（員工沒掃）應立即換新碼，不等 retryInterval（此處設為 1 小時）。
func TestBindingCodeExpiredRetriesImmediately(t *testing.T) {
	h := newHarness(t, func(_ context.Context, n int) error {
		if n < 3 {
			return enroll.ErrBindingCodeExpired
		}
		return nil
	})
	h.run(t)
	h.waitState(t, StateBound)
	if n := atomic.LoadInt32(&h.b.attempts); n != 3 {
		t.Fatalf("attempts = %d, want 3", n)
	}
}

// 後端錯誤應等待 retryInterval；StartBinding 可提前喚醒重試。
func TestBackendErrorWaitsRetryInterval(t *testing.T) {
	h := newHarness(t, func(_ context.Context, n int) error {
		if n == 1 {
			return errors.New("connection refused")
		}
		return nil
	})
	h.run(t)
	eventually(t, "first attempt", func() bool { return atomic.LoadInt32(&h.b.attempts) == 1 })
	// 頁面需要的回饋：最近一次錯誤與下次重試時間（約 1 小時後）。
	eventually(t, "binding error exposed", func() bool {
		st := h.a.Status(context.Background())
		return st.BindingError != nil && time.Until(st.BindingRetryAt) > 59*time.Minute
	})
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&h.b.attempts); n != 1 {
		t.Fatalf("attempts during retry wait = %d, want 1", n)
	}
	h.a.StartBinding()
	h.waitState(t, StateBound)
	if st := h.a.Status(context.Background()); st.BindingError != nil || !st.BindingRetryAt.IsZero() {
		t.Fatalf("binding error not cleared after bound: %+v", st)
	}
}

// PauseBinding（綁定頁關閉）應取消進行中的綁定並停止打後端；StartBinding 恢復。
func TestPauseBindingStopsAttemptsUntilStart(t *testing.T) {
	h := newHarness(t, blockUntilCancel)
	h.run(t)
	eventually(t, "binding attempt", func() bool { return atomic.LoadInt32(&h.b.attempts) == 1 })
	h.a.OnBindingCode("ecosensing://bind?code=X", time.Now().Add(time.Minute))

	if !h.a.PauseBinding() {
		t.Fatal("PauseBinding returned false in binding state")
	}
	h.waitState(t, StateIdle)
	if st := h.a.Status(context.Background()); st.BindingURI != "" {
		t.Fatalf("BindingURI in idle = %q, want empty", st.BindingURI)
	}
	time.Sleep(50 * time.Millisecond)
	if n := atomic.LoadInt32(&h.b.attempts); n != 1 {
		t.Fatalf("attempts while idle = %d, want 1 (no backend calls)", n)
	}
	if h.a.PauseBinding() {
		t.Fatal("PauseBinding in idle should be a no-op")
	}

	h.a.StartBinding()
	eventually(t, "attempt after StartBinding", func() bool { return atomic.LoadInt32(&h.b.attempts) == 2 })
	if s := h.a.State(); s != StateBinding {
		t.Fatalf("state = %s, want binding", s)
	}
}

// --- 已綁定階段 ---

func TestBoundStartsSensorsAndUploader(t *testing.T) {
	h := newHarness(t, bindOK)
	h.run(t)
	h.waitState(t, StateBound)
	eventually(t, "sensors running", func() bool { return atomic.LoadInt32(&h.sensors.running) == 1 })
	st := h.a.Status(context.Background())
	if !st.Paths[queue.PathComputer] {
		t.Fatalf("Status.Paths = %v, want computer enabled", st.Paths)
	}
	if err := h.a.FlushNow(context.Background()); err != nil {
		t.Fatalf("FlushNow: %v", err)
	}
}

// 撤銷 → 已撤銷（不結束）→ Rebind → ResetRevoked 並以新 uploader 重新進入已綁定。
func TestRevokedThenRebind(t *testing.T) {
	h := newHarness(t, bindOK)
	h.run(t)
	h.waitState(t, StateBound)

	h.enqueue(t, 2) // 撤銷前尚未送出的殘留

	h.b.setBound(false) // 撤銷時 uploader 會清憑證
	close(h.uploaders()[0].revokeCh)
	h.waitState(t, StateRevoked)
	// 與解綁同一規則：撤銷時捨棄殘留，避免 Rebind 後以新 id_token 送出（換人時歸錯戶）。
	eventually(t, "leftover discarded", func() bool {
		return h.a.Status(context.Background()).DiscardedOnRevoke == 2
	})
	if n, _ := h.q.Count(context.Background()); n != 0 {
		t.Fatalf("queue count after revoke = %d, want 0", n)
	}
	eventually(t, "sensors stopped", func() bool { return atomic.LoadInt32(&h.sensors.running) == 0 })
	if err := h.a.FlushNow(context.Background()); !errors.Is(err, ErrNotBound) {
		t.Fatalf("FlushNow while revoked = %v, want ErrNotBound", err)
	}

	h.a.Rebind()
	h.waitState(t, StateBound)
	if n := atomic.LoadInt32(&h.b.resetRevoke); n != 1 {
		t.Fatalf("ResetRevoked calls = %d, want 1", n)
	}
	if d := h.a.Status(context.Background()).DiscardedOnRevoke; d != 0 {
		t.Fatalf("DiscardedOnRevoke after Rebind = %d, want 0", d)
	}
	if n := len(h.uploaders()); n != 2 {
		t.Fatalf("uploaders created = %d, want 2 (fresh one after rebind)", n)
	}
}

// Refresh Token 失效（enroll 清了本機憑證）→ 結束 session 回到綁定。
func TestCredentialsGoneReturnsToBinding(t *testing.T) {
	h := newHarness(t, func(ctx context.Context, n int) error {
		if n == 1 {
			return nil
		}
		return blockUntilCancel(ctx, n)
	})
	h.run(t)
	h.waitState(t, StateBound)
	h.b.setBound(false)
	h.waitState(t, StateBinding)
	eventually(t, "sensors stopped", func() bool { return atomic.LoadInt32(&h.sensors.running) == 0 })
}

// --- 解綁（先送再丟）---

// 上傳後佇列清空 → 不詢問、直接解綁並回到綁定中。
func TestUnbindAllUploaded(t *testing.T) {
	h := newHarness(t, func(ctx context.Context, n int) error {
		if n == 1 {
			return nil
		}
		return blockUntilCancel(ctx, n)
	})
	h.flushFn = func(ctx context.Context) error { _, err := h.q.Purge(ctx); return err } // 模擬全數 200
	h.run(t)
	h.waitState(t, StateBound)
	h.enqueue(t, 2)

	asked := false
	if err := h.a.Unbind(context.Background(), func(int) bool { asked = true; return true }); err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if asked {
		t.Fatal("confirm should not be called when queue is empty after flush")
	}
	if n := atomic.LoadInt32(&h.b.unbinds); n != 1 {
		t.Fatalf("binder.Unbind calls = %d, want 1", n)
	}
	h.waitState(t, StateBinding)
	eventually(t, "sensors stopped", func() bool { return atomic.LoadInt32(&h.sensors.running) == 0 })
}

// 有殘留且同意捨棄 → 清空佇列並解綁；詢問期間三路徑已停止。
func TestUnbindLeftoverConfirmed(t *testing.T) {
	h := newHarness(t, func(ctx context.Context, n int) error {
		if n == 1 {
			return nil
		}
		return blockUntilCancel(ctx, n)
	})
	h.run(t)
	h.waitState(t, StateBound)
	h.enqueue(t, 3)

	var leftover int
	var runningDuringConfirm int32 = -1
	err := h.a.Unbind(context.Background(), func(n int) bool {
		leftover = n
		runningDuringConfirm = atomic.LoadInt32(&h.sensors.running)
		return true
	})
	if err != nil {
		t.Fatalf("Unbind: %v", err)
	}
	if leftover != 3 {
		t.Fatalf("confirm leftover = %d, want 3", leftover)
	}
	if runningDuringConfirm != 0 {
		t.Fatalf("sensors running during confirm = %d, want 0", runningDuringConfirm)
	}
	if n, _ := h.q.Count(context.Background()); n != 0 {
		t.Fatalf("queue count after confirmed unbind = %d, want 0", n)
	}
	if got := atomic.LoadInt32(&h.uploaders()[0].flushes); got != 1 {
		t.Fatalf("flushes = %d, want 1 (flush before discarding)", got)
	}
	h.waitState(t, StateBinding)
}

// 有殘留但取消 → 不解綁、資料保留、三路徑恢復、維持已綁定。
func TestUnbindLeftoverCancelled(t *testing.T) {
	h := newHarness(t, bindOK)
	h.run(t)
	h.waitState(t, StateBound)
	h.enqueue(t, 2)

	err := h.a.Unbind(context.Background(), func(int) bool { return false })
	if !errors.Is(err, ErrUnbindCancelled) {
		t.Fatalf("Unbind = %v, want ErrUnbindCancelled", err)
	}
	if n, _ := h.q.Count(context.Background()); n != 2 {
		t.Fatalf("queue count = %d, want 2 (kept)", n)
	}
	if n := atomic.LoadInt32(&h.b.unbinds); n != 0 {
		t.Fatalf("binder.Unbind calls = %d, want 0", n)
	}
	if s := h.a.State(); s != StateBound {
		t.Fatalf("state = %s, want bound", s)
	}
	eventually(t, "sensors restarted", func() bool {
		return atomic.LoadInt32(&h.sensors.running) == 1 && atomic.LoadInt32(&h.sensors.starts) == 2
	})
}

// 綁定中不可解綁（EnsureBound 持有 Enroller 鎖，且本無綁定可解）。
func TestUnbindWhileBindingRejected(t *testing.T) {
	h := newHarness(t, blockUntilCancel)
	h.run(t)
	eventually(t, "binding attempt", func() bool { return atomic.LoadInt32(&h.b.attempts) == 1 })
	if err := h.a.Unbind(context.Background(), nil); !errors.Is(err, ErrNotBound) {
		t.Fatalf("Unbind while binding = %v, want ErrNotBound", err)
	}
}

// Run 未執行時（CLI -unbind）也能解綁：以臨時 uploader 先送，殘留由 confirm 決定。
func TestUnbindWithoutRun(t *testing.T) {
	h := newHarness(t, bindOK)
	h.b.setBound(true)
	h.enqueue(t, 1)

	if err := h.a.Unbind(context.Background(), func(int) bool { return false }); !errors.Is(err, ErrUnbindCancelled) {
		t.Fatalf("Unbind (declined) = %v, want ErrUnbindCancelled", err)
	}
	if err := h.a.Unbind(context.Background(), func(int) bool { return true }); err != nil {
		t.Fatalf("Unbind (forced): %v", err)
	}
	if bound, _ := h.b.IsBound(); bound {
		t.Fatal("still bound after CLI unbind")
	}
	if n := len(h.uploaders()); n != 2 {
		t.Fatalf("temporary uploaders = %d, want 2", n)
	}
}
