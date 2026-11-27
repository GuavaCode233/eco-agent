// Package agent 是 Eco-Agent 的監督者迴圈（docs/Eco-Agent_系統匣UI規劃.md §3、U1）。
//
// 它把原本 cmd/eco-agent 的直線流程（綁定 → 三路徑 → 上傳 → 結束）改為可反覆進出的狀態機，
// 讓 UI（本機綁定頁、系統匣）能在不重啟程式的情況下解綁、重新綁定、暫停綁定：
//
//	loop:
//	  1. 綁定階段：綁定中 ⇄ 尚未綁定（閒置），直到綁定成功
//	  2. 已綁定階段：開子 ctx 啟動三路徑 + uploader.Run，直到
//	       - 員工解綁           → 回到 1
//	       - 偵測到撤銷（401/403）→ 捨棄佇列殘留 → 已撤銷，等 Rebind() → 回到 1
//	       - Refresh Token 失效 → 回到 1（enroll 已清本機憑證）
//	       - 關機訊號           → 結束（uploader 搶送零頭）
//
// 綁定重試節奏（§2）：綁定碼逾時（員工沒掃）立即換新碼；後端錯誤才等 retryInterval。
// PauseBinding（綁定頁關閉）取消進行中的綁定並進入閒置，閒置時完全不打後端，直到
// StartBinding（重新開頁、系統匣「開始綁定」、點擊提醒通知）。無 UI 的 headless 模式沒有人
// 呼叫 PauseBinding，因此維持持續換碼。
package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"eco-agent/internal/enroll"
	"eco-agent/internal/queue"
	"eco-agent/internal/uploader"
)

// State 是 Agent 的綁定狀態（§3「綁定狀態流程」）。
type State string

const (
	StateBinding State = "binding" // 綁定中：持續索取綁定碼並輪詢核銷
	StateIdle    State = "idle"    // 尚未綁定（閒置）：綁定頁已關閉，不打後端
	StateBound   State = "bound"   // 已綁定：三路徑與上傳運作中
	StateRevoked State = "revoked" // 已撤銷：停止上傳，等員工重新綁定
)

// 對外錯誤。
var (
	// ErrUnbindCancelled 表示員工在確認框拒絕捨棄殘留資料，解綁未執行、Agent 維持原狀。
	ErrUnbindCancelled = errors.New("agent: unbind cancelled by user")
	// ErrNotBound 表示操作需要已綁定狀態（如 FlushNow）。
	ErrNotBound = errors.New("agent: not bound")
)

// DefaultRetryInterval 是綁定遇後端錯誤（連不上、非 200）時的重試等待。
const DefaultRetryInterval = 30 * time.Second

// Binder 是 agent 依賴的綁定介面（*enroll.Enroller 滿足）。
type Binder interface {
	EnsureBound(ctx context.Context) error
	IsBound() (bool, error)
	Unbind(ctx context.Context) error
	ResetRevoked()
}

// Queue 是 agent 依賴的佇列介面（*queue.Queue 滿足）。
type Queue interface {
	Count(ctx context.Context) (int, error)
	Purge(ctx context.Context) (int, error)
}

// Uploader 是 agent 依賴的上傳介面（*uploader.Uploader 滿足）。
type Uploader interface {
	Run(ctx context.Context) error
	Flush(ctx context.Context, reason uploader.Reason) error
	LastResult() (uploader.Result, bool)
}

// SensorStarter 啟動三條感測路徑（各自 goroutine，ctx 取消即結束），每個 goroutine
// 須 wg.Add/Done；回傳實際啟動的路徑（未啟動者為優雅降級，見 DefaultSensors）。
type SensorStarter func(ctx context.Context, wg *sync.WaitGroup) map[queue.PathType]bool

