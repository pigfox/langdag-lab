package harness

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// FAULT INJECTION, and why it is here rather than left as untested branches.
//
// Every scenario builder propagates errors from calls that, against a MemStore
// and a scripted provider, cannot fail. Left alone those branches are code no
// test has ever run — and the gate would either report a coverage shortfall or
// be told to look away. Neither is acceptable, so the store is a parameter and
// the failures are real.
//
// The alternative considered and rejected: delete the error handling and let a
// failing store panic. langdag's Storage returns errors, a real store can fail,
// and code that ignores that would be wrong in a way the fixture could never
// reveal.

var errInjected = errors.New("injected store failure")

// faultStore fails one named method and delegates the rest.
type faultStore struct {
	*MemStore
	method string
	after  int
	seen   int
}

func failing(method string, after int) *faultStore {
	return &faultStore{MemStore: NewMemStore(), method: method, after: after}
}

// trip reports whether this call of method should fail.
func (f *faultStore) trip(method string) bool {
	if f.method != method {
		return false
	}
	f.seen++
	return f.seen > f.after
}

func (f *faultStore) CreateNode(ctx context.Context, n *types.Node) error {
	if f.trip("CreateNode") {
		return errInjected
	}
	return f.MemStore.CreateNode(ctx, n)
}

func (f *faultStore) GetNode(ctx context.Context, id string) (*types.Node, error) {
	if f.trip("GetNode") {
		return nil, errInjected
	}
	return f.MemStore.GetNode(ctx, id)
}

func (f *faultStore) GetAncestors(ctx context.Context, id string) ([]*types.Node, error) {
	if f.trip("GetAncestors") {
		return nil, errInjected
	}
	return f.MemStore.GetAncestors(ctx, id)
}

func (f *faultStore) GetSubtree(ctx context.Context, id string) ([]*types.Node, error) {
	if f.trip("GetSubtree") {
		return nil, errInjected
	}
	return f.MemStore.GetSubtree(ctx, id)
}

func (f *faultStore) GetOrphanedToolUses(ctx context.Context, ids []string) (map[string][]string, error) {
	if f.trip("GetOrphanedToolUses") {
		return nil, errInjected
	}
	return f.MemStore.GetOrphanedToolUses(ctx, ids)
}

// vanishingStore lets a node be created and then reports it missing, which is
// the one condition rootOf checks for that no error return covers.
//
// ALL THREE LOOKUPS ARE OVERRIDDEN, and that is not belt-and-braces. langdag
// resolves a node id by trying the exact id, then a prefix, then an alias — so
// a store that hid only the exact lookup would still be found by prefix, and
// the first draft of this test passed for that reason while proving nothing.
type vanishingStore struct {
	*MemStore
}

func (v *vanishingStore) GetNode(context.Context, string) (*types.Node, error) {
	return nil, nil
}

func (v *vanishingStore) GetNodeByPrefix(context.Context, string) (*types.Node, error) {
	return nil, nil
}

func (v *vanishingStore) GetNodeByAlias(context.Context, string) (*types.Node, error) {
	return nil, nil
}

type builder func(context.Context, langdag.Storage) (*Document, error)

func builders() map[string]builder {
	return map[string]builder{
		ScenarioToolUse:  buildToolUseIn,
		ScenarioBranch:   buildBranchIn,
		ScenarioFallback: buildFallbackIn,
	}
}

// Every builder must report a store failure rather than capturing a partial
// tree. Each injection point below is a different statement in the builder.
func TestBuildersReportStoreFailures(t *testing.T) {
	ctx := context.Background()
	for slug, build := range builders() {
		for _, tc := range []struct {
			method string
			after  int
		}{
			{"CreateNode", 0}, // the opening prompt cannot save its root
			// The assistant node fails to save. langdag reports that on the
			// STREAM rather than from the call, so this is the case that
			// exercises the error branch inside drain.
			{"CreateNode", 1},
			{"CreateNode", 2},          // a later turn's user node cannot save
			{"CreateNode", 3},          // a later turn's assistant node cannot save
			{"GetAncestors", 0},        // the continuation cannot read its history
			{"GetOrphanedToolUses", 0}, // the tool index cannot be consulted
			{"GetNode", 0},             // the root cannot be resolved
			{"GetSubtree", 0},          // the tree cannot be read back
		} {
			t.Run(slug+"/"+tc.method+"/"+string(rune('0'+tc.after)), func(t *testing.T) {
				if _, err := build(ctx, failing(tc.method, tc.after)); err == nil {
					t.Errorf("%s survived a failing %s", slug, tc.method)
				}
			})
		}
	}
}

