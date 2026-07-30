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
	"errors"
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
	// PathType 為來源路徑。上送時即 payload 的 path_type 共同欄位（v20 §4.4 [D12]，由 Agent
	// 明送、不由後端推斷）；其值域與後端 DIGITAL_USAGE.path_type 一致，兩端無翻譯層。
	PathType PathType
	// UsageDate 為該筆用量所屬日期（YYYY-MM-DD），即上送的 usage_date 共同欄位。
	// 與 CollectedAt 的分工：本欄是「哪一天的用量」（日期粒度、唯一鍵組成），
	// CollectedAt 是「何時採集到」（時刻粒度、亂序勝出判定），兩者不可混用。
	UsageDate string
	// Payload 為去識別化後的**該路徑量值**（不含姓名/Email，亦不含共同欄位——
	// path_type／usage_date／collected_at 各有專屬欄位，上送時與量值同層攤平）。
	// 例：路徑 A {"pc_active_hours":..., "pc_idle_hours":..., "pc_avg_cpu_util":..., "cpu_model":...}。
	Payload map[string]any
	// CreatedAt 為佇列首次寫入該事件的時間；同 ID 再次 Enqueue 不會更新此值，
	// 使 OldestAge（maxAge 保底觸發）反映「資料在佇列滯留多久」而非最後更新時間。
	CreatedAt time.Time
	// CollectedAt 為 Agent **採集**該筆數值的時間戳（v20 §4.4 [D14]），隨 payload 一併上送，
	// 供後端於亂序抵達時判定勝出（upsert 條件 EXCLUDED.collected_at > digital_usage.collected_at）。
	//
	// 與 CreatedAt 的關鍵差異：本欄**每次 Enqueue 都會更新**——路徑 A／C 送的是「當日累計值」，
	// 同一事件 ID 一天內反覆 upsert，每次都是一次較新的採集；後端要比較的正是「哪一次採集較新」，
	// 故不可沿用首次入列時間。CreatedAt 則相反，固定於首次入列以正確計算佇列滯留時間。
	//
	// 呼叫端未填（零值）時由 Enqueue 以入列當下時間補上。
	CollectedAt time.Time
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
	id           TEXT PRIMARY KEY,        -- 唯一事件 ID（穩定鍵），冪等去重
	path_type    TEXT NOT NULL,
	usage_date   TEXT NOT NULL,           -- 用量所屬日期 YYYY-MM-DD（唯一鍵組成）
	payload      TEXT NOT NULL,           -- JSON，僅該路徑量值（共同欄位另存專屬欄）
	created_at   INTEGER NOT NULL,        -- 首次入列時間（UnixNano），不隨 upsert 更新
	updated_at   INTEGER NOT NULL,        -- 最後更新時間（UnixNano）
	collected_at INTEGER NOT NULL         -- Agent 採集時間戳（UnixNano），隨 upsert 更新（[D14]）
);
CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at);
CREATE TABLE IF NOT EXISTS state (
	key   TEXT PRIMARY KEY,          -- 感測器跨重啟持久化狀態鍵（如 lastDriveQuotaCheckAt）
	value TEXT NOT NULL
);`
	if _, err := q.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("queue: init schema: %w", err)
	}
	return q.migrate(ctx)
}

// migrate 補齊既有佇列檔缺少的欄位。佇列落磁碟且跨版本保留，schema 演進時舊檔須就地升級，
// 否則升版後既有未送資料會全數讀取失敗（違反 at-least-once）。
func (q *Queue) migrate(ctx context.Context) error {
	if err := q.migrateCollectedAt(ctx); err != nil {
		return err
	}
	return q.migrateUsageDate(ctx)
}

// migrateCollectedAt 補 [D14] 新增的採集時間戳欄；舊列以 updated_at 回填——該值即「最後一次
// 寫入本筆的時間」，對已在佇列中的舊資料而言是最接近採集時間的可得值。
func (q *Queue) migrateCollectedAt(ctx context.Context) error {
	has, err := q.hasColumn(ctx, "events", "collected_at")
	if err != nil || has {
		return err
	}
	stmts := []string{
		"ALTER TABLE events ADD COLUMN collected_at INTEGER NOT NULL DEFAULT 0",
		"UPDATE events SET collected_at = updated_at WHERE collected_at = 0",
	}
	for _, s := range stmts {
		if _, err := q.db.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("queue: migrate collected_at: %w", err)
		}
	}
	return nil
}

// migrateUsageDate 補 usage_date 欄（payload 扁平化：共同欄位自 payload 提升為事件層欄位）。
// 舊列的日期存在 payload 的 "date" 鍵內，逐列搬出後自 payload 移除，避免同時帶新舊兩個欄位。
func (q *Queue) migrateUsageDate(ctx context.Context) error {
	has, err := q.hasColumn(ctx, "events", "usage_date")
	if err != nil || has {
		return err
	}
	if _, err := q.db.ExecContext(ctx,
		`ALTER TABLE events ADD COLUMN usage_date TEXT NOT NULL DEFAULT ''`); err != nil {
		return fmt.Errorf("queue: migrate usage_date: %w", err)
	}

	rows, err := q.db.QueryContext(ctx, `SELECT id, payload FROM events`)
	if err != nil {
		return fmt.Errorf("queue: migrate usage_date: scan rows: %w", err)
	}
	type row struct{ id, usageDate, payload string }
	var updates []row
	for rows.Next() {
		var id, payload string
		if err := rows.Scan(&id, &payload); err != nil {
			rows.Close()
			return fmt.Errorf("queue: migrate usage_date: scan: %w", err)
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(payload), &m); err != nil {
			continue // payload 無法解析：留白 usage_date，該列於上送時由後端拒收，不阻斷升級
		}
		date, _ := m["date"].(string)
		delete(m, "date")
		buf, err := json.Marshal(m)
		if err != nil {
			continue
		}
		updates = append(updates, row{id: id, usageDate: date, payload: string(buf)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return fmt.Errorf("queue: migrate usage_date: rows: %w", err)
	}
	rows.Close()

	for _, u := range updates {
		if _, err := q.db.ExecContext(ctx,
			`UPDATE events SET usage_date = ?, payload = ? WHERE id = ?`,
			u.usageDate, u.payload, u.id); err != nil {
			return fmt.Errorf("queue: migrate usage_date: update %s: %w", u.id, err)
		}
	}
	return nil
}

func (q *Queue) hasColumn(ctx context.Context, table, column string) (bool, error) {
	rows, err := q.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return false, fmt.Errorf("queue: table_info %s: %w", table, err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return false, fmt.Errorf("queue: table_info %s columns: %w", table, err)
	}
	for rows.Next() {
		// PRAGMA table_info 的欄數隨 SQLite 版本而異（cid, name, type, notnull, dflt_value, pk[, hidden]）；
		// 以實際欄數配置掃描目標，只取 name。
		cells := make([]any, len(cols))
		var name string
		for i := range cells {
			if cols[i] == "name" {
				cells[i] = &name
				continue
			}
			cells[i] = new(sql.RawBytes)
		}
		if err := rows.Scan(cells...); err != nil {
			return false, fmt.Errorf("queue: table_info %s scan: %w", table, err)
		}
		if name == column {
			return true, nil
		}
	}
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("queue: table_info %s rows: %w", table, err)
	}
	return false, nil
}

// SetState 寫入一組持久化鍵值，與事件同一份 SQLite 落磁碟（跨重啟保留）。
//
// 供狀態值長輪詢路徑保存「上次查詢時間戳」——路徑 C 的 lastDriveQuotaCheckAt、
// 路徑 B 的 lastPrinterPollAt（§4.4 觸發模型：以持久化時間戳＋checkInterval 到期判斷，
// 不用 sleep 絕對計時器）。value 由呼叫端自行編碼（例：RFC3339Nano 時間字串）。
func (q *Queue) SetState(ctx context.Context, key, value string) error {
	if key == "" {
		return fmt.Errorf("queue: set state: empty key")
	}
	const stmt = `