// Status 是 Agent 的狀態快照，供 UI 顯示。
type Status struct {
	State            State
	BindingURI       string    // 綁定中時的 QR 內容；其他狀態為空
	BindingExpiresAt time.Time // BindingURI 的到期時間
	QueueCount       int
	LastUpload       *uploader.Result // 本次綁定期間最近一次上傳結果；尚無則為 nil
	Paths            map[queue.PathType]bool
	// DiscardedOnRevoke 為最近一次撤銷時捨棄的佇列筆數（供 UI 告知員工）；Rebind 後歸零。
	DiscardedOnRevoke int
}

// Agent 是監督者迴圈。並行安全；Run 只能呼叫一次。
type Agent struct {
	binder        Binder
	q             Queue
	newUploader   func() Uploader
	startSensors  SensorStarter
	log           *slog.Logger
	retryInterval time.Duration
	sessionCheck  time.Duration

	opMu sync.Mutex // 序列化 Unbind／Rebind 等會改變階段的操作

	mu            sync.Mutex
	state         State
	bindingURI    string
	bindingExp    time.Time
	cancelAttempt context.CancelFunc // 綁定中：取消進行中的 EnsureBound
	sess          *session           // 已綁定：目前的子 session
	discarded     int                // 最近一次撤銷時捨棄的佇列筆數
	lastUp        Uploader           // 最近一個 session 的 uploader（供 Status 讀上傳結果）
	wake          chan struct{}      // 狀態變更時喚醒 Run 迴圈（容量 1）
}

// session 是一次「已綁定階段」的執行資源。
type session struct {
	mu            sync.Mutex // 保護 sensors*／paths（boundPhase 收尾與 Unbind 可能並行）
	up            Uploader
	ctx           context.Context // session ctx（三路徑 ctx 的父 ctx）
	cancel        context.CancelFunc
	sensorsCancel context.CancelFunc
	sensorsWG     *sync.WaitGroup
	paths         map[queue.PathType]bool
	unbound       bool // 由 Unbind 結束（非撤銷、非關機）
}

// Option 客製化 Agent。
type Option func(*Agent)

// WithLogger 設定日誌器。
func WithLogger(l *slog.Logger) Option { return func(a *Agent) { a.log = l } }

// WithRetryInterval 覆寫綁定遇後端錯誤時的重試等待（測試用）。
func WithRetryInterval(d time.Duration) Option { return func(a *Agent) { a.retryInterval = d } }

// WithSessionCheckInterval 覆寫已綁定階段檢查憑證是否仍在的間隔（測試用）。
func WithSessionCheckInterval(d time.Duration) Option {
	return func(a *Agent) { a.sessionCheck = d }
}

// New 建立 Agent。newUploader 於每次進入已綁定階段時建立新的 uploader（撤銷後的
// uploader 會停在 stopped 狀態，重新綁定需要新的實例）。
//
// 綁定碼要顯示在 UI 上，呼叫端需把 enroll.WithBindingCodeHandler 接到 OnBindingCode。
func New(b Binder, q Queue, newUploader func() Uploader, startSensors SensorStarter, opts ...Option) *Agent {
	a := &Agent{
		binder:        b,
		q:             q,
		newUploader:   newUploader,
		startSensors:  startSensors,
		log:           slog.Default(),
		retryInterval: DefaultRetryInterval,
		sessionCheck:  time.Minute,
		state:         StateBinding,
		wake:          make(chan struct{}, 1),
	}
	for _, o := range opts {
		o(a)
	}
	return a
}

// OnBindingCode 接收 enroll 取得的新綁定碼（見 enroll.WithBindingCodeHandler）。
func (a *Agent) OnBindingCode(uri string, expiresAt time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.bindingURI, a.bindingExp = uri, expiresAt
}