// rootOf's "vanished" branch: the node was created, and the store then reports
// it absent. No error is returned by the store, so only this check catches it.
func TestBuildersReportAVanishedRoot(t *testing.T) {
	ctx := context.Background()
	for slug, build := range builders() {
		t.Run(slug, func(t *testing.T) {
			_, err := build(ctx, &vanishingStore{MemStore: NewMemStore()})
			if err == nil {
				t.Errorf("%s survived a store that lost its root", slug)
			}
		})
	}
}

// subtree's Canonicalize failure: the store returns a set that is not a tree.
type twoRootStore struct {
	*MemStore
}

func (s *twoRootStore) GetSubtree(context.Context, string) ([]*types.Node, error) {
	return []*types.Node{
		{ID: "a", RootID: "a", NodeType: types.NodeTypeUser, Content: "a"},
		{ID: "b", RootID: "b", NodeType: types.NodeTypeUser, Content: "b"},
	}, nil
}

func TestBuildersReportAnUncanonicalizableTree(t *testing.T) {
	ctx := context.Background()
	for slug, build := range builders() {
		t.Run(slug, func(t *testing.T) {
			_, err := build(ctx, &twoRootStore{MemStore: NewMemStore()})
			if !errors.Is(err, ErrManyRoots) {
				t.Errorf("%s: got %v, want %v", slug, err, ErrManyRoots)
			}
		})
	}
}

// The provider-call assertions in each builder. A store that quietly drops the
// second turn's write would leave the call counts wrong, and each builder says
// so rather than capturing a tree that is missing a turn.
type droppingStore struct {
	*MemStore
	dropAfter int
	seen      int
}

func (d *droppingStore) CreateNode(ctx context.Context, n *types.Node) error {
	d.seen++
	if d.seen > d.dropAfter {
		return nil // accepted, and silently not stored
	}
	return d.MemStore.CreateNode(ctx, n)
}

func TestBuildersNoticeASilentlyDroppedWrite(t *testing.T) {
	ctx := context.Background()
	for slug, build := range builders() {
		t.Run(slug, func(t *testing.T) {
			if _, err := build(ctx, &droppingStore{MemStore: NewMemStore(), dropAfter: 1}); err == nil {
				t.Errorf("%s captured a tree from a store that dropped writes", slug)
			}
		})
	}
}

// --- Failures above the builders -------------------------------------------

func refusing(slug string) Scenario {
	return Scenario{Slug: slug, Build: func(context.Context) (*Document, error) {
		return nil, errInjected
	}}
}

// A document whose node metadata is not valid JSON cannot be encoded. It is the
// one Encode failure reachable without a fake marshaller, and it is real:
// Metadata is a json.RawMessage that passes through byte for byte.
func unencodable(slug string) Scenario {
	return Scenario{Slug: slug, Build: func(context.Context) (*Document, error) {
		return &Document{
			Schema:   FixtureSchema,
			Scenario: slug,
			Nodes:    []types.Node{{ID: "n0", Metadata: json.RawMessage("{not json")}},
		}, nil
	}}
}

func TestEncodeReportsAnUnencodableDocument(t *testing.T) {
	doc, err := unencodable("x").Build(context.Background())
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if _, err := Encode(doc); err == nil {
		t.Error("a document holding invalid raw JSON must not encode")
	}
}

func TestCaptureReportsAScenarioThatRefuses(t *testing.T) {
	ctx := context.Background()
	if _, err := captureInto(ctx, t.TempDir(), []Scenario{refusing("bad")}); err == nil {
		t.Error("captureInto passed over a scenario that refused")
	}
	if _, err := captureInto(ctx, t.TempDir(), []Scenario{unencodable("bad")}); err == nil {
		t.Error("captureInto passed over a document it could not encode")
	}
}

