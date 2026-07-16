package queue

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

const mockIDToken = "mock-id-token-0001" // MOCK: 對齊 §7，實際來自 internal/enroll

func openTemp(t *testing.T) (*Queue, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "queue.db")
	q, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q, path
}

func sampleEvent(id string) Event {
	return Event{
		ID:       id,
		PathType: PathComputer,
		Payload:  map[string]any{"date": "2026-07-16", "pc_active_hours": 1.5, "pc_tdp_w": 45},
	}
}

func TestEventIDStable(t *testing.T) {
	a := EventID(mockIDToken, "2026-07-16", PathComputer)
	b := EventID(mockIDToken, "2026-07-16", PathComputer)
	if a != b {
		t.Fatalf("EventID not stable: %q != %q", a, b)
	}
	if a == EventID(mockIDToken, "2026-07-16", PathDrive) {
		t.Fatal("EventID should differ by path type")
	}
	if a == EventID(mockIDToken, "2026-07-17", PathComputer) {
		t.Fatal("EventID should differ by date")
	}
}

func TestEnqueueCountPeek(t *testing.T) {
	ctx := context.Background()
	q, _ := openTemp(t)

	for i, id := range []string{"a", "b", "c"} {
		if err := q.Enqueue(ctx, sampleEvent(id)); err != nil {
			t.Fatalf("Enqueue %d: %v", i, err)
		}
	}
	if n, err := q.Count(ctx); err != nil || n != 3 {
		t.Fatalf("Count = %d, %v; want 3", n, err)
	}

	batch, err := q.PeekBatch(ctx, 2)
	if err != nil {
		t.Fatalf("PeekBatch: %v", err)
	}
	if len(batch) != 2 {
		t.Fatalf("PeekBatch len = %d, want 2", len(batch))
	}
	// FIFO：最舊者先出。
	if batch[0].ID != "a" || batch[1].ID != "b" {
		t.Fatalf("PeekBatch order = %s,%s; want a,b", batch[0].ID, batch[1].ID)
	}
	// Payload 正確往返（JSON 反序列化）。
	if got := batch[0].Payload["date"]; got != "2026-07-16" {
		t.Fatalf("payload date = %v, want 2026-07-16", got)
	}
	// Peek 不移除。
	if n, _ := q.Count(ctx); n != 3 {
		t.Fatalf("Count after peek = %d, want 3 (peek must not remove)", n)
	}
}

func TestMarkUploadedClearsOnly200(t *testing.T) {
	ctx := context.Background()
	q, _ := openTemp(t)
	for _, id := range []string{"a", "b", "c"} {
		q.Enqueue(ctx, sampleEvent(id))
	}
	// 模擬後端回 200：清除 a、b；c 尚未確認須保留（at-least-once）。
	if err := q.MarkUploaded(ctx, []string{"a", "b"}); err != nil {
		t.Fatalf("MarkUploaded: %v", err)
	}
	if n, _ := q.Count(ctx); n != 1 {
		t.Fatalf("Count = %d, want 1 (only unacked remains)", n)
	}
	remaining, _ := q.PeekBatch(ctx, 10)
	if len(remaining) != 1 || remaining[0].ID != "c" {
		t.Fatalf("remaining = %+v, want [c]", remaining)
	}
	// 空 ids 為 no-op。
	if err := q.MarkUploaded(ctx, nil); err != nil {
		t.Fatalf("MarkUploaded(nil): %v", err)
	}
}

func TestEnqueueIdempotentUpsert(t *testing.T) {
	ctx := context.Background()
	q, _ := openTemp(t)

	id := EventID(mockIDToken, "2026-07-16", PathComputer)
	e := sampleEvent(id)
	e.Payload["pc_active_hours"] = 1.0
	if err := q.Enqueue(ctx, e); err != nil {
		t.Fatal(err)
	}
	// 同一事件 ID 再次入列（狀態量累計）→ 更新 payload、不新增列。
	e.Payload["pc_active_hours"] = 2.5
	if err := q.Enqueue(ctx, e); err != nil {
		t.Fatal(err)
	}
	if n, _ := q.Count(ctx); n != 1 {
		t.Fatalf("Count = %d, want 1 (same ID must not duplicate)", n)
	}
	batch, _ := q.PeekBatch(ctx, 1)
	if got := batch[0].Payload["pc_active_hours"]; got != 2.5 {
		t.Fatalf("pc_active_hours = %v, want 2.5 (payload should update)", got)
	}
}

func TestOldestAge(t *testing.T) {
	ctx := context.Background()
	q, _ := openTemp(t)

	// 空佇列。
	if _, ok, err := q.OldestAge(ctx); err != nil || ok {
		t.Fatalf("OldestAge empty = ok:%v err:%v; want ok:false", ok, err)
	}

	q.Enqueue(ctx, sampleEvent("a"))
	time.Sleep(15 * time.Millisecond)
	q.Enqueue(ctx, sampleEvent("b"))

	age, ok, err := q.OldestAge(ctx)
	if err != nil || !ok {
		t.Fatalf("OldestAge = ok:%v err:%v; want ok:true", ok, err)
	}
	if age <= 0 {
		t.Fatalf("OldestAge = %v, want > 0", age)
	}
}

// TestPersistenceAcrossReopen 驗證關閉後重開，資料仍在（落磁碟、非純記憶體）。
func TestPersistenceAcrossReopen(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "persist.db")

	q1, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open 1: %v", err)
	}
	q1.Enqueue(ctx, sampleEvent("a"))
	q1.Enqueue(ctx, sampleEvent("b"))
	if err := q1.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}

	// 模擬崩潰／關機後重啟：重新開同一檔。
	q2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open 2: %v", err)
	}
	defer q2.Close()
	if n, _ := q2.Count(ctx); n != 2 {
		t.Fatalf("Count after reopen = %d, want 2 (data must survive restart)", n)
	}
}

func TestEnqueueRejectsEmptyID(t *testing.T) {
	q, _ := openTemp(t)
	if err := q.Enqueue(context.Background(), Event{PathType: PathComputer}); err == nil {
		t.Fatal("Enqueue with empty ID should error")
	}
}