INSERT INTO state (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value`
	if _, err := q.db.ExecContext(ctx, stmt, key, value); err != nil {
		return fmt.Errorf("queue: set state %q: %w", key, err)
	}
	return nil
}

// GetState 讀取持久化鍵值。鍵不存在回 ("", false, nil)——供冷啟動判斷（§2 Step 2.3：
// 時間戳不存在視為「已到期」）。
func (q *Queue) GetState(ctx context.Context, key string) (string, bool, error) {
	var value string
	err := q.db.QueryRowContext(ctx, "SELECT value FROM state WHERE key = ?", key).Scan(&value)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("queue: get state %q: %w", key, err)
	}
	return value, true, nil
}

// Close 關閉底層資料庫。
func (q *Queue) Close() error {
	return q.db.Close()
}

// Enqueue 寫入一筆事件。
//
// 冪等：同一事件 ID 再次 Enqueue 時，更新 payload、updated_at 與 collected_at，但保留原
// created_at（狀態量累計、或 at-least-once 重入皆安全，不會重複佔位、不會刷新滯留計時）。
// collected_at 與 payload 同步更新——新 payload 即一次新的採集，兩者必須一致（[D14]）。
//
// 呼叫端無需填 CreatedAt——由佇列以入列當下時間設定。CollectedAt 建議由感測器以其採集時鐘
// 明確填入；未填（零值）時同樣以入列當下時間補上。
func (q *Queue) Enqueue(ctx context.Context, e Event) error {
	if e.ID == "" {
		return fmt.Errorf("queue: enqueue: empty event ID")
	}
	// usage_date 為唯一鍵組成（employee_id, usage_date, path_type[, device_id]）；留空會讓後端
	// 無從落庫，且錯誤要到上傳時才浮現。在此擋下，讓感測器的漏填即刻失敗。
	if e.UsageDate == "" {
		return fmt.Errorf("queue: enqueue %s: empty usage date", e.ID)
	}
	payload, err := json.Marshal(e.Payload)
	if err != nil {
		return fmt.Errorf("queue: enqueue %s: marshal payload: %w", e.ID, err)
	}
	now := time.Now()
	collectedAt := e.CollectedAt
	if collectedAt.IsZero() {
		collectedAt = now
	}
	const stmt = `
INSERT INTO events (id, path_type, usage_date, payload, created_at, updated_at, collected_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(id) DO UPDATE SET
	path_type    = excluded.path_type,
	usage_date   = excluded.usage_date,
	payload      = excluded.payload,
	updated_at   = excluded.updated_at,
	collected_at = excluded.collected_at`
	if _, err := q.db.ExecContext(ctx, stmt, e.ID, string(e.PathType), e.UsageDate, string(payload),
		now.UnixNano(), now.UnixNano(), collectedAt.UnixNano()); err != nil {
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
SELECT id, path_type, usage_date, payload, created_at, collected_at
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
			e           Event
			pathType    string
			payload     string
			createdAt   int64
			collectedAt int64
		)
		if err := rows.Scan(&e.ID, &pathType, &e.UsageDate, &payload, &createdAt, &collectedAt); err != nil {
			return nil, fmt.Errorf("queue: peek scan: %w", err)
		}
		e.PathType = PathType(pathType)
		e.CreatedAt = time.Unix(0, createdAt)
		e.CollectedAt = time.Unix(0, collectedAt)
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

// Get 讀取指定事件 ID 的單筆事件。不存在時回 (Event{}, false, nil)。
//
// 供路徑 A（sensors/computer）重啟後回填「當日累計」使用：Agent 以「一天一筆累計事件」
// 的狀態值輪詢模型運作，事件 ID 為 idToken+日期+路徑；重啟時記憶體累計已失，若當日事件
// 仍在佇列（尚未上傳清除），先讀回作為累計基準，避免下次 upsert 以較小值覆蓋。
func (q *Queue) Get(ctx context.Context, id string) (Event, bool, error) {
	const query = `SELECT id, path_type, usage_date, payload, created_at, collected_at FROM events WHERE id = ?`
	var (
		e           Event
		pathType    string
		payload     string
		createdAt   int64
		collectedAt int64
	)
	err := q.db.QueryRowContext(ctx, query, id).
		Scan(&e.ID, &pathType, &e.UsageDate, &payload, &createdAt, &collectedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, false, nil
	}
	if err != nil {
		return Event{}, false, fmt.Errorf("queue: get %s: %w", id, err)
	}
	e.PathType = PathType(pathType)
	e.CreatedAt = time.Unix(0, createdAt)
	e.CollectedAt = time.Unix(0, collectedAt)
	if err := json.Unmarshal([]byte(payload), &e.Payload); err != nil {
		return Event{}, false, fmt.Errorf("queue: get unmarshal %s: %w", id, err)
	}
	return e, true, nil
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
