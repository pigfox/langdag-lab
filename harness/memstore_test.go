package harness

import (
	"context"
	"errors"
	"testing"
	"time"

	"langdag.com/langdag/types"
)

func node(id, parent string, seq int, content string) *types.Node {
	return &types.Node{
		ID: id, ParentID: parent, RootID: "root", Sequence: seq,
		NodeType: types.NodeTypeUser, Content: content,
		CreatedAt: time.Date(2026, 1, 1, 0, 0, seq, 0, time.UTC),
	}
}

// seeded builds root -> a -> b, plus a second child c of root.
func seeded(t *testing.T) (*MemStore, context.Context) {
	t.Helper()
	ctx := context.Background()
	s := NewMemStore()
	if err := s.Init(ctx); err != nil {
		t.Fatalf("Init: %v", err)
	}
	for _, n := range []*types.Node{
		node("root", "", 0, "root"),
		node("a", "root", 1, "a"),
		node("b", "a", 2, "b"),
		node("c", "root", 1, "c"),
	} {
		if err := s.CreateNode(ctx, n); err != nil {
			t.Fatalf("CreateNode %s: %v", n.ID, err)
		}
	}
	return s, ctx
}

func ids(nodes []*types.Node) []string {
	out := make([]string, len(nodes))
	for i, n := range nodes {
		out[i] = n.ID
	}
	return out
}

func equalIDs(got []*types.Node, want ...string) bool {
	g := ids(got)
	if len(g) != len(want) {
		return false
	}
	for i := range g {
		if g[i] != want[i] {
			return false
		}
	}
	return true
}

func TestMemStoreReads(t *testing.T) {
	s, ctx := seeded(t)

	got, err := s.GetNode(ctx, "a")
	if err != nil || got == nil || got.Content != "a" {
		t.Fatalf("GetNode: %v %+v", err, got)
	}
	missing, err := s.GetNode(ctx, "nope")
	if err != nil || missing != nil {
		t.Errorf("a missing node must read as (nil, nil), got %+v %v", missing, err)
	}

	// A returned node is a copy: mutating it must not reach the store.
	got.Content = "tampered"
	again, _ := s.GetNode(ctx, "a")
	if again.Content != "a" {
		t.Error("GetNode handed out a pointer into the store")
	}

	byPrefix, err := s.GetNodeByPrefix(ctx, "ro")
	if err != nil || byPrefix == nil || byPrefix.ID != "root" {
		t.Errorf("GetNodeByPrefix: %v %+v", err, byPrefix)
	}
	if none, err := s.GetNodeByPrefix(ctx, "zz"); err != nil || none != nil {
		t.Errorf("unmatched prefix must read as (nil, nil), got %+v %v", none, err)
	}

	kids, err := s.GetNodeChildren(ctx, "root")
	if err != nil || !equalIDs(kids, "a", "c") {
		t.Errorf("GetNodeChildren: %v %v", err, ids(kids))
	}

	sub, err := s.GetSubtree(ctx, "root")
	if err != nil || !equalIDs(sub, "root", "a", "c", "b") {
		t.Errorf("GetSubtree(root): %v %v", err, ids(sub))
	}
	part, err := s.GetSubtree(ctx, "a")
	if err != nil || !equalIDs(part, "a", "b") {
		t.Errorf("GetSubtree(a): %v %v", err, ids(part))
	}
	if none, err := s.GetSubtree(ctx, "nope"); err != nil || none != nil {
		t.Errorf("GetSubtree of a missing node must be empty, got %v %v", ids(none), err)
	}

	anc, err := s.GetAncestors(ctx, "b")
	if err != nil || !equalIDs(anc, "root", "a", "b") {
		t.Errorf("GetAncestors must be root-first and inclusive: %v %v", err, ids(anc))
	}
	if none, err := s.GetAncestors(ctx, "nope"); err != nil || len(none) != 0 {
		t.Errorf("GetAncestors of a missing node must be empty, got %v %v", ids(none), err)
	}

	roots, err := s.ListRootNodes(ctx)
	if err != nil || !equalIDs(roots, "root") {
		t.Errorf("ListRootNodes: %v %v", err, ids(roots))
	}
}

