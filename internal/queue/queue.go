// Package queue 提供 Eco-Agent 的本機持久化佇列（v12 §4.4.3）。
//
// 設計要點（不可違反）：
//   - 落磁碟持久化（SQLite 單檔），非純記憶體——關機／崩潰後資料須仍在，下次醒來補送。
//   - 每筆帶唯一事件 ID（id_token + 日期 + 路徑類型 組穩定鍵），作為主鍵；
//     供 at-least-once 重送時去重（同 ID 重送不重複計算），亦供後端 upsert 冪等（§4.4.3）。
//   - 佇列資料僅在後端回 200 後才由上層呼叫 MarkUploaded 清除；失敗則保留待下次觸發重送。
//
// 採用 modernc.org/sqlite（純 Go、免 cgo），以維持「單一靜態執行檔＋跨平台交叉編譯」定位。
package queue

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// PathType 標示事件來自哪條感測路徑（作為事件 ID 組成鍵之一）。
type PathType string

const (
	// PathComputer：路徑 A，電腦使用。
	PathComputer PathType = "computer"
	// PathDrive：路徑 C，雲端儲存。
	PathDrive PathType = "drive"
	// PathPrinter：路徑 B，印表機。
	PathPrinter PathType = "printer"
)

// Event 是佇列中的一筆待上傳資料。
type Event struct {
	// ID 為唯一事件 ID（穩定鍵）；作為主鍵去重。以 EventID() 組出。
	ID string
	// PathType 為來源路徑。
	PathType PathType
	// Payload 為去識別化後的資料欄位（僅含 ID Token 與量值，不含姓名/Email）。
	// 例：路徑 A {"date":..., "pc_active_hours":..., "pc_tdp_w":...}。
	Payload map[string]any
	// CreatedAt 為佇列首次寫入該事件的時間；同 ID 再次 Enqueue 不會更新此值，
	// 使 OldestAge（maxAge 保底觸發）反映「資料在佇列滯留多久」而非最後更新時間。
	CreatedAt time.Time
}

// EventID 依「id_token + 日期 + 路徑類型」組出穩定的唯一事件 ID（§4.4.3）。
// 同一員工、同一天、同一路徑恆得同一 ID，重送與跨重啟皆可重現，供冪等去重。
func EventID(idToken, date string, p PathType) string {
	return fmt.Sprintf("%s|%s|%s", idToken, date, p)
}

// Queue 是 SQLite 落磁碟持久化佇列。並行安全（由底層 database/sql 串行化）。
type Queue struct {
	db *sql.DB
}

// Open 開啟（或建立）指定路徑的佇列檔並初始化 schema。
// path 為 SQLite 檔案路徑（例：使用者資料目錄下的 eco-agent.db）。
func Open(ctx context.Context, path string) (*Queue, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("queue: open %q: %w", path, err)
	}
	// 單一嵌入式寫入者：限制連線數避免 SQLITE_BUSY 鎖競爭。
	db.SetMaxOpenConns(1)

	q := &Queue{db: db}
	if err := q.init(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return q, nil
}

func (q *Queue) init(ctx context.Context) error {
	pragmas := []string{
		"PRAGMA journal_mode=WAL",   // 崩潰韌性＋讀寫並行
		"PRAGMA synchronous=NORMAL", // 碳排資料容忍崩潰前數秒未落地（§5.1）
		"PRAGMA busy_timeout=5000",
	}
	for _, p := range pragmas {
		if _, err := q.db.ExecContext(ctx, p); err != nil {
			return fmt.Errorf("queue: pragma %q: %w", p, err)
		}
	}
	const schema = `
CREATE TABLE IF NOT EXISTS events (
	id         TEXT PRIMARY KEY,          -- 唯一事件 ID（穩定鍵），冪等去重
	path_type  TEXT NOT NULL,
	payload    TEXT NOT NULL,             -- JSON
	created_at INTEGER NOT NULL,          -- 首次入列時間（UnixNano）
	updated_at INTEGER NOT NULL           -- 最後更新時間（UnixNano）
);
CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at);`
	if _, err := q.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("queue: init schema: %w", err)
	}
	return nil
}

