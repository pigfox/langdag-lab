package harness

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"langdag.com/langdag/types"
)

// tree builds a small node set with real-looking generated identifiers.
//
// The ids are deliberately UNLIKE the ordinals the projection assigns, so a
// test that accidentally compared raw ids to canonical ones would fail rather
// than coincidentally pass.
func tree(idPrefix string) []*types.Node {
	root := &types.Node{
		ID: idPrefix + "-root", Sequence: 0, NodeType: types.NodeTypeUser,
		Content: "opening", Status: "completed", Title: "opening",
		CreatedAt: time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC),
	}
	root.RootID = root.ID
	first := &types.Node{
		ID: idPrefix + "-a", ParentID: root.ID, RootID: root.ID, Sequence: 1,
		NodeType: types.NodeTypeAssistant, Content: "first answer",
		Provider: "anthropic", Model: "m", TokensIn: 10, TokensOut: 4,
		LatencyMs: 17, CreatedAt: time.Date(2026, 3, 1, 12, 0, 1, 0, time.UTC),
	}
	second := &types.Node{
		ID: idPrefix + "-b", ParentID: root.ID, RootID: root.ID, Sequence: 1,
		NodeType: types.NodeTypeAssistant, Content: "second answer",
		Provider: "backup", Model: "m", TokensIn: 11, TokensOut: 5,
		LatencyMs: 23, CreatedAt: time.Date(2026, 3, 1, 12, 0, 2, 0, time.UTC),
	}
	return []*types.Node{root, first, second}
}

func canonJSON(t *testing.T, nodes []*types.Node) string {
	t.Helper()
	out, err := Canonicalize(nodes)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(encoded)
}

// THE FIRST DIRECTION. Everything the projection is allowed to erase must not
// read as a difference. If this fails, the fixture churns on every capture and
// the verify step stops meaning anything.
func TestCanonicalizeErasesRunToRunNoise(t *testing.T) {
	first := tree("run1")
	second := tree("run2")

	// Move every clock and latency in the second run, the way a real re-run
	// does.
	for i, n := range second {
		n.CreatedAt = time.Date(2027, 9, 9, 9, 9, i, 0, time.UTC)
		n.LatencyMs = 1000 + i
	}
	// And give it a different output group, with the same grouping.
	first[1].OutputGroupID = "group-uuid-one"
	first[2].OutputGroupID = "group-uuid-one"
	second[1].OutputGroupID = "totally-different-uuid"
	second[2].OutputGroupID = "totally-different-uuid"

	if a, b := canonJSON(t, first), canonJSON(t, second); a != b {
		t.Errorf("two runs of the same conversation projected differently:\n %s\n %s", a, b)
	}
}

// Input order is a property of the store's read, not of the conversation.
func TestCanonicalizeIsIndependentOfInputOrder(t *testing.T) {
	forward := tree("x")
	reversed := tree("x")
	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	if a, b := canonJSON(t, forward), canonJSON(t, reversed); a != b {
		t.Errorf("input order changed the projection:\n %s\n %s", a, b)
	}
}

// THE SECOND DIRECTION, and the one that matters more. A projection that
// erased too much would pass every test above and prove nothing. Each case
// changes exactly one real thing and the projection must notice.
func TestCanonicalizeKeepsEverySubstantiveDifference(t *testing.T) {
	baseline := canonJSON(t, tree("base"))

	for _, tc := range []struct {
		name   string
		change func(nodes []*types.Node)
	}{
		{"edited word", func(n []*types.Node) { n[1].Content = "first answer, revised" }},
		{"renamed provider", func(n []*types.Node) { n[1].Provider = "somebody-else" }},
		{"changed model", func(n []*types.Node) { n[1].Model = "m2" }},
		{"changed input tokens", func(n []*types.Node) { n[1].TokensIn = 99 }},
		{"changed output tokens", func(n []*types.Node) { n[1].TokensOut = 99 }},
		{"changed stop reason", func(n []*types.Node) { n[1].StopReason = "max_tokens" }},
		{"changed status", func(n []*types.Node) { n[1].Status = "failed" }},
		{"changed node type", func(n []*types.Node) { n[1].NodeType = types.NodeTypeSystem }},
		{"changed sequence", func(n []*types.Node) { n[1].Sequence = 7 }},
		{"changed title", func(n []*types.Node) { n[0].Title = "different" }},
		{"changed system prompt", func(n []*types.Node) { n[0].SystemPrompt = "be terse" }},
		{"changed metadata", func(n []*types.Node) { n[1].Metadata = json.RawMessage(`{"a":1}`) }},
		{"moved node to a different parent", func(n []*types.Node) { n[2].ParentID = n[1].ID }},
		{"dropped a node", func(n []*types.Node) { n[1].Content = n[2].Content; n[1].Provider = n[2].Provider }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nodes := tree("base")
			tc.change(nodes)
			out, err := Canonicalize(nodes)
			if err != nil {
				// "dropped a node" makes the two siblings identical, which is
				// the ambiguity error. That is still the projection noticing.
				if errors.Is(err, ErrAmbiguousSiblings) {
					return
				}
				t.Fatalf("Canonicalize: %v", err)
			}
			encoded, err := json.Marshal(out)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if string(encoded) == baseline {
				t.Errorf("%s did not change the projection, so the projection is too lossy", tc.name)
			}
		})
	}
}

