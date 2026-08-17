package harness

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

func buildAll(t *testing.T) map[string]*Document {
	t.Helper()
	ctx := context.Background()
	out := map[string]*Document{}
	for _, scenario := range Scenarios() {
		doc, err := scenario.Build(ctx)
		if err != nil {
			t.Fatalf("%s: %v", scenario.Slug, err)
		}
		if doc.Scenario != scenario.Slug {
			t.Errorf("%s built a document labelled %q", scenario.Slug, doc.Scenario)
		}
		out[scenario.Slug] = doc
	}
	return out
}

// Every scenario stamps the schema and the version its fixture claims.
func TestScenariosStampTheirProvenance(t *testing.T) {
	for slug, doc := range buildAll(t) {
		if doc.Schema != FixtureSchema {
			t.Errorf("%s: schema %q", slug, doc.Schema)
		}
		if doc.LangdagVersion != LangdagVersion {
			t.Errorf("%s: version %q", slug, doc.LangdagVersion)
		}
		if doc.Title == "" || doc.Summary == "" {
			t.Errorf("%s: a fixture without display copy leaves the page to invent it", slug)
		}
		if len(doc.Nodes) == 0 {
			t.Errorf("%s: no nodes", slug)
		}
	}
}

// BUILDING THE SAME SCENARIO TWICE MUST PRODUCE THE SAME BYTES. This is the
// property the whole canonical projection exists for, asserted directly rather
// than only through the verify subcommand.
func TestScenariosAreReproducible(t *testing.T) {
	first := buildAll(t)
	second := buildAll(t)
	for slug, doc := range first {
		a, err := Encode(doc)
		if err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		b, err := Encode(second[slug])
		if err != nil {
			t.Fatalf("%s: %v", slug, err)
		}
		if string(a) != string(b) {
			t.Errorf("%s is not reproducible", slug)
		}
	}
}

// The tool scenario is four nodes because langdag does not run tools: the
// request is stored as content blocks and the caller answers it.
func TestToolUseScenarioShape(t *testing.T) {
	doc := buildAll(t)[ScenarioToolUse]
	if len(doc.Nodes) != 4 {
		t.Fatalf("got %d nodes, want 4", len(doc.Nodes))
	}
	wantTypes := []types.NodeType{
		types.NodeTypeUser, types.NodeTypeAssistant,
		types.NodeTypeUser, types.NodeTypeAssistant,
	}
	for i, want := range wantTypes {
		if doc.Nodes[i].NodeType != want {
			t.Errorf("node %d is %q, want %q", i, doc.Nodes[i].NodeType, want)
		}
		if doc.Nodes[i].ID != "n"+string(rune('0'+i)) {
			t.Errorf("node %d has id %q", i, doc.Nodes[i].ID)
		}
	}

	// The assistant's tool request survives as parseable content blocks, not
	// as prose about a tool.
	var blocks []types.ContentBlock
	if err := json.Unmarshal([]byte(doc.Nodes[1].Content), &blocks); err != nil {
		t.Fatalf("the tool request is not content blocks: %v", err)
	}
	if len(blocks) != 1 || blocks[0].Type != "tool_use" || blocks[0].ID != toolCallID || blocks[0].Name != toolName {
		t.Errorf("blocks: %+v", blocks)
	}
	if doc.Nodes[1].StopReason != "tool_use" {
		t.Errorf("stop reason is %q", doc.Nodes[1].StopReason)
	}
	// And the answer comes back keyed to the same call id.
	if !strings.Contains(doc.Nodes[2].Content, toolCallID) {
		t.Errorf("the tool result does not reference the call: %q", doc.Nodes[2].Content)
	}
	if doc.Nodes[3].StopReason != "end_turn" {
		t.Errorf("final stop reason is %q", doc.Nodes[3].StopReason)
	}
	// No routing policy on this one, so no attempt log either.
	if doc.Policy != nil || doc.Attempts != nil {
		t.Error("a scenario with no policy must not carry one")
	}
}