// Run 執行監督者迴圈，直到 ctx 取消（關機）為止，回傳 ctx.Err()。
func (a *Agent) Run(ctx context.Context) error {
	for {
		if err := a.bindPhase(ctx); err != nil {
			return err
		}
		revoked := a.boundPhase(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if revoked {
			if err := a.waitRebind(ctx); err != nil {
				return err
			}
		}
	}
}

// bindPhase 停留在綁定中／閒置，直到綁定成功或 ctx 取消。
func (a *Agent) bindPhase(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if a.State() == StateIdle {
			if err := a.waitWake(ctx, 0); err != nil {
				return err
			}
			continue
		}

		attempt, cancel := context.WithCancel(ctx)
		a.mu.Lock()
		if a.state != StateBinding { // 進入嘗試前剛被 Pause
			a.mu.Unlock()
			cancel()
			continue
		}
		a.cancelAttempt = cancel
		a.mu.Unlock()

		err := a.binder.EnsureBound(attempt)

		a.mu.Lock()
		a.cancelAttempt = nil
		paused := a.state == StateIdle
		a.mu.Unlock()
		cancel()

		switch {
		case err == nil:
			return nil
		case ctx.Err() != nil:
			return ctx.Err()
		case paused:
			a.log.Info("agent: binding paused (binding page closed)")
		case errors.Is(err, enroll.ErrRevoked):
			// enroll 仍在撤銷終止態（不應發生：Rebind 會先 ResetRevoked）；改走已撤銷等待。
			a.setState(StateRevoked)
			a.discardOnRevoke()
			if err := a.waitRebind(ctx); err != nil {
				return err
			}
		case errors.Is(err, enroll.ErrBindingCodeExpired):
			a.log.Info("agent: binding code expired, requesting a new one")
		default:
			a.log.Warn("agent: binding failed, will retry", "err", err, "retry_in", a.retryInterval)
			if err := a.waitWake(ctx, a.retryInterval); err != nil {
				return err
			}
		}
	}
}

// boundPhase 執行一次已綁定階段，回傳是否因撤銷結束。
func (a *Agent) boundPhase(parent context.Context) (revoked bool) {
	sctx, cancel := context.WithCancel(parent)
	s := &session{up: a.newUploader(), ctx: sctx, cancel: cancel}
	a.startSessionSensors(s)

	a.mu.Lock()
	a.state = StateBound
	a.bindingURI, a.bindingExp = "", time.Time{}
	a.sess = s
	a.lastUp = s.up
	a.mu.Unlock()
	a.log.Info("agent: device bound, sensors and uploader started")

	upDone := make(chan error, 1)
	go func() { upDone <- s.up.Run(sctx) }()

	check := time.NewTicker(a.sessionCheck)
	defer check.Stop()

	var upErr error
loop:
	for {
		select {
		case upErr = <-upDone:
			break loop
		case <-check.C:
			// Refresh Token 失效時 enroll 會清本機憑證並回 ErrNotBound，但 uploader 只會
			// 略過 flush、不會結束；在此偵測後結束 session，回到綁定階段。
			if bound, err := a.binder.IsBound(); err == nil && !bound {
				a.log.Warn("agent: credentials gone (refresh token rejected), returning to binding")
				cancel()
			}
		}
	}

	cancel()
	a.stopSessionSensors(s)

	a.mu.Lock()
	a.sess = nil
	unbound := s.unbound
	switch {
	case parent.Err() != nil:
		// 關機：狀態不重要。
	case errors.Is(upErr, uploader.ErrStopped) && !unbound:
		a.state = StateRevoked
		revoked = true
	default:
		a.state = StateBinding
	}
	a.mu.Unlock()

	if revoked {
		a.log.Warn("agent: credentials revoked, upload stopped; waiting for rebind")
		a.discardOnRevoke()
	}
	return revoked
}

// discardOnRevoke 於撤銷時清空佇列殘留（與解綁「先送再丟」同一規則，§2）。
//
// 撤銷後後端已不接受這台裝置的舊綁定，殘留資料無法再送出；若留到 Rebind 後才送，會以新
// 綁定的 id_token 歸戶，換人重綁時就算到別人名下。呼叫時三路徑已停止，不會再有新資料寫入。
func (a *Agent) discardOnRevoke() {
	n, err := a.q.Purge(context.Background())
	if err != nil {
		a.log.Warn("agent: discard queue on revoke failed", "err", err)
		return
	}
	a.mu.Lock()
	a.discarded = n
	a.mu.Unlock()
	if n > 0 {
		a.log.Warn("agent: discarded queued events of the revoked binding", "count", n)
	}
}