// Grouping STRUCTURE is substance even though the group identifier is not.
func TestCanonicalizeKeepsGroupingStructure(t *testing.T) {
	shared := tree("g")
	shared[1].OutputGroupID = "one"
	shared[2].OutputGroupID = "one"

	split := tree("g")
	split[1].OutputGroupID = "one"
	split[2].OutputGroupID = "two"

	if canonJSON(t, shared) == canonJSON(t, split) {
		t.Error("two nodes in one output group projected the same as two nodes in different groups")
	}
}

// Ordinals are assigned in walk order and the walk is depth-first, so a
// conversation reads top to bottom.
func TestCanonicalizeAssignsOrdinalsDepthFirst(t *testing.T) {
	out, err := Canonicalize(tree("d"))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	want := []string{"n0", "n1", "n2"}
	if len(out) != len(want) {
		t.Fatalf("got %d nodes, want %d", len(out), len(want))
	}
	for i, id := range want {
		if out[i].ID != id {
			t.Errorf("node %d is %q, want %q", i, out[i].ID, id)
		}
		if out[i].RootID != "n0" {
			t.Errorf("node %d has root %q, want n0", i, out[i].RootID)
		}
	}
	if out[0].ParentID != "" {
		t.Errorf("root has parent %q, want empty", out[0].ParentID)
	}
	if out[1].ParentID != "n0" || out[2].ParentID != "n0" {
		t.Errorf("children point at %q and %q, want n0 both", out[1].ParentID, out[2].ParentID)
	}
	for _, n := range out {
		if !n.CreatedAt.IsZero() {
			t.Errorf("node %s kept a timestamp", n.ID)
		}
		if n.LatencyMs != 0 {
			t.Errorf("node %s kept a latency", n.ID)
		}
	}
}

// A tree branching twice with the SAME message is the two-branch scenario, and
// it is the case a node-only comparison cannot order. It must succeed, and the
// two sides must keep their own children.
func TestCanonicalizeOrdersIdenticalSiblingsByTheirSubtrees(t *testing.T) {
	root := &types.Node{ID: "r", RootID: "r", Sequence: 0, NodeType: types.NodeTypeUser, Content: "q"}
	leftAsk := &types.Node{ID: "l", ParentID: "r", RootID: "r", Sequence: 1, NodeType: types.NodeTypeUser, Content: "same"}
	rightAsk := &types.Node{ID: "g", ParentID: "r", RootID: "r", Sequence: 1, NodeType: types.NodeTypeUser, Content: "same"}
	leftAns := &types.Node{ID: "la", ParentID: "l", RootID: "r", Sequence: 2, NodeType: types.NodeTypeAssistant, Content: "alpha"}
	rightAns := &types.Node{ID: "ga", ParentID: "g", RootID: "r", Sequence: 2, NodeType: types.NodeTypeAssistant, Content: "beta"}

	out, err := Canonicalize([]*types.Node{root, leftAsk, rightAsk, leftAns, rightAns})
	if err != nil {
		t.Fatalf("identical siblings with different subtrees must be orderable: %v", err)
	}
	if len(out) != 5 {
		t.Fatalf("got %d nodes, want 5", len(out))
	}
	// Each ask must be immediately followed by its own answer.
	for i := 1; i < len(out); i += 2 {
		if out[i].NodeType != types.NodeTypeUser || out[i+1].ParentID != out[i].ID {
			t.Errorf("node %d and %d are not an ask/answer pair: %+v %+v", i, i+1, out[i], out[i+1])
		}
	}
	// And swapping which generated id is which must not move anything.
	swapped := canonJSON(t, []*types.Node{root, rightAsk, leftAsk, rightAns, leftAns})
	if swapped != canonJSON(t, []*types.Node{root, leftAsk, rightAsk, leftAns, rightAns}) {
		t.Error("sibling projection depended on the order the nodes arrived in")
	}
}

