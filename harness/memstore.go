// Package harness builds real langdag conversation trees against mock
// providers and captures them as deterministic fixtures.
//
// WHY THIS REPOSITORY EXISTS. pigfox.com renders these trees on a demo page.
// The page must show what langdag actually produces — node shapes, sequence
// numbers, provider attribution, token counters — and not a hand-written
// imitation of it. So the trees are BUILT HERE, through langdag's public API,
// and the resulting JSON is committed. The demo reads the committed bytes.
//
// The alternative was to build the trees inside the website at render time.
// That would put a 357-package dependency graph, an AWS credential chain and a
// Google Cloud auth stack into a web binary in order to draw a diagram. The
// capture lives here; only the JSON travels.
package harness

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"

	"langdag.com/langdag/types"
)

// ErrNodeNotFound is returned by the reads that address a single node.
//
// langdag's own SQLite store answers a missing row with (nil, nil) on GetNode
// and the manager checks for nil, so this store does the same rather than
// returning an error the caller does not expect. The sentinel is used by the
// writes, where a missing target IS an error in both implementations.
var ErrNodeNotFound = errors.New("harness: node not found")

// MemStore is an in-memory langdag Storage.
//
// NO FILE IS EVER CREATED. langdag's shipped store is SQLite and its Client
// constructor makes a database on disk; this repository deliberately does not
// use it. A capture that wrote a database would leave a build artifact whose
// contents depend on when it ran, and the fixture would then have two sources
// of truth — the JSON and the file that produced it — which is one more than
// can be kept honest.
//
// It is concurrency-safe because langdag's Client documents itself as safe for
// concurrent use and the manager streams from a goroutine. The capture is
// sequential, but a store that is only correct when called sequentially would
// be a trap for the next reader.
type MemStore struct {
	mu sync.RWMutex

	// nodes is the store proper. Values are owned copies: CreateNode copies in
	// and the reads copy out, so a caller mutating a returned *Node cannot
	// reach into the store.
	nodes map[string]types.Node

	// order records insertion order. It is what makes every read here
	// deterministic without inventing a tiebreak: SQLite's ORDER BY sequence
	// leaves siblings at the same sequence in an unspecified order, and this
	// repository captures exactly that shape on purpose in the two-branch
	// scenario. Map iteration would make the capture differ run to run.
	order []string

	// aliases maps an alias to a node id.
	aliases map[string]string

	// toolIDs records the tool-use and tool-result index the manager maintains
	// so orphan detection is a lookup rather than a JSON parse. Keyed by node,
	// then role, holding the tool ids in insertion order.
	toolIDs map[string]map[string][]string

	closed bool
}

// NewMemStore builds an empty store. It is ready without Init; Init exists to
// satisfy the interface and is idempotent.
func NewMemStore() *MemStore {
	return &MemStore{
		nodes:   map[string]types.Node{},
		aliases: map[string]string{},
		toolIDs: map[string]map[string][]string{},
	}
}

// Init satisfies langdag's Storage. There are no migrations to run.
func (s *MemStore) Init(context.Context) error { return nil }

// Close marks the store closed. Closed is observable so a capture that keeps
// using a client after Close fails loudly instead of quietly succeeding.
func (s *MemStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

// Closed reports whether Close has been called.
func (s *MemStore) Closed() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.closed
}

