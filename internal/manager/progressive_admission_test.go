package manager

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/online111111/mcp-manager/internal/catalog"
	"github.com/online111111/mcp-manager/internal/config"
	"github.com/online111111/mcp-manager/internal/router"
)

func TestValidatedAdmissionCannotUsePublishedOldSchemaWithNewGeneration(t *testing.T) {
	pub := NewCatalogPublisher(catalog.NewCatalog())
	tools := []*mcp.Tool{{Name: "tool", InputSchema: json.RawMessage(`{"type":"object"}`)}}
	snap, err := pub.PublishServer("srv", tools, nil)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.ResolvedServer{ID: "srv", Enabled: true, CallTimeout: time.Second, MaxConcurrency: 1}
	coord := newCoordinator("srv", DesiredServer{Revision: 1, ResolvedConfig: cfg}, pub, nil, nil, nil, 0, time.Millisecond)
	genCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gen := newGeneration(2, "srv", nil, nil, 1, time.Second, genCtx, cancel, 2)
	coord.currentGen = gen
	coord.state = StateReady
	// Model the publish/install transition: visible schema is still the old
	// generation's catalog while a different generation is ready to admit.
	mgr := NewManager(pub)
	mgr.coordinators["srv"] = coord
	lease, err := mgr.AcquireValidatedLease("srv", snap)
	if lease != nil {
		lease.Release()
	}
	if err == nil {
		t.Fatal("old catalog schema was admitted on a different generation")
	}
}

func TestValidatedAdmissionRejectsRemovedMembership(t *testing.T) {
	pub := NewCatalogPublisher(catalog.NewCatalog())
	snap, err := pub.PublishServer("srv", []*mcp.Tool{{Name: "tool", InputSchema: json.RawMessage(`{"type":"object"}`)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	mgr := NewManager(pub)
	if _, err := mgr.AcquireValidatedLease("srv", snap); err == nil {
		t.Fatal("missing coordinator admitted")
	}
	if _, err := pub.RemoveServer("srv"); err != nil {
		t.Fatal(err)
	}
	if _, err := mgr.AcquireValidatedLease("srv", snap); err == nil {
		t.Fatal("removed catalog admitted")
	}
	_ = router.ErrServerUnavailable
}
