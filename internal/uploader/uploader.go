// Package uploader 實作 Eco-Agent 的四重觸發批次上傳與 at-least-once 送達（v12 §4.4.3）。
//
// 四重觸發（皆不綁絕對時刻，先到先觸發）：
//   - 累積達量 thresholdCount：巡檢時佇列達 N 筆即 flush（主力）。
//   - 最長滯留 maxAge：巡檢時最舊一筆超過即 flush（保底）。
//   - 開機／喚醒後檢查：Run 啟動先補送上次未送完者（路徑 C/B 到期補查日後在此合流）。
//   - 關機／登出前 hook：ctx 取消時搶送零頭（OS 關機事件 → ctx 取消由 cmd/platform 接線）。
//
// At-least-once：佇列僅在後端回 200 後才 MarkUploaded 清除；失敗保留、搭下次觸發重送。
// 每筆帶唯一事件 ID，後端 upsert 冪等去重（重複送達不重複計算）。
//
// 重試策略（§4.4.4）：不設獨立重試計時器、不設次數上限、不採指數退避——失敗即留佇列，
// 節奏跟隨既有稀疏觸發（最密 checkInterval）。
//
// 撤銷夾帶檢查：每次 flush 讀後端回應；401/403 視為已撤銷 → 自清憑證、停止上傳（§4.4.2）。
package uploader

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"sync"
	"time"

	"eco-agent/internal/config"
	"eco-agent/internal/enroll"
	"eco-agent/internal/queue"
)

// ErrStopped 表示上傳已因撤銷而停止；Run／Flush 之後不再送出，直到重新綁定。
var ErrStopped = errors.New("uploader: stopped (credentials revoked)")

// Reason 標示本次 flush 的觸發來源（供 log 與測試）。
type Reason string

const (
	ReasonThreshold Reason = "threshold" // 累積達量
	ReasonMaxAge    Reason = "maxage"    // 最長滯留
	ReasonStartup   Reason = "startup"   // 開機後補送
	ReasonShutdown  Reason = "shutdown"  // 關機前搶送
	ReasonManual    Reason = "manual"    // 手動（測試／demo）
)

// Queue 是 uploader 依賴的佇列介面（*queue.Queue 滿足）。
type Queue interface {
	PeekBatch(ctx context.Context, n int) ([]queue.Event, error)
	MarkUploaded(ctx context.Context, ids []string) error
	Count(ctx context.Context) (int, error)
	OldestAge(ctx context.Context) (time.Duration, bool, error)
}

// Credentials 是 uploader 依賴的憑證介面（*enroll.Enroller 滿足）。
type Credentials interface {
	IDToken() (string, error)
	AccessToken(ctx context.Context) (string, error)
	ClearCredentials() error
}

// Uploader 協調佇列、憑證與傳輸，執行四重觸發上傳。
type Uploader struct {
	q       Queue
	creds   Credentials
	cfg     config.Config
	senders map[Protocol]Sender
	log     *slog.Logger

	shutdownTimeout time.Duration

	flushMu sync.Mutex // 序列化 flush（巡檢／關機／手動不重疊）

	mu      sync.Mutex
	stopped bool
}

// Option 客製化 Uploader。
type Option func(*Uploader)

// WithLogger 設定日誌器。
func WithLogger(l *slog.Logger) Option { return func(u *Uploader) { u.log = l } }

// WithSender 覆寫指定協定的 Sender（測試注入）。
func WithSender(p Protocol, s Sender) Option {
	return func(u *Uploader) { u.senders[p] = s }
}

// WithUploadURL 讓兩協定 Sender 皆指向指定 mock URL（測試指向 httptest）。
func WithUploadURL(url string) Option {
	return func(u *Uploader) {
		u.senders[ProtocolMQTT] = NewMockHTTPSender(ProtocolMQTT, url)
		u.senders[ProtocolHTTPS] = NewMockHTTPSender(ProtocolHTTPS, url)
	}
}