// ListRootNodes orders newest first, matching the shipped store.
func TestMemStoreListsRootsNewestFirst(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	older := node("older", "", 0, "older")
	newer := node("newer", "", 0, "newer")
	newer.CreatedAt = older.CreatedAt.Add(time.Hour)
	// Inserted oldest first so insertion order cannot produce the answer.
	for _, n := range []*types.Node{older, newer} {
		if err := s.CreateNode(ctx, n); err != nil {
			t.Fatalf("CreateNode: %v", err)
		}
	}
	roots, err := s.ListRootNodes(ctx)
	if err != nil || !equalIDs(roots, "newer", "older") {
		t.Errorf("got %v %v", ids(roots), err)
	}
}

// Siblings at the same sequence keep insertion order, which is what makes a
// capture reproducible. Map iteration would not.
func TestMemStoreOrdersEqualSequencesByInsertion(t *testing.T) {
	ctx := context.Background()
	s := NewMemStore()
	if err := s.CreateNode(ctx, node("root", "", 0, "r")); err != nil {
		t.Fatalf("CreateNode: %v", err)
	}
	for _, id := range []string{"z", "y", "x", "w", "v", "u", "t", "s"} {
		if err := s.CreateNode(ctx, node(id, "root", 1, id)); err != nil {
			t.Fatalf("CreateNode %s: %v", id, err)
		}
	}
	for i := range 20 {
		kids, err := s.GetNodeChildren(ctx, "root")
		if err != nil {
			t.Fatalf("GetNodeChildren: %v", err)
		}
		if !equalIDs(kids, "z", "y", "x", "w", "v", "u", "t", "s") {
			t.Fatalf("read %d returned %v", i, ids(kids))
		}
	}
}

func TestMemStoreWrites(t *testing.T) {
	s, ctx := seeded(t)

	if err := s.CreateNode(ctx, nil); err == nil {
		t.Error("CreateNode(nil) must fail")
	}
	if err := s.CreateNode(ctx, node("a", "root", 1, "dup")); err == nil {
		t.Error("a duplicate id must fail")
	}

	updated := node("a", "root", 1, "edited")
	if err := s.UpdateNode(ctx, updated); err != nil {
		t.Fatalf("UpdateNode: %v", err)
	}
	if got, _ := s.GetNode(ctx, "a"); got.Content != "edited" {
		t.Errorf("update did not take: %q", got.Content)
	}
	if err := s.UpdateNode(ctx, nil); err == nil {
		t.Error("UpdateNode(nil) must fail")
	}
	if err := s.UpdateNode(ctx, node("ghost", "", 0, "x")); !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("updating a missing node: got %v", err)
	}
}

func TestMemStoreDeleteRemovesTheWholeSubtree(t *testing.T) {
	s, ctx := seeded(t)
	if err := s.CreateAlias(ctx, "b", "leaf"); err != nil {
		t.Fatalf("CreateAlias: %v", err)
	}
	if err := s.IndexToolIDs(ctx, "b", []string{"t1"}, "use"); err != nil {
		t.Fatalf("IndexToolIDs: %v", err)
	}

	if err := s.DeleteNode(ctx, "a"); err != nil {
		t.Fatalf("DeleteNode: %v", err)
	}
	for _, id := range []string{"a", "b"} {
		if got, _ := s.GetNode(ctx, id); got != nil {
			t.Errorf("%s survived the delete", id)
		}
	}
	if got, _ := s.GetNodeByAlias(ctx, "leaf"); got != nil {
		t.Error("an alias to a deleted node survived")
	}
	sub, _ := s.GetSubtree(ctx, "root")
	if !equalIDs(sub, "root", "c") {
		t.Errorf("after delete the tree is %v", ids(sub))
	}
	if err := s.DeleteNode(ctx, "gone"); !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("deleting a missing node: got %v", err)
	}
}

