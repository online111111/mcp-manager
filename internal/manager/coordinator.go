package manager

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/catalog"
	"github.com/online111111/mcp-manager/internal/config"
)

type configChangeType int

const (
	changeNone configChangeType = iota
	changeToolsOnly
	changeTimeoutOnly
	changeToolsAndTimeout
	changeConnection
)

var (
	defaultBackoffDelays = []time.Duration{
		1 * time.Second,
		2 * time.Second,
		4 * time.Second,
		8 * time.Second,
		16 * time.Second,
		30 * time.Second,
	}
	defaultReadyResetDuration = 60 * time.Second
	defaultDrainTimeout       = 10 * time.Second
)

// Coordinator manages the lifecycle, backoff, reconnection, and generation of one downstream server.
type Coordinator struct {
	serverID string
	pub      catalog.Publisher
	factory  SessionFactory
	limiter  chan struct{}

	backoffDelays []time.Duration
	readyResetDur time.Duration
	drainTimeout  time.Duration

	// publishMu linearizes desired-revision changes with catalog publication.
	// Network discovery is deliberately performed without this lock; the result
	// must pass a revision check after acquiring publishMu before it can commit.
	publishMu sync.Mutex
	mu        sync.Mutex

	desired         DesiredServer
	currentGen      *Generation
	publishedGen    *Generation // generation owning the currently published schema
	state           string
	consecutiveFail int
	lastError       string
	lastConnected   time.Time
	lastDisconnect  time.Time
	publishedCount  int
	cachedTools     []*mcp.Tool
	everSucceeded   bool
	genSeq          uint64

	// operationCancel owns the currently connecting/discovering attempt. Config
	// changes cancel it so a superseded startupTimeout cannot delay reconciliation.
	operationCancel   context.CancelFunc
	operationRevision int64

	ctx           context.Context
	cancel        context.CancelFunc
	reconcileCh   chan struct{}
	toolChangedCh chan struct{}
	refreshCh     chan chan error
	stopped       bool
	stoppedCh     chan struct{}
}

func newCoordinator(
	serverID string,
	desired DesiredServer,
	pub catalog.Publisher,
	factory SessionFactory,
	limiter chan struct{},
	backoffDelays []time.Duration,
	readyResetDur time.Duration,
	drainTimeout time.Duration,
) *Coordinator {
	if len(backoffDelays) == 0 {
		backoffDelays = defaultBackoffDelays
	}
	if readyResetDur <= 0 {
		readyResetDur = defaultReadyResetDuration
	}
	if drainTimeout <= 0 {
		drainTimeout = defaultDrainTimeout
	}

	c := &Coordinator{
		serverID:      serverID,
		desired:       desired,
		pub:           pub,
		factory:       factory,
		limiter:       limiter,
		backoffDelays: backoffDelays,
		readyResetDur: readyResetDur,
		drainTimeout:  drainTimeout,
		state:         StateDisabled,
		reconcileCh:   make(chan struct{}, 1),
		toolChangedCh: make(chan struct{}, 1),
		refreshCh:     make(chan chan error, 1),
		stoppedCh:     make(chan struct{}),
	}
	if desired.ResolvedConfig.Enabled {
		c.state = StateConnecting
	}
	return c
}

func (c *Coordinator) Start(parentCtx context.Context) {
	c.mu.Lock()
	if c.ctx != nil {
		c.mu.Unlock()
		return
	}
	c.ctx, c.cancel = context.WithCancel(parentCtx)
	c.mu.Unlock()
	go c.run()
}

