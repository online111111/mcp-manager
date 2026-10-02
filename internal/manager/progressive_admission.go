package manager

import (
	"errors"

	"github.com/online111111/mcp-manager/internal/catalog"
	"github.com/online111111/mcp-manager/internal/router"
)

// AcquireValidatedLease binds the published schema to generation admission.
// Never take the manager lock while holding publisher/coordinator locks: Apply
// holds the manager lock before changing coordinator publication.
func (m *Manager) AcquireValidatedLease(serverID string, expected *catalog.Snapshot) (router.Lease, error) {
	m.mu.RLock()
	if m.stopped {
		m.mu.RUnlock()
		return nil, ErrManagerStopped
	}
	coord, ok := m.coordinators[serverID]
	m.mu.RUnlock()
	if !ok {
		return nil, ErrServerNotFound
	}
	lease, err := coord.acquireValidatedLease(expected)
	if err != nil {
		return nil, err
	}
	return lease, nil
}

func (c *Coordinator) acquireValidatedLease(expected *catalog.Snapshot) (*Lease, error) {
	if expected == nil {
		return nil, errors.New("validated catalog unavailable")
	}
	c.publishMu.Lock()
	defer c.publishMu.Unlock()
	c.pub.RLock()
	defer c.pub.RUnlock()
	current := c.pub.Snapshot()
	if current == nil || current.Revision != expected.Revision {
		return nil, errors.New("validated catalog changed")
	}
	c.mu.Lock()
	gen := c.currentGen
	ready := !c.stopped && c.state == StateReady && gen != nil && c.desired.ResolvedConfig.Enabled
	sameGeneration := ready && gen == c.publishedGen
	c.mu.Unlock()
	if !sameGeneration {
		return nil, ErrNotAdmitting
	}
	return gen.acquire()
}