func TestMemStoreAliases(t *testing.T) {
	s, ctx := seeded(t)

	if err := s.CreateAlias(ctx, "ghost", "x"); !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("aliasing a missing node: got %v", err)
	}
	for _, alias := range []string{"second", "first"} {
		if err := s.CreateAlias(ctx, "a", alias); err != nil {
			t.Fatalf("CreateAlias %s: %v", alias, err)
		}
	}
	got, err := s.GetNodeByAlias(ctx, "first")
	if err != nil || got == nil || got.ID != "a" {
		t.Fatalf("GetNodeByAlias: %v %+v", err, got)
	}
	if none, err := s.GetNodeByAlias(ctx, "absent"); err != nil || none != nil {
		t.Errorf("unknown alias must read as (nil, nil), got %+v %v", none, err)
	}

	list, err := s.ListAliases(ctx, "a")
	if err != nil || len(list) != 2 || list[0] != "first" || list[1] != "second" {
		t.Errorf("ListAliases must be sorted: %v %v", list, err)
	}
	if err := s.DeleteAlias(ctx, "first"); err != nil {
		t.Fatalf("DeleteAlias: %v", err)
	}
	if err := s.DeleteAlias(ctx, "first"); !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("deleting a missing alias: got %v", err)
	}
}

// An alias whose target vanished without going through DeleteNode still must
// not resolve to a node that is not there.
func TestMemStoreAliasToVanishedTargetDoesNotResolve(t *testing.T) {
	s, ctx := seeded(t)
	if err := s.CreateAlias(ctx, "b", "leaf"); err != nil {
		t.Fatalf("CreateAlias: %v", err)
	}
	s.mu.Lock()
	delete(s.nodes, "b")
	s.mu.Unlock()
	if got, err := s.GetNodeByAlias(ctx, "leaf"); err != nil || got != nil {
		t.Errorf("got %+v %v, want (nil, nil)", got, err)
	}
}

func TestMemStoreToolIndexFindsUnansweredCalls(t *testing.T) {
	s, ctx := seeded(t)

	if err := s.IndexToolIDs(ctx, "ghost", []string{"t"}, "use"); !errors.Is(err, ErrNodeNotFound) {
		t.Errorf("indexing against a missing node: got %v", err)
	}
	// The empty id is dropped rather than indexed.
	if err := s.IndexToolIDs(ctx, "a", []string{"t1", "", "t2"}, "use"); err != nil {
		t.Fatalf("IndexToolIDs: %v", err)
	}
	if err := s.IndexToolIDs(ctx, "b", []string{"t1"}, "result"); err != nil {
		t.Fatalf("IndexToolIDs: %v", err)
	}

	scope := []string{"root", "a", "b"}
	orphans, err := s.GetOrphanedToolUses(ctx, scope)
	if err != nil {
		t.Fatalf("GetOrphanedToolUses: %v", err)
	}
	if len(orphans["a"]) != 1 || orphans["a"][0] != "t2" {
		t.Errorf("expected t2 unanswered, got %v", orphans)
	}

	// Answer it and the report must empty out entirely.
	if err := s.IndexToolIDs(ctx, "b", []string{"t2"}, "result"); err != nil {
		t.Fatalf("IndexToolIDs: %v", err)
	}
	if orphans, err := s.GetOrphanedToolUses(ctx, scope); err != nil || orphans != nil {
		t.Errorf("everything answered must report nothing, got %v %v", orphans, err)
	}

	// A result recorded OUTSIDE the queried scope does not count as an answer.
	if err := s.IndexToolIDs(ctx, "c", []string{"t3"}, "result"); err != nil {
		t.Fatalf("IndexToolIDs: %v", err)
	}
	if err := s.IndexToolIDs(ctx, "a", []string{"t3"}, "use"); err != nil {
		t.Fatalf("IndexToolIDs: %v", err)
	}
	narrow, err := s.GetOrphanedToolUses(ctx, []string{"root", "a", "b"})
	if err != nil || len(narrow["a"]) != 1 || narrow["a"][0] != "t3" {
		t.Errorf("out-of-scope answer must not count, got %v %v", narrow, err)
	}
}

func TestMemStoreCloseIsObservable(t *testing.T) {
	s := NewMemStore()
	if s.Closed() {
		t.Error("a fresh store reports closed")
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !s.Closed() {
		t.Error("Close did not take")
	}
}