// Stop closes admission immediately but intentionally keeps the coordinator
// context alive while the current generation drains. Generation contexts are
// derived from c.ctx, so cancelling c.ctx before drain would abort in-flight
// calls instead of allowing them to finish within drainTimeout.
func (c *Coordinator) Stop() {
	c.publishMu.Lock()
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		c.publishMu.Unlock()
		return
	}
	c.stopped = true
	if c.operationCancel != nil {
		c.operationCancel()
		c.operationCancel = nil
	}
	cancel := c.cancel
	gen := c.currentGen
	c.currentGen = nil
	c.state = StateClosed
	c.mu.Unlock()
	c.publishMu.Unlock()

	if gen != nil {
		gen.drain(c.drainTimeout)
		_ = gen.Close()
	}
	if cancel != nil {
		cancel()
	}
	close(c.stoppedCh)
}

// UpdateConfig applies a new desired revision. Publication and revision mutation
// share publishMu so an older discovery result can never commit after this update.
func (c *Coordinator) UpdateConfig(newDesired DesiredServer) bool {
	c.publishMu.Lock()
	defer c.publishMu.Unlock()

	c.mu.Lock()
	oldCfg := c.desired.ResolvedConfig
	newCfg := newDesired.ResolvedConfig
	change := diffConfig(oldCfg, newCfg)
	if change == changeNone {
		c.mu.Unlock()
		return false
	}

	c.desired = newDesired
	var opCancel context.CancelFunc
	if c.operationCancel != nil && c.operationRevision != newDesired.Revision {
		opCancel = c.operationCancel
	}

	switch change {
	case changeToolsOnly:
		tools := append([]*mcp.Tool(nil), c.cachedTools...)
		state := c.state
		c.mu.Unlock()
		if opCancel != nil {
			opCancel()
		}
		if state == StateReady && len(tools) > 0 && c.pub != nil {
			disabled := disabledNames(newCfg.DisabledTools)
			snap, err := c.pub.PublishServer(c.serverID, tools, disabled)
			if err == nil && snap != nil {
				c.mu.Lock()
				if c.desired.Revision == newDesired.Revision {
					c.publishedCount = snap.PublishedCount(c.serverID)
				}
				c.mu.Unlock()
			}
		}
		return false

	case changeTimeoutOnly:
		gen := c.currentGen
		c.mu.Unlock()
		if opCancel != nil {
			opCancel()
		}
		if gen != nil {
			gen.SetCallTimeout(newCfg.CallTimeout)
		}
		return false

	case changeToolsAndTimeout:
		gen := c.currentGen
		tools := append([]*mcp.Tool(nil), c.cachedTools...)
		state := c.state
		c.mu.Unlock()
		if opCancel != nil {
			opCancel()
		}
		if gen != nil {
			gen.SetCallTimeout(newCfg.CallTimeout)
		}
		if state == StateReady && len(tools) > 0 && c.pub != nil {
			disabled := disabledNames(newCfg.DisabledTools)
			snap, err := c.pub.PublishServer(c.serverID, tools, disabled)
			if err == nil && snap != nil {
				c.mu.Lock()
				if c.desired.Revision == newDesired.Revision {
					c.publishedCount = snap.PublishedCount(c.serverID)
				}
				c.mu.Unlock()
			}
		}
		return false

	case changeConnection:
		c.mu.Unlock()
		if opCancel != nil {
			opCancel()
		}
		select {
		case c.reconcileCh <- struct{}{}:
		default:
		}
		return true
	default:
		c.mu.Unlock()
		return false
	}
}

func (c *Coordinator) Refresh(ctx context.Context) error {
	errCh := make(chan error, 1)
	select {
	case c.refreshCh <- errCh:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.stoppedCh:
		return ErrManagerStopped
	}
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-c.stoppedCh:
		return ErrManagerStopped
	}
}

func (c *Coordinator) AcquireLease() (*Lease, error) {
	c.mu.Lock()
	gen := c.currentGen
	state := c.state
	c.mu.Unlock()
	if state != StateReady || gen == nil {
		return nil, ErrNotAdmitting
	}
	return gen.acquire()
}