// WithShutdownTimeout 設定關機搶送的逾時。
func WithShutdownTimeout(d time.Duration) Option {
	return func(u *Uploader) { u.shutdownTimeout = d }
}

// New 建立 Uploader。上傳端點取自環境變數 ECO_AGENT_UPLOAD_URL，預設 DefaultUploadURL（§7）。
func New(q Queue, creds Credentials, cfg config.Config, opts ...Option) *Uploader {
	url := os.Getenv(EnvUploadURL)
	if url == "" {
		url = DefaultUploadURL
	}
	u := &Uploader{
		q:     q,
		creds: creds,
		cfg:   cfg,
		senders: map[Protocol]Sender{
			ProtocolMQTT:  NewMockHTTPSender(ProtocolMQTT, url),
			ProtocolHTTPS: NewMockHTTPSender(ProtocolHTTPS, url),
		},
		log:             slog.Default(),
		shutdownTimeout: 5 * time.Second,
	}
	for _, o := range opts {
		o(u)
	}
	return u
}

// Stopped 回報是否已因撤銷停止。
func (u *Uploader) Stopped() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.stopped
}

func (u *Uploader) stop() {
	u.mu.Lock()
	u.stopped = true
	u.mu.Unlock()
}

// Run 阻塞執行上傳迴圈：開機後補送 → 每 checkInterval 巡檢達量/滯留 → ctx 取消時關機搶送。
// 因撤銷停止時回 ErrStopped；正常關閉回 ctx.Err()。
func (u *Uploader) Run(ctx context.Context) error {
	// 開機／喚醒後檢查（補送上次未送完者）。
	if err := u.Flush(ctx, ReasonStartup); err != nil && errors.Is(err, ErrStopped) {
		return err
	}

	ticker := time.NewTicker(u.cfg.CheckInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			u.shutdownFlush() // 關機前 hook 兜底：以獨立逾時 ctx 搶送零頭。
			return ctx.Err()
		case <-ticker.C:
			if u.Stopped() {
				return ErrStopped
			}
			reason, due, err := u.dueTrigger(ctx)
			if err != nil {
				u.log.Warn("uploader: trigger check failed", "err", err)
				continue
			}
			if due {
				if err := u.Flush(ctx, reason); err != nil && errors.Is(err, ErrStopped) {
					return err
				}
			}
		}
	}
}

// dueTrigger 判斷巡檢當下是否有觸發條件成立（達量優先於滯留）。
func (u *Uploader) dueTrigger(ctx context.Context) (Reason, bool, error) {
	n, err := u.q.Count(ctx)
	if err != nil {
		return "", false, err
	}
	if n > 0 && n >= u.cfg.ThresholdCount {
		return ReasonThreshold, true, nil
	}
	age, ok, err := u.q.OldestAge(ctx)
	if err != nil {
		return "", false, err
	}
	if ok && age >= u.cfg.MaxAge {
		return ReasonMaxAge, true, nil
	}
	return "", false, nil
}

func (u *Uploader) shutdownFlush() {
	if u.Stopped() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), u.shutdownTimeout)
	defer cancel()
	if err := u.Flush(ctx, ReasonShutdown); err != nil && !errors.Is(err, ErrStopped) {
		u.log.Warn("uploader: shutdown flush failed", "err", err)
	}
}

