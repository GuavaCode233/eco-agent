package queue

import (
	"context"
	"database/sql"
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
		ID:        id,
		PathType:  PathComputer,
		UsageDate: "2026-07-16",
		Payload:   map[string]any{"pc_active_hours": 1.5, "pc_idle_hours": 0.5},
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
	// 共同欄位與 payload 量值皆正確往返（JSON 反序列化）。
	if got := batch[0].UsageDate; got != "2026-07-16" {
		t.Fatalf("UsageDate = %v, want 2026-07-16", got)
	}
	if got := batch[0].Payload["pc_active_hours"]; got != 1.5 {
		t.Fatalf("payload pc_active_hours = %v, want 1.5", got)
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

// usage_date 是後端唯一鍵組成，留空會讓該筆無從落庫；須在入列時即擋下，
// 而非等到上傳才由後端拒收。
func TestEnqueueRejectsEmptyUsageDate(t *testing.T) {
	q, _ := openTemp(t)
	e := sampleEvent("a")
	e.UsageDate = ""
	if err := q.Enqueue(context.Background(), e); err == nil {
		t.Fatal("Enqueue with empty usage date should error")
	}
}

// TestCollectedAtRefreshedOnUpsert 驗證 [D14] 的核心語意：同一事件 ID 重複 upsert 時，
// collected_at 隨新 payload 更新（後端據此判定「哪一次採集較新」），created_at 則不動
// （maxAge 保底觸發須反映佇列滯留時間，不可被累計更新刷新）。
func TestCollectedAtRefreshedOnUpsert(t *testing.T) {
	ctx := context.Background()
	q, _ := openTemp(t)

	id := EventID(mockIDToken, "2026-07-16", PathComputer)
	first := time.Date(2026, 7, 16, 9, 0, 0, 0, time.UTC)
	second := first.Add(90 * time.Minute)

	e := sampleEvent(id)
	e.Payload["pc_active_hours"] = 1.0
	e.CollectedAt = first
	if err := q.Enqueue(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, _ := q.PeekBatch(ctx, 1)
	createdAt := got[0].CreatedAt
	if !got[0].CollectedAt.Equal(first) {
		t.Fatalf("CollectedAt = %v, want %v", got[0].CollectedAt, first)
	}

	// 第二次採集：payload 與 collected_at 一起前進。
	e.Payload["pc_active_hours"] = 2.5
	e.CollectedAt = second
	if err := q.Enqueue(ctx, e); err != nil {
		t.Fatal(err)
	}
	got, _ = q.PeekBatch(ctx, 1)
	if !got[0].CollectedAt.Equal(second) {
		t.Fatalf("CollectedAt after re-enqueue = %v, want %v（採集時間戳須隨 payload 更新）",
			got[0].CollectedAt, second)
	}
	if !got[0].CreatedAt.Equal(createdAt) {
		t.Fatalf("CreatedAt changed on upsert (%v → %v)；首次入列時間不可被刷新",
			createdAt, got[0].CreatedAt)
	}

	// Get 亦須帶回 collected_at（路徑 A 重啟回填當日累計時會走此路徑）。
	one, ok, err := q.Get(ctx, id)
	if err != nil || !ok {
		t.Fatalf("Get = %v, %v", ok, err)
	}
	if !one.CollectedAt.Equal(second) {
		t.Fatalf("Get CollectedAt = %v, want %v", one.CollectedAt, second)
	}
}

// TestCollectedAtDefaultsToEnqueueTime 驗證呼叫端未填時由佇列補上（不得留零值上送）。
func TestCollectedAtDefaultsToEnqueueTime(t *testing.T) {
	ctx := context.Background()
	q, _ := openTemp(t)

	before := time.Now()
	if err := q.Enqueue(ctx, sampleEvent("a")); err != nil { // 未設 CollectedAt
		t.Fatal(err)
	}
	got, _ := q.PeekBatch(ctx, 1)
	if got[0].CollectedAt.Before(before) || got[0].CollectedAt.After(time.Now()) {
		t.Fatalf("CollectedAt = %v, want 入列當下時間（介於 %v 與現在之間）", got[0].CollectedAt, before)
	}
}

// TestMigrateLegacyQueueFile 驗證 payload 對齊前建立的舊佇列檔可就地升級，既有未送資料不遺失
// （否則升版即違反 at-least-once）：補 collected_at（以 updated_at 回填）、補 usage_date
// （自 payload 的 "date" 鍵搬出並移除，避免同時帶新舊兩個欄位）。
func TestMigrateLegacyQueueFile(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "legacy.db")

	// 以 [D14] 之前的 schema 建檔並寫入一筆待送資料。
	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy: %v", err)
	}
	const legacySchema = `
CREATE TABLE events (
	id         TEXT PRIMARY KEY,
	path_type  TEXT NOT NULL,
	payload    TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	updated_at INTEGER NOT NULL
);`
	if _, err := legacy.ExecContext(ctx, legacySchema); err != nil {
		t.Fatalf("legacy schema: %v", err)
	}
	updatedAt := time.Date(2026, 7, 16, 10, 30, 0, 0, time.UTC)
	if _, err := legacy.ExecContext(ctx,
		`INSERT INTO events (id, path_type, payload, created_at, updated_at) VALUES (?, ?, ?, ?, ?)`,
		"legacy-1", string(PathComputer), `{"date":"2026-07-16","pc_active_hours":3}`,
		updatedAt.Add(-time.Hour).UnixNano(), updatedAt.UnixNano()); err != nil {
		t.Fatalf("legacy insert: %v", err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy: %v", err)
	}

	// 升版後開檔：migrate 應補欄，舊資料仍可讀。
	q, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open legacy db: %v", err)
	}
	defer q.Close()

	batch, err := q.PeekBatch(ctx, 10)
	if err != nil {
		t.Fatalf("PeekBatch: %v", err)
	}
	if len(batch) != 1 {
		t.Fatalf("PeekBatch len = %d, want 1（舊檔資料不可遺失）", len(batch))
	}
	if !batch[0].CollectedAt.Equal(updatedAt) {
		t.Fatalf("CollectedAt = %v, want %v（舊列須以 updated_at 回填）", batch[0].CollectedAt, updatedAt)
	}
	if got := batch[0].UsageDate; got != "2026-07-16" {
		t.Errorf("UsageDate = %q, want 2026-07-16（須自 payload 的 date 鍵搬出）", got)
	}
	if _, still := batch[0].Payload["date"]; still {
		t.Errorf("payload 仍留有 date 鍵：%v（搬出後須移除，否則新舊欄位並存）", batch[0].Payload)
	}
	if got := batch[0].Payload["pc_active_hours"]; got != float64(3) {
		t.Errorf("payload pc_active_hours = %v, want 3（量值不可在搬移中遺失）", got)
	}

	// 重複開檔不得重跑 ALTER（否則第二次啟動即失敗）。
	if err := q.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	q2, err := Open(ctx, path)
	if err != nil {
		t.Fatalf("Open migrated db again: %v", err)
	}
	defer q2.Close()
	if n, _ := q2.Count(ctx); n != 1 {
		t.Fatalf("Count after reopen = %d, want 1", n)
	}
}