// CreateNode stores a copy of node.
func (s *MemStore) CreateNode(_ context.Context, node *types.Node) error {
	if node == nil {
		return errors.New("harness: CreateNode(nil)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.nodes[node.ID]; exists {
		return errors.New("harness: duplicate node id " + node.ID)
	}
	s.nodes[node.ID] = *node
	s.order = append(s.order, node.ID)
	return nil
}

// GetNode returns the node, or (nil, nil) when there is no such id — the shape
// langdag's manager checks for.
func (s *MemStore) GetNode(_ context.Context, id string) (*types.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	n, ok := s.nodes[id]
	if !ok {
		return nil, nil
	}
	return &n, nil
}

// GetNodeByPrefix returns the first node in insertion order whose id starts
// with prefix, or (nil, nil).
func (s *MemStore) GetNodeByPrefix(_ context.Context, prefix string) (*types.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, id := range s.order {
		if strings.HasPrefix(id, prefix) {
			n := s.nodes[id]
			return &n, nil
		}
	}
	return nil, nil
}

// GetNodeChildren returns the direct children of parentID, by sequence then
// insertion order.
func (s *MemStore) GetNodeChildren(_ context.Context, parentID string) ([]*types.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*types.Node
	for _, id := range s.order {
		if n := s.nodes[id]; n.ParentID == parentID {
			copied := n
			out = append(out, &copied)
		}
	}
	return s.sorted(out), nil
}

// GetSubtree returns nodeID and every descendant, by sequence then insertion
// order.
func (s *MemStore) GetSubtree(_ context.Context, nodeID string) ([]*types.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if _, ok := s.nodes[nodeID]; !ok {
		return nil, nil
	}
	in := map[string]bool{nodeID: true}
	var out []*types.Node
	// One pass in insertion order suffices: a child is always created after
	// its parent, so a parent is always already marked when its child is read.
	for _, id := range s.order {
		n := s.nodes[id]
		if id != nodeID && !in[n.ParentID] {
			continue
		}
		in[id] = true
		copied := n
		out = append(out, &copied)
	}
	return s.sorted(out), nil
}

// GetAncestors returns the path from the root down to nodeID inclusive, by
// sequence.
func (s *MemStore) GetAncestors(_ context.Context, nodeID string) ([]*types.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*types.Node
	// A cycle cannot occur through the public API — a node's parent always
	// exists before it does — but a bounded walk means a malformed store fails
	// rather than hangs.
	seen := map[string]bool{}
	for id := nodeID; id != ""; {
		n, ok := s.nodes[id]
		if !ok || seen[id] {
			break
		}
		seen[id] = true
		copied := n
		out = append(out, &copied)
		id = n.ParentID
	}
	// Walked leaf-to-root; the manager wants root-first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// ListRootNodes returns the parentless nodes, newest first, matching the
// shipped store's ORDER BY created_at DESC.
func (s *MemStore) ListRootNodes(_ context.Context) ([]*types.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []*types.Node
	for _, id := range s.order {
		if n := s.nodes[id]; n.ParentID == "" {
			copied := n
			out = append(out, &copied)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		return out[i].CreatedAt.After(out[j].CreatedAt)
	})
	return out, nil
}

// UpdateNode replaces a stored node.
func (s *MemStore) UpdateNode(_ context.Context, node *types.Node) error {
	if node == nil {
		return errors.New("harness: UpdateNode(nil)")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[node.ID]; !ok {
		return ErrNodeNotFound
	}
	s.nodes[node.ID] = *node
	return nil
}

// DeleteNode removes a node and everything beneath it.
//
// GetSubtree is called for its result only — it has no failure mode, so there
// is no error branch here to handle. Writing one would add a line no test could
// reach.
func (s *MemStore) DeleteNode(ctx context.Context, id string) error {
	sub, _ := s.GetSubtree(ctx, id)
	if len(sub) == 0 {
		return ErrNodeNotFound
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	doomed := map[string]bool{}
	for _, n := range sub {
		doomed[n.ID] = true
		delete(s.nodes, n.ID)
		delete(s.toolIDs, n.ID)
	}
	kept := s.order[:0]
	for _, existing := range s.order {
		if !doomed[existing] {
			kept = append(kept, existing)
		}
	}
	s.order = kept
	for alias, target := range s.aliases {
		if doomed[target] {
			delete(s.aliases, alias)
		}
	}
	return nil
}

// CreateAlias points alias at nodeID.
func (s *MemStore) CreateAlias(_ context.Context, nodeID, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[nodeID]; !ok {
		return ErrNodeNotFound
	}
	s.aliases[alias] = nodeID
	return nil
}

// DeleteAlias removes an alias.
func (s *MemStore) DeleteAlias(_ context.Context, alias string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.aliases[alias]; !ok {
		return ErrNodeNotFound
	}
	delete(s.aliases, alias)
	return nil
}

// GetNodeByAlias resolves an alias, or (nil, nil).
func (s *MemStore) GetNodeByAlias(_ context.Context, alias string) (*types.Node, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	id, ok := s.aliases[alias]
	if !ok {
		return nil, nil
	}
	n, ok := s.nodes[id]
	if !ok {
		return nil, nil
	}
	return &n, nil
}

// ListAliases returns the aliases pointing at nodeID, in a stable order.
func (s *MemStore) ListAliases(_ context.Context, nodeID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []string
	for alias, target := range s.aliases {
		if target == nodeID {
			out = append(out, alias)
		}
	}
	sort.Strings(out)
	return out, nil
}

// IndexToolIDs records tool ids against a node under a role, which is "use" on
// an assistant node that asked for a tool and "result" on the user node
// carrying the answer.
func (s *MemStore) IndexToolIDs(_ context.Context, nodeID string, ids []string, role string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.nodes[nodeID]; !ok {
		return ErrNodeNotFound
	}
	byRole := s.toolIDs[nodeID]
	if byRole == nil {
		byRole = map[string][]string{}
		s.toolIDs[nodeID] = byRole
	}
	for _, id := range ids {
		if id != "" {
			byRole[role] = append(byRole[role], id)
		}
	}
	return nil
}

// GetOrphanedToolUses reports, per node, the tool ids that node asked for and
// which no node in the same set answered.
//
// It exists so langdag can repair a conversation whose tool call was never
// answered before the next turn — an unanswered tool_use is rejected by the
// real APIs. The captures here always answer, so this returns empty on the
// capture path; it is implemented properly rather than stubbed because a store
// that silently reports "no orphans" would mask exactly the condition the
// manager consults it about.
func (s *MemStore) GetOrphanedToolUses(_ context.Context, ancestorIDs []string) (map[string][]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	inScope := map[string]bool{}
	for _, id := range ancestorIDs {
		inScope[id] = true
	}
	answered := map[string]bool{}
	for id, byRole := range s.toolIDs {
		if !inScope[id] {
			continue
		}
		for _, toolID := range byRole["result"] {
			answered[toolID] = true
		}
	}
	out := map[string][]string{}
	for _, id := range ancestorIDs {
		for _, toolID := range s.toolIDs[id]["use"] {
			if !answered[toolID] {
				out[id] = append(out[id], toolID)
			}
		}
	}
	if len(out) == 0 {
		return nil, nil
	}
	return out, nil
}

// sorted orders by sequence, holding insertion order within a sequence. The
// caller holds at least a read lock.
func (s *MemStore) sorted(in []*types.Node) []*types.Node {
	pos := make(map[string]int, len(s.order))
	for i, id := range s.order {
		pos[id] = i
	}
	sort.SliceStable(in, func(i, j int) bool {
		if in[i].Sequence != in[j].Sequence {
			return in[i].Sequence < in[j].Sequence
		}
		return pos[in[i].ID] < pos[in[j].ID]
	})
	return in
}