// startSessionSensors 啟動三路徑；session 已結束或三路徑已在執行時為 no-op。
func (a *Agent) startSessionSensors(s *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil || s.sensorsCancel != nil {
		return
	}
	sctx, cancel := context.WithCancel(s.ctx)
	s.sensorsCancel = cancel
	s.sensorsWG = &sync.WaitGroup{}
	s.paths = a.startSensors(sctx, s.sensorsWG)
}

// stopSessionSensors 停止三路徑並等待其 goroutine 結束；未在執行時為 no-op。
func (a *Agent) stopSessionSensors(s *session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sensorsCancel == nil {
		return
	}
	s.sensorsCancel()
	s.sensorsWG.Wait()
	s.sensorsCancel = nil
}

// waitRebind 停在已撤銷狀態，直到 Rebind() 或 ctx 取消。
func (a *Agent) waitRebind(ctx context.Context) error {
	for a.State() == StateRevoked {
		if err := a.waitWake(ctx, 0); err != nil {
			return err
		}
	}
	return nil
}

// waitWake 等待狀態變更通知、逾時（d>0）或 ctx 取消。
func (a *Agent) waitWake(ctx context.Context, d time.Duration) error {
	var timeout <-chan time.Time
	if d > 0 {
		t := time.NewTimer(d)
		defer t.Stop()
		timeout = t.C
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-a.wake:
	case <-timeout:
	}
	return nil
}

func (a *Agent) notify() {
	select {
	case a.wake <- struct{}{}:
	default:
	}
}

func (a *Agent) setState(s State) {
	a.mu.Lock()
	a.state = s
	a.mu.Unlock()
	a.notify()
}