// The branch scenario's subject: one node, two continuations, same question,
// different answers, shared history.
func TestBranchScenarioDivergesBelowOneNode(t *testing.T) {
	doc := buildAll(t)[ScenarioBranch]
	if len(doc.Nodes) != 6 {
		t.Fatalf("got %d nodes, want 6", len(doc.Nodes))
	}

	byID := map[string]types.Node{}
	children := map[string][]types.Node{}
	for _, n := range doc.Nodes {
		byID[n.ID] = n
		children[n.ParentID] = append(children[n.ParentID], n)
	}

	// Exactly one node has two children — the node both lines leave from.
	var pivot string
	for id, kids := range children {
		if len(kids) == 2 && id != "" {
			if pivot != "" {
				t.Fatalf("more than one node has two children")
			}
			pivot = id
		}
	}
	if pivot == "" {
		t.Fatal("no node has two children, so nothing diverged")
	}
	if byID[pivot].NodeType != types.NodeTypeAssistant {
		t.Errorf("the divergence point is a %q node", byID[pivot].NodeType)
	}

	// The two asks are the same text.
	asks := children[pivot]
	if asks[0].Content != asks[1].Content {
		t.Errorf("the two continuations asked different things:\n %q\n %q", asks[0].Content, asks[1].Content)
	}
	// And the two answers are not.
	left, right := children[asks[0].ID], children[asks[1].ID]
	if len(left) != 1 || len(right) != 1 {
		t.Fatalf("each continuation should have one answer, got %d and %d", len(left), len(right))
	}
	if left[0].Content == right[0].Content {
		t.Error("the same question got the same answer, which is not a divergence")
	}
}

// The fallback scenario shows a route that answers, then declines, and the
// standby that picks it up — with the declined attempt visible.
func TestFallbackScenarioAttributesEachAnswer(t *testing.T) {
	doc := buildAll(t)[ScenarioFallback]

	if len(doc.Policy) != 2 {
		t.Fatalf("policy: %+v", doc.Policy)
	}
	if doc.Policy[0].Route != PrimaryProvider || doc.Policy[0].Weight != PrimaryWeight {
		t.Errorf("policy[0]: %+v", doc.Policy[0])
	}
	if doc.Policy[1].Route != BackupProvider || doc.Policy[1].Weight != BackupWeight {
		t.Errorf("policy[1]: %+v", doc.Policy[1])
	}

	var served []string
	for _, n := range doc.Nodes {
		if n.NodeType == types.NodeTypeAssistant {
			served = append(served, n.Provider)
		}
	}
	if len(served) != 2 || served[0] != PrimaryProvider || served[1] != BackupProvider {
		t.Errorf("answers were served by %v, want [%s %s]", served, PrimaryProvider, BackupProvider)
	}

	// The refusal is in the attempt log and NOT in the tree, because a call
	// that returned nothing produces no node.
	if len(doc.Attempts) != 3 {
		t.Fatalf("attempts: %+v", doc.Attempts)
	}
	var refusals int
	for _, a := range doc.Attempts {
		if !a.OK {
			refusals++
			if a.Error == "" {
				t.Error("a refused attempt carries no reason")
			}
			if a.Route != PrimaryProvider {
				t.Errorf("the refusal came from %q", a.Route)
			}
		}
	}
	if refusals != 1 {
		t.Errorf("got %d refusals, want 1", refusals)
	}
	for _, n := range doc.Nodes {
		if strings.Contains(n.Content, "529") {
			t.Error("a refusal leaked into the tree as node content")
		}
	}
}

// A scenario whose provider runs dry fails loudly rather than capturing a
// truncated tree.
func TestScenarioFailsWhenTheProviderRunsDry(t *testing.T) {
	ctx := context.Background()
	client := langdag.NewWithDeps(NewMemStore(), NewScriptedProvider("p", "m"))
	defer client.Close() //nolint:errcheck // MemStore.Close cannot fail.

	result, err := client.Prompt(ctx, "anything", langdag.WithModel("m"))
	if err != nil {
		// langdag may surface the provider error here.
		return
	}
	if _, err := drain(result); err == nil {
		t.Error("a conversation with no scripted turns must not report success")
	}
}

func TestDrainRejectsANilResult(t *testing.T) {
	if _, err := drain(nil); err == nil {
		t.Error("drain(nil) must fail")
	}
}

func TestRootOfReportsAMissingNode(t *testing.T) {
	ctx := context.Background()
	client := langdag.NewWithDeps(NewMemStore(), NewScriptedProvider("p", "m"))
	defer client.Close() //nolint:errcheck // MemStore.Close cannot fail.

	if _, err := rootOf(ctx, client, "no-such-node"); err == nil {
		t.Error("rootOf must refuse a node that is not there")
	}
}

// Every scenario slug is distinct and non-empty, since it names a file.
func TestScenarioSlugsAreDistinct(t *testing.T) {
	seen := map[string]bool{}
	for _, scenario := range Scenarios() {
		if scenario.Slug == "" {
			t.Fatal("a scenario has no slug")
		}
		if seen[scenario.Slug] {
			t.Errorf("duplicate slug %q — one fixture would overwrite the other", scenario.Slug)
		}
		seen[scenario.Slug] = true
	}
	if len(seen) != 3 {
		t.Errorf("got %d scenarios, want 3", len(seen))
	}
}