// Close 關閉底層資料庫。
func (q *Queue) Close() error {
	return q.db.Close()
}

// Enqueue 寫入一筆事件。
//
// 冪等：同一事件 ID 再次 Enqueue 時，更新 payload 與 updated_at，但保留原 created_at
// （狀態量累計、或 at-least-once 重入皆安全，不會重複佔位、不會刷新滯留計時）。
// 呼叫端無需填 CreatedAt——由佇列以入列當下時間設定。
func (q *Queue) Enqueue(ctx context.Context, e Event) error {
	if e.ID == "" {
		return fmt.Errorf("queue: enqueue: empty event ID")
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("queue: enqueue %s: marshal payload: %w", e.ID, err)
	}
	now := time.Now().UnixNano()
	const stmt = `
INSERT INTO events (id, path_type, payload, created_at, updated_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	path_type  = excluded.path_type,
	payload    = excluded.payload,
	updated_at = excluded.updated_at`
	if _, err := q.db.ExecContext(ctx, stmt, e.ID, string(e.PathType), string(payload), now, now); err != nil {
		return fmt.Errorf("queue: enqueue %s: %w", e.ID, err)
	}
	return nil
}

// PeekBatch 取出最舊的至多 n 筆事件（不移除）。上傳成功後由 MarkUploaded 清除。
// n <= 0 時回傳空切片。
func (q *Queue) PeekBatch(ctx context.Context, n int) ([]Event, error) {
	if n <= 0 {
		return nil, nil
	}
	const query = `
SELECT id, path_type, payload, created_at
FROM events
ORDER BY created_at ASC, id ASC
LIMIT ?`
	rows, err := q.db.QueryContext(ctx, query, n)
	if err != nil {
		return nil, fmt.Errorf("queue: peek: %w", err)
	}
	defer rows.Close()

	var events []Event
	for rows.Next() {
		var (
			e         Event
			pathType  string
			payload   string
			createdAt int64
		)
		if err := rows.Scan(&e.ID, &pathType, &payload, &createdAt); err != nil {
			return nil, fmt.Errorf("queue: peek scan: %w", err)
		}
		e.PathType = PathType(pathType)
		e.CreatedAt = time.Unix(0, createdAt)
		if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
			return nil, fmt.Errorf("queue: peek unmarshal %s: %w", e.ID, err)
		}
		events = append(events, e)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("queue: peek rows: %w", err)
	}
	return events, nil
}

// MarkUploaded 於後端回 200 後，將指定事件自佇列清除（at-least-once：僅 200 才清）。
// ids 為空時為 no-op。
func (q *Queue) MarkUploaded(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	placeholders := make([]string, len(ids))
	args := make([]any, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	stmt := fmt.Sprintf("DELETE FROM events WHERE id IN (%s)", strings.Join(placeholders, ","))
	if _, err := q.db.ExecContext(ctx, stmt, args...); err != nil {
		return fmt.Errorf("queue: mark uploaded: %w", err)
	}
	return nil
}

// Count 回傳佇列目前待上傳筆數（供 thresholdCount 累積達量觸發）。
func (q *Queue) Count(ctx context.Context) (int, error) {
	var n int
	if err := q.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM events").Scan(&n); err != nil {
		return 0, fmt.Errorf("queue: count: %w", err)
	}
	return n, nil
}

// OldestAge 回傳佇列中最舊一筆的滯留時間（供 maxAge 保底觸發）。
// 佇列為空時回傳 (0, false, nil)。
func (q *Queue) OldestAge(ctx context.Context) (time.Duration, bool, error) {
	var oldest sql.NullInt64
	if err := q.db.QueryRowContext(ctx, "SELECT MIN(created_at) FROM events").Scan(&oldest); err != nil {
		return 0, false, fmt.Errorf("queue: oldest age: %w", err)
	}
	if !oldest.Valid {
		return 0, false, nil
	}
	return time.Since(time.Unix(0, oldest.Int64)), true, nil
}
