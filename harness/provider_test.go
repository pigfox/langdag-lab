package harness

import (
	"context"
	"errors"
	"testing"

	"langdag.com/langdag/types"
)

func drainStream(t *testing.T, events <-chan types.StreamEvent) (string, *types.CompletionResponse) {
	t.Helper()
	var text string
	var response *types.CompletionResponse
	for event := range events {
		switch event.Type {
		case types.StreamEventDelta:
			text += event.Content
		case types.StreamEventDone:
			response = event.Response
		}
	}
	return text, response
}

func TestScriptedProviderAnswersByCallIndex(t *testing.T) {
	ctx := context.Background()
	p := NewScriptedProvider("test", "model-x",
		Turn{Text: "first", StopReason: "end_turn", InputTokens: 1, OutputTokens: 2},
		Turn{Text: "second", StopReason: "end_turn", InputTokens: 3, OutputTokens: 4},
	)
	if p.Name() != "test" {
		t.Errorf("Name is %q", p.Name())
	}
	models := p.Models()
	if len(models) != 1 || models[0].ID != "model-x" || models[0].ContextWindow != scriptedContextWindow {
		t.Errorf("Models: %+v", models)
	}

	// The SAME request twice must get DIFFERENT answers. That is the whole
	// reason this is index-driven and not request-matched.
	req := &types.CompletionRequest{Model: "model-x"}
	firstEvents, err := p.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	firstText, firstResp := drainStream(t, firstEvents)

	secondEvents, err := p.Stream(ctx, req)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	secondText, secondResp := drainStream(t, secondEvents)

	if firstText != "first" || secondText != "second" {
		t.Errorf("got %q then %q", firstText, secondText)
	}
	if firstResp.ID == secondResp.ID {
		t.Errorf("both responses carry id %q", firstResp.ID)
	}
	if firstResp.Provider != "test" || firstResp.Usage.InputTokens != 1 || firstResp.Usage.OutputTokens != 2 {
		t.Errorf("first response: %+v", firstResp)
	}
	if p.Calls() != 2 {
		t.Errorf("Calls is %d, want 2", p.Calls())
	}

	if _, err := p.Stream(ctx, req); !errors.Is(err, ErrScriptExhausted) {
		t.Errorf("a third call must be refused, got %v", err)
	}
}

func TestScriptedProviderCompleteServesOneTurn(t *testing.T) {
	ctx := context.Background()
	p := NewScriptedProvider("test", "model-x", Turn{Text: "hello", StopReason: "end_turn"})

	resp, err := p.Complete(ctx, &types.CompletionRequest{})
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	// An empty request model leaves the provider's own model in place.
	if resp.Model != "model-x" || len(resp.Content) != 1 || resp.Content[0].Text != "hello" {
		t.Errorf("response: %+v", resp)
	}
	if _, err := p.Complete(ctx, nil); !errors.Is(err, ErrScriptExhausted) {
		t.Errorf("a second call must be refused, got %v", err)
	}
}