// State 回傳目前狀態。
func (a *Agent) State() State {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

// PauseBinding 於綁定頁關閉時呼叫：綁定中 → 尚未綁定（閒置），並取消進行中的綁定。
// 其他狀態下為 no-op。回傳是否真的進入閒置（供提醒通知判斷）。
func (a *Agent) PauseBinding() bool {
	a.mu.Lock()
	if a.state != StateBinding {
		a.mu.Unlock()
		return false
	}
	a.state = StateIdle
	a.bindingURI, a.bindingExp = "", time.Time{}
	if a.cancelAttempt != nil {
		a.cancelAttempt()
	}
	a.mu.Unlock()
	a.notify()
	return true
}

// StartBinding 從尚未綁定（閒置）回到綁定中；綁定中正在等待重試時則立即重試。
// 其他狀態下為 no-op。
func (a *Agent) StartBinding() {
	a.mu.Lock()
	if a.state == StateIdle {
		a.state = StateBinding
	}
	a.mu.Unlock()
	a.notify()
}

// Rebind 於已撤銷時解除終止態並回到綁定中；其他狀態下為 no-op。
func (a *Agent) Rebind() {
	a.opMu.Lock()
	defer a.opMu.Unlock()
	a.mu.Lock()
	if a.state != StateRevoked {
		a.mu.Unlock()
		return
	}
	a.binder.ResetRevoked()
	a.state = StateBinding
	a.discarded = 0
	a.mu.Unlock()
	a.notify()
}

// FlushNow 立即上傳佇列（系統匣「立即上傳」、喚醒後補送）。未綁定回 ErrNotBound。
func (a *Agent) FlushNow(ctx context.Context) error {
	a.mu.Lock()
	s := a.sess
	a.mu.Unlock()
	if s == nil {
		return ErrNotBound
	}
	return s.up.Flush(ctx, uploader.ReasonManual)
}

// Unbind 員工主動解綁，採「先送再丟」（§2）：
//  1. 停止三路徑（避免解綁途中寫入新資料）
//  2. 立即上傳一次
//  3. 仍有 N 筆送不出去時呼叫 confirm(N)；拒絕則恢復三路徑並回 ErrUnbindCancelled
//  4. 同意（或無殘留）→ 清空佇列 → enroll.Unbind（通知後端並清本機憑證）→ 回到綁定中
//
// Run 未在執行時（如 CLI -unbind）也可呼叫：以 newUploader 建立臨時 uploader 上傳。
// confirm 為 nil 視同一律同意。
func (a *Agent) Unbind(ctx context.Context, confirm func(leftover int) bool) error {
	a.opMu.Lock()
	defer a.opMu.Unlock()

	a.mu.Lock()
	s := a.sess
	binding := a.cancelAttempt != nil
	a.mu.Unlock()
	if binding {
		// 綁定中 EnsureBound 持有 Enroller 鎖直到綁定碼逾時；此時本就沒有可解除的綁定。
		return ErrNotBound
	}

	up := a.newUploaderFor(s)
	if s != nil {
		a.stopSessionSensors(s)
	}
	restore := func() {
		if s != nil {
			a.startSessionSensors(s)
		}
	}

	if err := up.Flush(ctx, uploader.ReasonManual); err != nil {
		if errors.Is(err, uploader.ErrStopped) {
			// 已被撤銷：後端已不接受這台裝置，殘留資料同樣無法送出；照常往下詢問捨棄。
			a.log.Warn("agent: unbind: device already revoked")
		} else {
			a.log.Warn("agent: unbind: flush failed", "err", err)
		}
	}

	n, err := a.q.Count(ctx)
	if err != nil {
		restore()
		return fmt.Errorf("agent: unbind: count queue: %w", err)
	}
	if n > 0 {
		if confirm != nil && !confirm(n) {
			restore()
			return ErrUnbindCancelled
		}
		purged, err := a.q.Purge(ctx)
		if err != nil {
			restore()
			return fmt.Errorf("agent: unbind: purge queue: %w", err)
		}
		a.log.Warn("agent: unbind: discarded events that could not be uploaded", "count", purged)
	}

	if err := a.binder.Unbind(ctx); err != nil {
		restore()
		return fmt.Errorf("agent: unbind: %w", err)
	}
	// 解綁後回到乾淨狀態：若先前已被撤銷，一併解除 enroll 的撤銷終止態，讓之後能重新綁定。
	a.binder.ResetRevoked()
	a.log.Info("agent: device unbound")

	a.mu.Lock()
	if s != nil {
		s.unbound = true
		s.cancel() // 結束已綁定階段；boundPhase 會把狀態設回綁定中
	}
	if a.state == StateRevoked {
		a.state = StateBinding // 撤銷與解綁並行時，boundPhase 可能已先設為已撤銷
	}
	a.mu.Unlock()
	a.notify()
	return nil
}

func (a *Agent) newUploaderFor(s *session) Uploader {
	if s != nil {
		return s.up
	}
	return a.newUploader()
}

// Status 回傳狀態快照。
func (a *Agent) Status(ctx context.Context) Status {
	a.mu.Lock()
	st := Status{
		State:             a.state,
		BindingURI:        a.bindingURI,
		BindingExpiresAt:  a.bindingExp,
		DiscardedOnRevoke: a.discarded,
	}
	sess := a.sess
	up := a.lastUp
	a.mu.Unlock()

	if sess != nil {
		sess.mu.Lock()
		st.Paths = make(map[queue.PathType]bool, len(sess.paths))
		for k, v := range sess.paths {
			st.Paths[k] = v
		}
		sess.mu.Unlock()
	}
	if up != nil {
		if r, ok := up.LastResult(); ok {
			st.LastUpload = &r
		}
	}
	if n, err := a.q.Count(ctx); err == nil {
		st.QueueCount = n
	}
	return st
}
