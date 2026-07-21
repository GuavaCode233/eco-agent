package drive

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"eco-agent/internal/queue"
)

// ── 測試替身 ──

// fakeSampler 回傳可控的 Quota，並計數 StorageQuota 呼叫次數以斷言「是否有查」。
type fakeSampler struct {
	quota Quota
	err   error
	calls int
}

func (f *fakeSampler) StorageQuota(ctx context.Context) (Quota, error) {
	f.calls++
	if f.err != nil {
		return Quota{}, f.err
	}
	return f.quota, nil
}

type fakeEnroll struct{ token string }

func (f fakeEnroll) IDToken() (string, error) { return f.token, nil }

const testToken = "test-idtoken-C"

func newTestQueue(t *testing.T) *queue.Queue {
	t.Helper()
	q, err := queue.Open(context.Background(), filepath.Join(t.TempDir(), "q.db"))
	if err != nil {
		t.Fatalf("queue.Open: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

// clock 是可推進的假時鐘。
type clock struct{ t time.Time }

func (c *clock) now() time.Time      { return c.t }
func (c *clock) add(d time.Duration) { c.t = c.t.Add(d) }

func newSensor(t *testing.T, q *queue.Queue, sampler QuotaSampler, clk *clock) *Sensor {
	return NewSensor(q, fakeEnroll{testToken}, sampler,
		5*time.Second, // checkInterval（測試不實際掛 ticker，直接呼叫 checkAndSample）
		24*time.Hour,  // quotaInterval
		WithSensorNow(clk.now),
	)
}

// TestColdStart：無時間戳（冷啟動）→ 首次巡檢即查、入列、寫入時間戳（2.3）。
func TestColdStart(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	sampler := &fakeSampler{quota: Quota{UsageInDrive: 3_000_000_000}} // 3 GB
	clk := &clock{t: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)}
	s := newSensor(t, q, sampler, clk)

	s.checkAndSample(ctx)

	if sampler.calls != 1 {
		t.Fatalf("StorageQuota calls = %d, want 1 (冷啟動應立即查)", sampler.calls)
	}
	assertQueuedUsage(t, ctx, q, 3.0)
	if _, ok, _ := q.GetState(ctx, StateKeyLastCheck); !ok {
		t.Error("時間戳未寫入")
	}
}

// TestNotYetDue：已有近期時間戳、未達門檻 → 不查。
func TestNotYetDue(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	sampler := &fakeSampler{quota: Quota{UsageInDrive: 1_000_000_000}}
	clk := &clock{t: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)}
	s := newSensor(t, q, sampler, clk)

	s.checkAndSample(ctx) // 第一次：查（冷啟動）
	if sampler.calls != 1 {
		t.Fatalf("first check calls = %d, want 1", sampler.calls)
	}

	clk.add(1 * time.Hour) // 僅過 1 小時（< 24h 門檻）
	s.checkAndSample(ctx)
	if sampler.calls != 1 {
		t.Errorf("StorageQuota calls = %d, want 1 (未到期不應再查)", sampler.calls)
	}
}

// TestDueAfterInterval：超過 quotaInterval → 再查一次。
func TestDueAfterInterval(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	sampler := &fakeSampler{quota: Quota{UsageInDrive: 1_000_000_000}}
	clk := &clock{t: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)}
	s := newSensor(t, q, sampler, clk)

	s.checkAndSample(ctx)
	clk.add(25 * time.Hour) // 超過 24h 門檻
	s.checkAndSample(ctx)

	if sampler.calls != 2 {
		t.Errorf("StorageQuota calls = %d, want 2 (到期應再查)", sampler.calls)
	}
}

// TestBootCatchUp：時間戳為很久以前（模擬關機數日）→ 首次巡檢立即補查（2.4）。
func TestBootCatchUp(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	sampler := &fakeSampler{quota: Quota{UsageInDrive: 2_000_000_000}}
	clk := &clock{t: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)}

	// 手動寫入 5 天前的時間戳，模擬上次查詢後關機數日。
	old := clk.t.Add(-5 * 24 * time.Hour).Format(time.RFC3339Nano)
	if err := q.SetState(ctx, StateKeyLastCheck, old); err != nil {
		t.Fatal(err)
	}

	s := newSensor(t, q, sampler, clk)
	s.checkAndSample(ctx)

	if sampler.calls != 1 {
		t.Errorf("StorageQuota calls = %d, want 1 (開機補查)", sampler.calls)
	}
	assertQueuedUsage(t, ctx, q, 2.0)
}