// Flush 打包並上傳佇列資料，直到清空、遇失敗（保留待重送）或撤銷。
// 撤銷時回 ErrStopped；其餘錯誤（非致命）回 nil 或包裝後的 error（資料保留、下次觸發重送）。
func (u *Uploader) Flush(ctx context.Context, reason Reason) error {
	u.flushMu.Lock()
	defer u.flushMu.Unlock()
	if u.Stopped() {
		return ErrStopped
	}

	for {
		batch, err := u.q.PeekBatch(ctx, u.cfg.UploadBatchMax)
		if err != nil {
			return fmt.Errorf("uploader: peek: %w", err)
		}
		if len(batch) == 0 {
			return nil
		}

		idToken, accessToken, err := u.credentials(ctx)
		if err != nil {
			switch {
			case errors.Is(err, enroll.ErrRevoked):
				u.stop()
				return ErrStopped
			case errors.Is(err, enroll.ErrNotBound):
				u.log.Warn("uploader: flush skipped, device not bound", "reason", reason)
				return nil
			default:
				return fmt.Errorf("uploader: credentials: %w", err)
			}
		}

		uploaded, revoked := u.sendBatch(ctx, idToken, accessToken, batch)
		if len(uploaded) > 0 {
			// At-least-once：只有回 200 的事件才在此清除。
			if err := u.q.MarkUploaded(ctx, uploaded); err != nil {
				return fmt.Errorf("uploader: mark uploaded: %w", err)
			}
		}
		u.log.Info("uploader: flush batch",
			"reason", reason, "peeked", len(batch), "uploaded", len(uploaded), "revoked", revoked)

		if revoked {
			u.stop()
			return ErrStopped
		}
		if len(uploaded) < len(batch) {
			// 有事件未成功（非 200／網路錯誤）→ 停止本次 flush，保留待下次觸發重送。
			return nil
		}
		// 本批全數清除，續抓下一批直到清空。
	}
}

// credentials 取回去識別化打包所需的 ID Token 與上傳用 Access Token。
func (u *Uploader) credentials(ctx context.Context) (idToken, accessToken string, err error) {
	if idToken, err = u.creds.IDToken(); err != nil {
		return "", "", err
	}
	if accessToken, err = u.creds.AccessToken(ctx); err != nil {
		return "", "", err
	}
	return idToken, accessToken, nil
}

// sendBatch 依協定分流送出，回傳成功清除的事件 ID 與是否偵測到撤銷。
func (u *Uploader) sendBatch(ctx context.Context, idToken, accessToken string, batch []queue.Event) (uploaded []string, revoked bool) {
	groups := groupByProtocol(batch)
	for _, proto := range protocolOrder {
		evs := groups[proto]
		if len(evs) == 0 {
			continue
		}
		sender := u.senders[proto]
		resp, err := sender.Send(ctx, Batch{
			IDToken:     idToken,
			AccessToken: accessToken,
			Protocol:    proto,
			Events:      evs,
		})
		if err != nil {
			// 網路／離線錯誤：保留佇列，不重試迴圈（搭下次觸發）。
			u.log.Warn("uploader: send failed, keeping in queue", "protocol", proto, "err", err)
			continue
		}
		switch {
		case resp.StatusCode == http.StatusOK:
			uploaded = append(uploaded, eventIDs(evs)...)
		case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
			// 撤銷夾帶檢查：自清憑證（含金鑰庫 Refresh Token）。
			u.log.Warn("uploader: revocation detected, self-clearing credentials",
				"protocol", proto, "status", resp.StatusCode)
			if cerr := u.creds.ClearCredentials(); cerr != nil {
				u.log.Warn("uploader: clear credentials failed", "err", cerr)
			}
			return uploaded, true
		default:
			// 其他非 200：保留佇列，下次觸發重送。
			u.log.Warn("uploader: non-200 response, keeping in queue",
				"protocol", proto, "status", resp.StatusCode)
		}
	}
	return uploaded, false
}

// groupByProtocol 依協定分組，組內維持原順序。
func groupByProtocol(batch []queue.Event) map[Protocol][]queue.Event {
	groups := make(map[Protocol][]queue.Event)
	for _, e := range batch {
		p := ProtocolFor(e.PathType)
		groups[p] = append(groups[p], e)
	}
	return groups
}

func eventIDs(evs []queue.Event) []string {
	ids := make([]string, len(evs))
	for i, e := range evs {
		ids[i] = e.ID
	}
	return ids
}