func TestCaptureReportsAnUnwritableFile(t *testing.T) {
	dir := t.TempDir()
	// A slug containing a separator puts the file under a directory that does
	// not exist, so the write fails while the target directory itself is fine.
	if _, err := captureInto(context.Background(), dir, []Scenario{{
		Slug:  "missing-dir/leaf",
		Build: Scenarios()[0].Build,
	}}); err == nil {
		t.Error("captureInto passed over a file it could not write")
	}
}

func TestVerifyReportsAScenarioThatRefuses(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	real := Scenarios()[0]

	// Seed a fixture so verify gets past the read and reaches the build.
	if _, err := captureInto(ctx, dir, []Scenario{{Slug: "bad", Build: real.Build}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := verifyAgainst(ctx, dir, []Scenario{refusing("bad")}); err == nil {
		t.Error("verifyAgainst passed over a scenario that refused")
	}
	if _, err := verifyAgainst(ctx, dir, []Scenario{unencodable("bad")}); err == nil {
		t.Error("verifyAgainst passed over a document it could not encode")
	}
}

func TestCLIReportsAVerifyThatCouldNotRun(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	if _, err := captureInto(ctx, dir, []Scenario{{Slug: "bad", Build: Scenarios()[0].Build}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	var out, errBuf strings.Builder
	code := mainWith([]string{"langdag-lab", CommandVerify, dir}, &out, &errBuf, []Scenario{refusing("bad")})
	if code != exitFail {
		t.Errorf("exited %d, want %d", code, exitFail)
	}
	if !strings.Contains(errBuf.String(), "verify could not run") {
		t.Errorf("stderr: %q", errBuf.String())
	}
}

// finish is where the "did the library still make the calls we expect" checks
// live. Both outcomes, since a check that cannot fail protects nothing.
func TestFinishChecksCallCounts(t *testing.T) {
	doc := &Document{Scenario: "s"}

	got, err := finish(doc, callCheck{"provider calls", 2, 2})
	if err != nil || got != doc {
		t.Fatalf("a satisfied check must pass the document through: %v %v", got, err)
	}
	if _, err := finish(doc, callCheck{"provider calls", 1, 2}); err == nil {
		t.Error("an unsatisfied check must fail the capture")
	}
	// The second check is reached even when the first is satisfied.
	_, err = finish(doc,
		callCheck{"attempts on the preferred route", 2, 2},
		callCheck{"calls on the standby route", 0, 1},
	)
	if err == nil || !strings.Contains(err.Error(), "standby") {
		t.Errorf("got %v, want the standby check to fail", err)
	}
}

// drain's last branch: a stream that closes having saved nothing and reported
// no error. langdag produces exactly that when a provider returns an empty
// stream, and a capture must refuse it rather than record an empty node id.
type emptyStreamProvider struct{}

func (emptyStreamProvider) Name() string              { return "empty" }
func (emptyStreamProvider) Models() []types.ModelInfo { return nil }

func (emptyStreamProvider) Complete(context.Context, *types.CompletionRequest) (*types.CompletionResponse, error) {
	return nil, errInjected
}

func (emptyStreamProvider) Stream(context.Context, *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	events := make(chan types.StreamEvent)
	close(events)
	return events, nil
}

func TestDrainRefusesAStreamThatSavedNothing(t *testing.T) {
	client := langdag.NewWithDeps(NewMemStore(), emptyStreamProvider{})
	defer client.Close() //nolint:errcheck // MemStore.Close cannot fail.

	result, err := client.Prompt(context.Background(), "anything", langdag.WithModel("m"))
	if err != nil {
		t.Fatalf("Prompt: %v", err)
	}
	if _, err := drain(result); err == nil {
		t.Error("a stream that saved no node must not report success")
	}
}

// A file the walk cannot read must fail the scan rather than be skipped, or a
// rejected word could hide in it.
func TestScanTreeReportsAnUnreadableFile(t *testing.T) {
	dir := t.TempDir()
	// A symlink pointing nowhere is reported by the walk as an entry and
	// cannot be read. It needs no permission games, so it behaves the same for
	// any user including root.
	if err := symlinkTo("nowhere", dir+"/dangling"); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := ScanTree(dir); err == nil {
		t.Error("an unreadable entry must fail the scan, not be skipped")
	}
}