func (c *Coordinator) Status() ServiceStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	active := 0
	var genID uint64
	var activeRevision int64
	if c.currentGen != nil {
		active = c.currentGen.Active()
		genID = c.currentGen.id
		activeRevision = c.currentGen.revision
	}
	return ServiceStatus{
		ID:                  c.serverID,
		Enabled:             c.desired.ResolvedConfig.Enabled,
		State:               c.state,
		DesiredRevision:     c.desired.Revision,
		GenerationID:        genID,
		ActiveRevision:      activeRevision,
		ActiveLeases:        active,
		MaxConcurrency:      c.desired.ResolvedConfig.MaxConcurrency,
		LastError:           c.lastError,
		ConsecutiveFailures: c.consecutiveFail,
		LastConnectedAt:     c.lastConnected,
		LastDisconnectAt:    c.lastDisconnect,
		PublishedTools:      c.publishedCount,
	}
}

func (c *Coordinator) onToolListChanged() {
	select {
	case c.toolChangedCh <- struct{}{}:
	default:
	}
}

func (c *Coordinator) run() {
	defer c.Stop()
	for {
		if c.ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		if c.stopped {
			c.mu.Unlock()
			return
		}
		enabled := c.desired.ResolvedConfig.Enabled
		c.mu.Unlock()
		if !enabled {
			c.handleDisabled()
			continue
		}
		c.handleConnectingAndReady()
	}
}

func (c *Coordinator) handleDisabled() {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.state = StateDisabled
	gen := c.currentGen
	c.currentGen = nil
	c.mu.Unlock()

	if c.pub != nil {
		c.publishMu.Lock()
		_, _ = c.pub.RemoveServer(c.serverID)
		c.publishMu.Unlock()
	}
	if gen != nil {
		gen.drain(c.drainTimeout)
		_ = gen.Close()
	}
	select {
	case <-c.reconcileCh:
		return
	case ch := <-c.refreshCh:
		ch <- fmt.Errorf("server %q is disabled", c.serverID)
	case <-c.ctx.Done():
		return
	}
}

func (c *Coordinator) handleConnectingAndReady() {
	c.mu.Lock()
	if c.stopped || c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.state = StateConnecting
	targetRev := c.desired.Revision
	cfg := c.desired.ResolvedConfig
	opCtx, opCancel := context.WithCancel(c.ctx)
	c.operationCancel = opCancel
	c.operationRevision = targetRev
	c.mu.Unlock()
	defer func() {
		opCancel()
		c.mu.Lock()
		if c.operationRevision == targetRev {
			c.operationCancel = nil
		}
		c.mu.Unlock()
	}()

	c.drainAndCloseOldGen()

	select {
	case c.limiter <- struct{}{}:
	case <-opCtx.Done():
		return
	case <-c.reconcileCh:
		return
	}

	sess, procCloser, err := c.factory.CreateSession(opCtx, cfg, c.onToolListChanged)
	<-c.limiter
	if err != nil {
		if opCtx.Err() != nil || !c.targetCurrent(targetRev) {
			return
		}
		c.handleConnectFailure(err)
		return
	}
	closeAttempt := func() {
		_ = sess.Close()
		if procCloser != nil {
			_ = procCloser.Close()
		}
	}

	if !c.targetCurrent(targetRev) {
		closeAttempt()
		return
	}

	discoverCtx := opCtx
	var discoverCancel context.CancelFunc
	if cfg.StartupTimeout > 0 {
		discoverCtx, discoverCancel = context.WithTimeout(opCtx, cfg.StartupTimeout)
	}
	tools, err := sess.ListAllTools(discoverCtx)
	if discoverCancel != nil {
		discoverCancel()
	}
	if err != nil {
		closeAttempt()
		if opCtx.Err() != nil || !c.targetCurrent(targetRev) {
			return
		}
		c.handleConnectFailure(fmt.Errorf("tool discovery failed: %w", err))
		return
	}

	c.publishMu.Lock()
	if !c.targetCurrent(targetRev) {
		c.publishMu.Unlock()
		closeAttempt()
		return
	}
	disabled := disabledNames(cfg.DisabledTools)
	snap, err := c.pub.PublishServer(c.serverID, tools, disabled)
	if err != nil {
		c.publishMu.Unlock()
		closeAttempt()
		c.handleConnectFailure(fmt.Errorf("publish tools failed: %w", err))
		return
	}

	c.mu.Lock()
	if c.desired.Revision != targetRev || !c.desired.ResolvedConfig.Enabled || c.stopped {
		c.mu.Unlock()
		_, _ = c.pub.RemoveServer(c.serverID)
		c.publishMu.Unlock()
		closeAttempt()
		return
	}
	c.genSeq++
	genID := c.genSeq
	genCtx, genCancel := context.WithCancel(c.ctx)
	gen := newGeneration(genID, c.serverID, sess, procCloser, cfg.MaxConcurrency, cfg.CallTimeout, genCtx, genCancel, targetRev)
	c.currentGen = gen
	c.publishedGen = gen
	c.cachedTools = tools
	c.publishedCount = snap.PublishedCount(c.serverID)
	c.state = StateReady
	// Startup cancellation must no longer own a healthy session. Tool/filter
	// and call-timeout edits are hot changes and must not cancel its lifetime.
	c.operationCancel = nil
	c.lastConnected = time.Now()
	c.lastError = ""
	c.everSucceeded = true
	c.mu.Unlock()
	c.publishMu.Unlock()

	c.readyLoop(gen, sess)
}

