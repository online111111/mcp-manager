package bridge

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/downstream"
)

// A generation owns one initialized session and all work that may publish its
// directory. Only the coordinator can replace it; old results cannot publish.
type hubGeneration struct {
	session         *downstream.Session
	cancel          context.CancelFunc
	ready           bool // guarded by sessionMu
	terminal        atomic.Pointer[hubTransportError]
	transportFailed atomic.Bool
}

type hubFailure struct {
	generation *hubGeneration
	err        error
}

func (g *hubGeneration) close() {
	g.cancel()
	_ = g.session.Close()
}

func (b *Bridge) connectHub(ctx context.Context) (*hubGeneration, []*mcp.Tool, error) {
	lifetime, cancel := context.WithCancel(ctx)
	// The SDK opens an initial standalone SSE GET using a detached context.
	// Bound that header wait as well as initialize/discovery, without retaining
	// the startup deadline on the established session lifetime.
	startup := time.AfterFunc(b.opts.StartupTimeout, cancel)
	defer startup.Stop()
	generation := &hubGeneration{cancel: cancel}
	base := b.opts.BaseRoundTripper
	if base == nil {
		base = http.DefaultTransport
	}
	session, err := downstream.DialHTTP(lifetime, downstream.HTTPOptions{
		Endpoint: b.opts.Endpoint, Headers: b.opts.Headers,
		BaseRoundTripper: &hubRoundTripper{base: base, ctx: lifetime, generation: generation},
		// Recovery always initializes a fresh session. In particular, never
		// resume/replay an uncertain tools/call via the SDK's HTTP retry loop.
		MaxRetries: -1, StartupTimeout: b.opts.StartupTimeout,
		ClientInfo: b.opts.ClientInfo, KeepAlive: b.opts.KeepAlive,
	})
	if err != nil {
		cancel()
		if terminal := generation.terminal.Load(); terminal != nil {
			err = terminal
		}
		return nil, nil, err
	}
	generation.session = session
	listCtx, listCancel := context.WithTimeout(lifetime, b.opts.StartupTimeout)
	defer listCancel()
	tools, err := session.ListAllTools(listCtx)
	if err != nil {
		generation.close()
		if terminal := generation.terminal.Load(); terminal != nil {
			err = terminal
		}
		return nil, nil, err
	}
	if !startup.Stop() || lifetime.Err() != nil {
		generation.close()
		return nil, nil, context.DeadlineExceeded
	}
	return generation, tools, nil
}

func (b *Bridge) signalStateLocked() {
	close(b.stateChanged)
	b.stateChanged = make(chan struct{})
}

func (b *Bridge) awaitHub(ctx context.Context) (*hubGeneration, error) {
	for {
		b.sessionMu.Lock()
		generation := b.current
		changed := b.stateChanged
		if generation != nil && generation.ready && !generation.session.IsClosed() {
			b.sessionMu.Unlock()
			return generation, nil
		}
		b.sessionMu.Unlock()
		if b.closed.Load() {
			return nil, ErrBridgeClosed
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (b *Bridge) requestRecovery(generation *hubGeneration, err error) {
	if err == nil {
		err = ErrHubSessionClosed
	}
	var rpcErr *jsonrpc.Error
	if errors.As(err, &rpcErr) && generation.terminal.Load() == nil && !generation.transportFailed.Load() && !generation.session.IsClosed() {
		return // A tool/application error is not a lost transport session.
	}
	b.sessionMu.Lock()
	defer b.sessionMu.Unlock()
	if b.current != generation || !generation.ready || b.closed.Load() {
		return
	}
	generation.ready = false
	b.signalStateLocked()
	b.recoverCh <- hubFailure{generation, err}
}

func (b *Bridge) watchHub(ctx context.Context, generation *hubGeneration) {
	go b.watchToolListChanges(ctx, generation)
	go func() {
		err := generation.session.RawSession().Wait()
		if ctx.Err() == nil {
			b.requestRecovery(generation, err)
		}
	}()
}

func (b *Bridge) closeHub() {
	b.sessionMu.Lock()
	generation := b.current
	b.current = nil
	b.signalStateLocked()
	b.sessionMu.Unlock()
	if generation != nil {
		generation.close()
	}
}

func (b *Bridge) recoverHub(ctx context.Context) {
	for {
		var failure hubFailure
		select {
		case <-ctx.Done():
			return
		case failure = <-b.recoverCh:
		}
		failure.generation.close()
		var last error
		if terminal := failure.generation.terminal.Load(); terminal != nil {
			b.failHub(terminal)
			return
		}
		delay := b.opts.ReconnectDelay
		for attempt := 0; attempt < b.opts.ReconnectAttempts; attempt++ {
			if attempt > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
				delay *= 2
				if delay > 5*time.Second {
					delay = 5 * time.Second
				}
			}
			generation, tools, err := b.connectHub(ctx)
			if err != nil {
				last = err
				var terminal *hubTransportError
				if errors.As(err, &terminal) {
					break
				}
				continue
			}
			b.sessionMu.Lock()
			if ctx.Err() != nil || b.closed.Load() {
				b.sessionMu.Unlock()
				generation.close()
				return
			}
			// Registry publication and ready transition are one atomic admission
			// point. No tool may use the replacement before full discovery.
			b.updateRegisteredTools(tools)
			b.current = generation
			generation.ready = true
			b.syncedSeq.Store(b.changeSeq.Load())
			b.syncNeeded.Store(false)
			b.signalStateLocked()
			b.sessionMu.Unlock()
			b.watchHub(ctx, generation)
			last = nil
			break
		}
		if ctx.Err() != nil {
			return
		}
		if last != nil {
			b.failHub(fmt.Errorf("recovery exhausted: %w", last))
			return
		}
	}
}

func (b *Bridge) failHub(err error) {
	b.hubErrMu.Lock()
	b.hubFailureErr = err
	b.hubErrMu.Unlock()
	b.cancel(errors.Join(ErrHubSessionClosed, err))
}