func TestCanonicalizeRejectsMalformedSets(t *testing.T) {
	root := func(id string) *types.Node {
		return &types.Node{ID: id, RootID: id, Sequence: 0, NodeType: types.NodeTypeUser, Content: id}
	}
	for _, tc := range []struct {
		name  string
		nodes []*types.Node
		want  error
	}{
		{"empty", nil, ErrNoRoot},
		{"two roots", []*types.Node{root("a"), root("b")}, ErrManyRoots},
		{
			"orphan",
			[]*types.Node{root("a"), {ID: "c", ParentID: "missing", Sequence: 1}},
			ErrOrphanNode,
		},
		{
			"identical siblings",
			[]*types.Node{
				root("a"),
				{ID: "x", ParentID: "a", RootID: "a", Sequence: 1, NodeType: types.NodeTypeAssistant, Content: "same"},
				{ID: "y", ParentID: "a", RootID: "a", Sequence: 1, NodeType: types.NodeTypeAssistant, Content: "same"},
			},
			ErrAmbiguousSiblings,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Canonicalize(tc.nodes); !errors.Is(err, tc.want) {
				t.Errorf("got %v, want %v", err, tc.want)
			}
		})
	}
}

func TestCanonicalizeRejectsNilAndDuplicates(t *testing.T) {
	if _, err := Canonicalize([]*types.Node{nil}); err == nil {
		t.Error("a nil node must be refused")
	}
	dup := &types.Node{ID: "a", RootID: "a", NodeType: types.NodeTypeUser}
	if _, err := Canonicalize([]*types.Node{dup, dup}); err == nil {
		t.Error("a duplicate id must be refused")
	}
}

// A node set that is entirely a cycle has no parentless node. It cannot arise
// through langdag, and the projection must still refuse it rather than
// recursing forever.
func TestCanonicalizeRejectsAParentlessCycle(t *testing.T) {
	a := &types.Node{ID: "a", ParentID: "b", Sequence: 0}
	b := &types.Node{ID: "b", ParentID: "a", Sequence: 1}
	if _, err := Canonicalize([]*types.Node{a, b}); !errors.Is(err, ErrNoRoot) {
		t.Errorf("got %v, want %v", err, ErrNoRoot)
	}
}

// A cycle BELOW the root passes every other check — both parents are present
// and there is exactly one parentless node — and would recurse until the stack
// ran out. It must be refused, not crashed on.
func TestCanonicalizeRefusesACycleBelowTheRoot(t *testing.T) {
	root := &types.Node{ID: "r", RootID: "r", Sequence: 0, NodeType: types.NodeTypeUser, Content: "q"}
	a := &types.Node{ID: "a", ParentID: "b", RootID: "r", Sequence: 1, NodeType: types.NodeTypeAssistant, Content: "a"}
	b := &types.Node{ID: "b", ParentID: "a", RootID: "r", Sequence: 1, NodeType: types.NodeTypeAssistant, Content: "b"}

	if _, err := Canonicalize([]*types.Node{root, a, b}); !errors.Is(err, ErrCycle) {
		t.Errorf("got %v, want %v", err, ErrCycle)
	}
}

// The shape key length-prefixes each field. Without that, two different
// conversations whose adjacent fields concatenate the same way would hash
// identically and the two branches could swap.
func TestShapeKeyDoesNotConfuseAdjacentFields(t *testing.T) {
	root := &types.Node{ID: "r", RootID: "r", Sequence: 0, NodeType: types.NodeTypeUser, Content: "q"}
	left := &types.Node{ID: "l", ParentID: "r", RootID: "r", Sequence: 1, NodeType: types.NodeTypeAssistant, Provider: "ab", Model: "c"}
	right := &types.Node{ID: "g", ParentID: "r", RootID: "r", Sequence: 1, NodeType: types.NodeTypeAssistant, Provider: "a", Model: "bc"}

	out, err := Canonicalize([]*types.Node{root, left, right})
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if out[1].Provider == out[2].Provider && out[1].Model == out[2].Model {
		t.Error("two distinct nodes collapsed into one shape")
	}
}