// Explicit blocks win over Text, which is how a tool request is expressed.
func TestScriptedProviderCarriesExplicitBlocks(t *testing.T) {
	ctx := context.Background()
	p := NewScriptedProvider("test", "m", Turn{
		Blocks:     []types.ContentBlock{{Type: "tool_use", ID: "t1", Name: "n"}},
		StopReason: "tool_use",
	})
	events, err := p.Stream(ctx, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	text, resp := drainStream(t, events)
	if text != "" {
		t.Errorf("a tool request must emit no text deltas, got %q", text)
	}
	if len(resp.Content) != 1 || resp.Content[0].Type != "tool_use" {
		t.Errorf("content: %+v", resp.Content)
	}
}

var errDown = errors.New("deployment is down")

func TestScriptedProviderRefusesAfterN(t *testing.T) {
	ctx := context.Background()
	p := NewScriptedProvider("primary", "m",
		Turn{Text: "ok", StopReason: "end_turn"},
		Turn{Text: "never reached", StopReason: "end_turn"},
	).RefuseAfter(1, errDown)

	if _, err := p.Stream(ctx, nil); err != nil {
		t.Fatalf("the first call must be served: %v", err)
	}
	if _, err := p.Stream(ctx, nil); !errors.Is(err, errDown) {
		t.Errorf("the second call must be refused, got %v", err)
	}
	// A refused call still counts, so a router that tried this route can prove
	// it did.
	if p.Calls() != 2 {
		t.Errorf("Calls is %d, want 2 — a refusal that is not counted makes a route that was tried look untried", p.Calls())
	}
	if _, err := p.Complete(ctx, nil); !errors.Is(err, errDown) {
		t.Errorf("Complete must refuse too, got %v", err)
	}
}

func TestFailingProviderAlwaysRefuses(t *testing.T) {
	ctx := context.Background()
	p := NewFailingProvider("down", "m", errDown)
	if p.Name() != "down" || len(p.Models()) != 1 || p.Models()[0].ID != "m" {
		t.Errorf("Name/Models: %q %+v", p.Name(), p.Models())
	}
	if _, err := p.Complete(ctx, nil); !errors.Is(err, errDown) {
		t.Errorf("Complete: %v", err)
	}
	if _, err := p.Stream(ctx, nil); !errors.Is(err, errDown) {
		t.Errorf("Stream: %v", err)
	}
	if p.Calls() != 2 {
		t.Errorf("Calls is %d, want 2", p.Calls())
	}
}

// THE WEIGHTS ARE REAL. The capture fixes the draw so its output is
// reproducible, which on its own would be satisfied by a router that ignored
// the weights entirely and always picked the first route. This drives every
// value across the range and asserts the split is the declared one.
func TestWeightedRouterHonorsWeights(t *testing.T) {
	ctx := context.Background()
	total := PrimaryWeight + BackupWeight

	counts := map[string]int{}
	for draw := range total {
		at := draw
		router := NewWeightedRouter("w", func(int) int { return at },
			Route{Provider: NewScriptedProvider(PrimaryProvider, PrimaryModel, Turn{Text: "p"}), Weight: PrimaryWeight},
			Route{Provider: NewScriptedProvider(BackupProvider, BackupModel, Turn{Text: "b"}), Weight: BackupWeight},
		)
		if _, err := router.Complete(ctx, nil); err != nil {
			t.Fatalf("draw %d: %v", draw, err)
		}
		attempts := router.Attempts()
		if len(attempts) != 1 {
			t.Fatalf("draw %d produced %d attempts, want 1", draw, len(attempts))
		}
		counts[attempts[0].Route]++
	}

	if counts[PrimaryProvider] != PrimaryWeight {
		t.Errorf("preferred route served %d of %d draws, want %d",
			counts[PrimaryProvider], total, PrimaryWeight)
	}
	if counts[BackupProvider] != BackupWeight {
		t.Errorf("standby route served %d of %d draws, want %d",
			counts[BackupProvider], total, BackupWeight)
	}
}

func TestWeightedRouterFallsThroughOnRefusal(t *testing.T) {
	ctx := context.Background()
	primary := NewFailingProvider(PrimaryProvider, PrimaryModel, errDown)
	backup := NewScriptedProvider(BackupProvider, BackupModel, Turn{Text: "rescued", StopReason: "end_turn"})

	router := NewWeightedRouter("w", func(int) int { return 0 },
		Route{Provider: primary, Weight: PrimaryWeight},
		Route{Provider: backup, Weight: BackupWeight},
	)
	events, err := router.Stream(ctx, nil)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	text, _ := drainStream(t, events)
	if text != "rescued" {
		t.Errorf("got %q", text)
	}

	attempts := router.Attempts()
	if len(attempts) != 2 {
		t.Fatalf("attempts: %+v", attempts)
	}
	if attempts[0].Route != PrimaryProvider || attempts[0].OK || attempts[0].Error == "" {
		t.Errorf("first attempt should be a recorded refusal: %+v", attempts[0])
	}
	if attempts[0].Weight != PrimaryWeight {
		t.Errorf("attempt did not carry the declared weight: %+v", attempts[0])
	}
	if attempts[1].Route != BackupProvider || !attempts[1].OK {
		t.Errorf("second attempt should be the standby succeeding: %+v", attempts[1])
	}
	if primary.Calls() != 1 {
		t.Errorf("the preferred route was called %d times, want 1", primary.Calls())
	}
}

func TestWeightedRouterReportsWhenEveryRouteRefuses(t *testing.T) {
	ctx := context.Background()
	router := NewWeightedRouter("w", func(int) int { return 0 },
		Route{Provider: NewFailingProvider("a", "m", errDown), Weight: 1},
		Route{Provider: NewFailingProvider("b", "m", errDown), Weight: 1},
	)
	if _, err := router.Stream(ctx, nil); !errors.Is(err, ErrAllRoutesFailed) {
		t.Errorf("Stream: %v", err)
	}
	if _, err := router.Complete(ctx, nil); !errors.Is(err, ErrAllRoutesFailed) {
		t.Errorf("Complete: %v", err)
	}
}

func TestWeightedRouterWithNoRoutes(t *testing.T) {
	ctx := context.Background()
	router := NewWeightedRouter("w", func(int) int { return 0 })
	if _, err := router.Stream(ctx, nil); !errors.Is(err, ErrNoRoutes) {
		t.Errorf("Stream: %v", err)
	}
	if _, err := router.Complete(ctx, nil); !errors.Is(err, ErrNoRoutes) {
		t.Errorf("Complete: %v", err)
	}
	if len(router.Models()) != 0 {
		t.Error("a router with no routes reported models")
	}
}

// A router built with no draw, or with weightless routes, still has to answer.
// It falls back to the first declared route rather than dividing by zero.
func TestWeightedRouterWithoutADrawUsesTheFirstRoute(t *testing.T) {
	ctx := context.Background()
	router := NewWeightedRouter("w", nil,
		Route{Provider: NewScriptedProvider("first", "m", Turn{Text: "a"}), Weight: 0},
		Route{Provider: NewScriptedProvider("second", "m", Turn{Text: "b"}), Weight: 0},
	)
	if _, err := router.Complete(ctx, nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := router.Attempts()[0].Route; got != "first" {
		t.Errorf("served by %q, want first", got)
	}
	if router.Name() != "w" {
		t.Errorf("Name is %q", router.Name())
	}
}

// Routes are ordered by descending weight, so a fallthrough goes to the next
// largest share rather than to whichever route was declared next.
func TestWeightedRouterOrdersRoutesByWeight(t *testing.T) {
	router := NewWeightedRouter("w", func(int) int { return 0 },
		Route{Provider: NewScriptedProvider("small", "m", Turn{Text: "s"}), Weight: 1},
		Route{Provider: NewScriptedProvider("large", "m", Turn{Text: "l"}), Weight: 9},
	)
	models := router.Models()
	if len(models) != 2 {
		t.Fatalf("Models: %+v", models)
	}
	if _, err := router.Complete(context.Background(), nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if got := router.Attempts()[0].Route; got != "large" {
		t.Errorf("a draw of 0 selected %q, want the largest share", got)
	}
}

// Attempts hands out a copy; a caller cannot edit the router's log.
func TestWeightedRouterAttemptsAreACopy(t *testing.T) {
	router := NewWeightedRouter("w", func(int) int { return 0 },
		Route{Provider: NewScriptedProvider("only", "m", Turn{Text: "x"}), Weight: 1},
	)
	if _, err := router.Complete(context.Background(), nil); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	got := router.Attempts()
	got[0].Route = "tampered"
	if router.Attempts()[0].Route != "only" {
		t.Error("Attempts returned the router's own slice")
	}
}
