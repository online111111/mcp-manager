package manager

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/catalog"
	"github.com/online111111/mcp-manager/internal/config"
)

func TestValidatedAdmissionConcurrentFilterReloadNoDeadlock(t *testing.T) {
	pub := NewCatalogPublisher(catalog.NewCatalog())
	tools := []*mcp.Tool{{Name: "tool", InputSchema: map[string]any{"type": "object"}}}
	if _, err := pub.PublishServer("srv", tools, nil); err != nil {
		t.Fatal(err)
	}
	cfg := config.ResolvedServer{ID: "srv", Enabled: true, CallTimeout: time.Second, MaxConcurrency: 64}
	coord := newCoordinator("srv", DesiredServer{Revision: 1, ResolvedConfig: cfg}, pub, nil, nil, nil, 0, time.Millisecond)
	genCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gen := newGeneration(1, "srv", nil, nil, 64, time.Second, genCtx, cancel, 1)
	coord.currentGen = gen
	coord.publishedGen = gen
	coord.cachedTools = tools
	coord.state = StateReady
	mgr := NewManager(pub)
	mgr.coordinators["srv"] = coord
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			snap := pub.Snapshot()
			lease, err := mgr.AcquireValidatedLease("srv", snap)
			if err == nil {
				lease.Release()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			srv := cfg
			if i%2 == 0 {
				srv.DisabledTools = map[string]struct{}{"tool": {}}
			}
			if err := mgr.Apply(context.Background(), &config.ResolvedConfig{Servers: map[string]config.ResolvedServer{"srv": srv}}); err != nil {
				t.Error(err)
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("validated admission and filter Apply deadlocked")
	}
	if gen.Active() != 0 {
		t.Fatal("concurrent validation leaked leases")
	}
}