// TestSampleErrorNoTimestampUpdate：查詢失敗 → 不更新時間戳，下次仍視為到期會重試。
func TestSampleErrorNoTimestampUpdate(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	sampler := &fakeSampler{err: errors.New("network down")}
	clk := &clock{t: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)}
	s := newSensor(t, q, sampler, clk)

	s.checkAndSample(ctx)

	if _, ok, _ := q.GetState(ctx, StateKeyLastCheck); ok {
		t.Error("查詢失敗不應寫入時間戳（確保重試）")
	}
	if n, _ := q.Count(ctx); n != 0 {
		t.Errorf("查詢失敗不應入列，佇列筆數 = %d", n)
	}

	// 恢復正常 → 下次巡檢因時間戳仍不存在而視為到期，重試成功。
	sampler.err = nil
	sampler.quota = Quota{UsageInDrive: 1_000_000_000}
	s.checkAndSample(ctx)
	if _, ok, _ := q.GetState(ctx, StateKeyLastCheck); !ok {
		t.Error("恢復後應重試成功並寫入時間戳")
	}
}

// TestIdempotentSameDay：同一天多次到期查詢 → upsert 同一事件，不重複佔位。
func TestIdempotentSameDay(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	sampler := &fakeSampler{quota: Quota{UsageInDrive: 1_000_000_000}}
	clk := &clock{t: time.Date(2026, 7, 21, 0, 0, 0, 0, time.UTC)}
	s := newSensor(t, q, sampler, clk)

	s.checkAndSample(ctx) // 首查，入列 1 GB

	// 模擬同日重啟後再次到期：清時間戳（視為到期）、時間推進但仍同一天、用量變 2 GB。
	_ = q.SetState(ctx, StateKeyLastCheck, "")
	clk.t = time.Date(2026, 7, 21, 12, 0, 0, 0, time.UTC)
	sampler.quota = Quota{UsageInDrive: 2_000_000_000}
	s.checkAndSample(ctx)

	n, _ := q.Count(ctx)
	if n != 1 {
		t.Errorf("同一天應僅一筆（upsert），佇列筆數 = %d", n)
	}
	assertQueuedUsage(t, ctx, q, 2.0) // 最新值覆蓋
}

// TestUsesUsageInDriveNotUsage：drive_usage_gb 取 usageInDrive 而非帳號總 usage（v15 [D8]）。
func TestUsesUsageInDriveNotUsage(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	// usage（含 Gmail/Photos）遠大於 usageInDrive；歸戶應只取 usageInDrive。
	sampler := &fakeSampler{quota: Quota{Usage: 50_000_000_000, UsageInDrive: 4_000_000_000}}
	clk := &clock{t: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)}
	s := newSensor(t, q, sampler, clk)

	s.checkAndSample(ctx)
	assertQueuedUsage(t, ctx, q, 4.0) // 4 GB（usageInDrive），非 50 GB（usage）
}

// TestTrashFieldDisabled：drive_trash_gb 現階段不啟用，payload 不應含此欄（v15 [D8] 待確認）。
func TestTrashFieldDisabled(t *testing.T) {
	ctx := context.Background()
	q := newTestQueue(t)
	sampler := &fakeSampler{quota: Quota{UsageInDrive: 4_000_000_000, UsageInDriveTrash: 500_000_000}}
	clk := &clock{t: time.Date(2026, 7, 21, 10, 0, 0, 0, time.UTC)}
	s := newSensor(t, q, sampler, clk)

	s.checkAndSample(ctx)

	batch, err := q.PeekBatch(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch) == 0 {
		t.Fatal("佇列為空")
	}
	if _, ok := batch[0].Payload["drive_trash_gb"]; ok {
		t.Error("drive_trash_gb 不應出現在 payload（現階段不啟用）")
	}
}

// assertQueuedUsage 斷言佇列中路徑 C 事件的 drive_usage_gb 約等於 wantGB。
func assertQueuedUsage(t *testing.T, ctx context.Context, q *queue.Queue, wantGB float64) {
	t.Helper()
	batch, err := q.PeekBatch(ctx, 10)
	if err != nil {
		t.Fatalf("PeekBatch: %v", err)
	}
	for _, e := range batch {
		if e.PathType != queue.PathDrive {
			continue
		}
		got, _ := e.Payload["drive_usage_gb"].(float64)
		if got < wantGB-0.001 || got > wantGB+0.001 {
			t.Errorf("drive_usage_gb = %v, want ~%v", got, wantGB)
		}
		return
	}
	t.Fatalf("佇列中找不到路徑 C 事件")
}