func (c *Coordinator) targetCurrent(rev int64) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return !c.stopped && c.desired.ResolvedConfig.Enabled && c.desired.Revision == rev
}

func disabledNames(disabled map[string]struct{}) []string {
	out := make([]string, 0, len(disabled))
	for name := range disabled {
		out = append(out, name)
	}
	return out
}

func (c *Coordinator) drainAndCloseOldGen() {
	c.mu.Lock()
	gen := c.currentGen
	c.currentGen = nil
	c.mu.Unlock()
	if gen != nil {
		gen.drain(c.drainTimeout)
		_ = gen.Close()
	}
}

func (c *Coordinator) readyLoop(gen *Generation, sess Session) {
	resetTimer := time.NewTimer(c.readyResetDur)
	defer resetTimer.Stop()
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-resetTimer.C:
			c.mu.Lock()
			if c.state == StateReady {
				c.consecutiveFail = 0
			}
			c.mu.Unlock()
		case <-c.toolChangedCh:
			_ = c.refreshTools(gen, sess)
		case ch := <-c.refreshCh:
			ch <- c.refreshTools(gen, sess)
		case <-c.reconcileCh:
			c.mu.Lock()
			c.state = StateDraining
			c.mu.Unlock()
			gen.drain(c.drainTimeout)
			_ = gen.Close()
			return
		case <-ticker.C:
			if sess.IsClosed() || gen.IsClosed() {
				c.mu.Lock()
				c.state = StateUnavailable
				c.lastDisconnect = time.Now()
				c.lastError = "downstream session closed"
				c.mu.Unlock()
				gen.drain(c.drainTimeout)
				_ = gen.Close()
				c.handleConnectFailure(errors.New("downstream session closed"))
				return
			}
		case <-c.ctx.Done():
			gen.drain(c.drainTimeout)
			_ = gen.Close()
			return
		}
	}
}

func (c *Coordinator) refreshTools(gen *Generation, sess Session) error {
	c.mu.Lock()
	targetRev := c.desired.Revision
	cfg := c.desired.ResolvedConfig
	if c.currentGen != gen || c.state != StateReady {
		c.mu.Unlock()
		return fmt.Errorf("tool refresh superseded by generation change")
	}
	c.mu.Unlock()

	discoverCtx := c.ctx
	var cancel context.CancelFunc
	if cfg.StartupTimeout > 0 {
		discoverCtx, cancel = context.WithTimeout(c.ctx, cfg.StartupTimeout)
		defer cancel()
	}
	tools, err := sess.ListAllTools(discoverCtx)
	if err != nil {
		return err
	}

	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	c.mu.Lock()
	if c.stopped || c.currentGen != gen || c.state != StateReady || c.desired.Revision != targetRev {
		c.mu.Unlock()
		return fmt.Errorf("tool refresh superseded by newer configuration")
	}
	c.mu.Unlock()

	disabled := disabledNames(cfg.DisabledTools)
	snap, err := c.pub.PublishServer(c.serverID, tools, disabled)
	if err != nil {
		return err
	}
	c.mu.Lock()
	if c.currentGen == gen && c.desired.Revision == targetRev {
		c.cachedTools = tools
		c.publishedCount = snap.PublishedCount(c.serverID)
	}
	c.mu.Unlock()
	return nil
}

func (c *Coordinator) handleConnectFailure(err error) {
	c.mu.Lock()
	if c.stopped || c.ctx.Err() != nil {
		c.mu.Unlock()
		return
	}
	c.consecutiveFail++
	c.lastError = sanitizeError(err)
	c.lastDisconnect = time.Now()
	c.state = StateUnavailable
	fails := c.consecutiveFail
	c.mu.Unlock()

	delay := c.computeBackoff(fails)
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.state = StateBackoff
	c.mu.Unlock()
	select {
	case <-time.After(delay):
	case <-c.reconcileCh:
	case ch := <-c.refreshCh:
		ch <- nil
	case <-c.ctx.Done():
	}
}

func (c *Coordinator) computeBackoff(failCount int) time.Duration {
	if failCount <= 0 {
		failCount = 1
	}
	idx := failCount - 1
	if idx >= len(c.backoffDelays) {
		idx = len(c.backoffDelays) - 1
	}
	base := c.backoffDelays[idx]
	factor := 0.8 + 0.4*rand.Float64()
	return time.Duration(float64(base) * factor)
}

func diffConfig(old, new config.ResolvedServer) configChangeType {
	if old.Enabled != new.Enabled ||
		old.Type != new.Type ||
		old.Command != new.Command ||
		old.Cwd != new.Cwd ||
		old.URL != new.URL ||
		old.MaxConcurrency != new.MaxConcurrency ||
		old.StartupTimeout != new.StartupTimeout ||
		!slicesEqual(old.Args, new.Args) ||
		!mapsEqual(old.Env, new.Env) ||
		!mapsEqual(old.Headers, new.Headers) {
		return changeConnection
	}
	toolsDiff := !toolSetsEqual(old.DisabledTools, new.DisabledTools)
	timeoutDiff := old.CallTimeout != new.CallTimeout
	if toolsDiff && timeoutDiff {
		return changeToolsAndTimeout
	}
	if toolsDiff {
		return changeToolsOnly
	}
	if timeoutDiff {
		return changeTimeoutOnly
	}
	return changeNone
}

func slicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mapsEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if other, exists := b[k]; !exists || other != v {
			return false
		}
	}
	return true
}

func toolSetsEqual(a, b map[string]struct{}) bool {
	if len(a) != len(b) {
		return false
	}
	for k := range a {
		if _, ok := b[k]; !ok {
			return false
		}
	}
	return true
}

func sanitizeError(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "process_not_found"
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return "timeout"
		}
		return "network_error"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection refused"), strings.Contains(message, "connectex"):
		return "connection_refused"
	case strings.Contains(message, "no such host"), strings.Contains(message, "lookup"):
		return "dns_error"
	case strings.Contains(message, "initialize"):
		return "initialize_failed"
	case strings.Contains(message, "launch stdio"), strings.Contains(message, "start worker"), strings.Contains(message, "start downstream"):
		return "process_start_failed"
	case strings.Contains(message, "session closed"), strings.Contains(message, "eof"), strings.Contains(message, "closed pipe"):
		return "session_closed"
	default:
		return "downstream_error"
	}
}
