package harness

import (
	"context"
	"errors"
	"fmt"

	"langdag.com/langdag"
	"langdag.com/langdag/types"
)

// Document is one captured scenario, and it is the file a consumer reads.
//
// It carries the tree AND the things the tree cannot say about itself. The
// fallback scenario is the reason: a refused provider call never becomes a
// node, because langdag stores what a provider returned and a refusal returns
// nothing — so a page rendering only the nodes could not show that a fallback
// happened, which is that scenario's entire subject.
type Document struct {
	// Schema is the capture format, checked before the rest is trusted.
	Schema string `json:"schema"`

	// LangdagVersion is the module version that produced this tree.
	LangdagVersion string `json:"langdag_version"`

	// Scenario is the slug, and names the file.
	Scenario string `json:"scenario"`

	// Title and Summary are display copy, captured here rather than written
	// into the website so the description of a tree travels with it.
	Title   string `json:"title"`
	Summary string `json:"summary"`

	// Policy is the declared routing policy, present only on a scenario that
	// has one.
	Policy []PolicyRoute `json:"policy,omitempty"`

	// Attempts is the router's own log of routes tried. Present only on a
	// scenario with a policy. NOT langdag nodes — see the type comment.
	Attempts []Attempt `json:"attempts,omitempty"`

	// Nodes is the canonicalized subtree, in walk order.
	Nodes []types.Node `json:"nodes"`
}

// PolicyRoute is one declared route and its share.
type PolicyRoute struct {
	Route  string `json:"route"`
	Weight int    `json:"weight"`
}

// Scenario is a named capture.
type Scenario struct {
	// Slug names the fixture file and is the consumer's key.
	Slug string

	// Build runs the conversation and returns the document.
	Build func(ctx context.Context) (*Document, error)
}

// Scenarios returns every capture this repository produces, in a stable order.
func Scenarios() []Scenario {
	return []Scenario{
		{Slug: ScenarioToolUse, Build: func(ctx context.Context) (*Document, error) {
			return buildToolUseIn(ctx, NewMemStore())
		}},
		{Slug: ScenarioBranch, Build: func(ctx context.Context) (*Document, error) {
			return buildBranchIn(ctx, NewMemStore())
		}},
		{Slug: ScenarioFallback, Build: func(ctx context.Context) (*Document, error) {
			return buildFallbackIn(ctx, NewMemStore())
		}},
	}
}

// drain consumes a prompt result to completion and returns the saved node id.
//
// EVERY CAPTURE GOES THROUGH HERE. langdag saves the assistant node from a
// background goroutine and reports the id on the stream's final chunk, so a
// caller that reads PromptResult.NodeID without draining races the write and
// usually loses. Draining in one place means no scenario can get that wrong
// individually.
func drain(result *langdag.PromptResult) (string, error) {
	if result == nil {
		return "", errors.New("harness: nil prompt result")
	}
	for chunk := range result.Stream {
		if chunk.Error != nil {
			return "", chunk.Error
		}
	}
	// NO "was a node actually saved" CHECK, and its absence is a decision
	// rather than an oversight, because the obvious version of this function
	// has one.
	//
	// langdag's buildResult guarantees the two outcomes are exhaustive: it
	// tracks whether the stream terminated, and a stream that ends without
	// either a node-saved or an error event gets a synthetic error chunk
	// ("stream ended without completion") appended before the channel closes.
	// So by the time the range above finishes, either an error was seen or the
	// node id is set. A check here could not fail, and an unreachable branch is
	// one no test can cover and no reader can trust.
	//
	// It was written first and measured second — an empty-stream provider was
	// driven through it and the error arrived on the stream, not from a missing
	// id. TestDrainRefusesAStreamThatSavedNothing keeps driving that case, so a
	// langdag upgrade that stopped synthesizing the chunk fails there rather
	// than silently returning an empty id from here.
	return result.GetNodeID(), nil
}

// callCheck is one expectation about how many times something was called.
type callCheck struct {
	// what names the thing counted, for the message.
	what string

	// got and want are the observed and expected counts.
	got, want int
}

// finish assembles a document after checking that the conversation actually
// made the calls it was supposed to.
//
// WHY THIS IS A FUNCTION AND NOT AN "IF" IN EACH BUILDER. The checks guard
// against langdag changing how many provider calls a turn costs — a tree that
// looked right while a route was never tried, or a turn never issued. They can
// only fail if the library changes, so written inline they would be four
// branches no test could reach. Here they are one call site each, and both
// outcomes are driven directly by TestFinishChecksCallCounts.
func finish(doc *Document, checks ...callCheck) (*Document, error) {
	for _, c := range checks {
		if c.got != c.want {
			return nil, fmt.Errorf("harness: %s expected %d %s, got %d",
				doc.Scenario, c.want, c.what, c.got)
		}
	}
	return doc, nil
}

// subtree canonicalizes the whole tree the root node heads.
func subtree(ctx context.Context, client *langdag.Client, rootID string) ([]types.Node, error) {
	nodes, err := client.GetSubtree(ctx, rootID)
	if err != nil {
		return nil, err
	}
	return Canonicalize(nodes)
}

// rootOf returns the id of the tree the given node belongs to.
func rootOf(ctx context.Context, client *langdag.Client, nodeID string) (string, error) {
	node, err := client.GetNode(ctx, nodeID)
	if err != nil {
		return "", err
	}
	if node == nil {
		return "", fmt.Errorf("harness: node %s vanished", nodeID)
	}
	return node.RootID, nil
}

// The tool the first scenario's model asks for. Declared once: the definition
// sent to the provider and the id echoed back in the result have to agree, and
// a mismatch is exactly what langdag's orphan index exists to catch.
const (
	toolName      = "lookup_timezone"
	toolCallID    = "toolu_tokyo_1"
	toolInputJSON = `{"city":"Tokyo"}`
	toolSchema    = `{"type":"object","properties":{"city":{"type":"string"}},"required":["city"]}`
	toolResult    = `[{"type":"tool_result","tool_use_id":"` + toolCallID + `","content":"Asia/Tokyo, UTC+09:00, no daylight saving"}]`
)

// buildToolUse captures a tool round trip: the model asks for a tool, the
// caller answers, the model uses the answer.
//
// WHAT THIS SHOWS THAT A PLAIN EXCHANGE DOES NOT: langdag does not run tools.
// It stores the request as content blocks on the assistant node, indexes the
// tool id, and hands control back — the loop is the caller's. The captured tree
// is therefore four nodes rather than two, and the second user node holds a
// tool_result rather than anything a person typed.
func buildToolUseIn(ctx context.Context, store langdag.Storage) (*Document, error) {
	provider := NewScriptedProvider(SingleProvider, SingleModel,
		Turn{
			Blocks: []types.ContentBlock{{
				Type:  "tool_use",
				ID:    toolCallID,
				Name:  toolName,
				Input: []byte(toolInputJSON),
			}},
			StopReason:   "tool_use",
			InputTokens:  412,
			OutputTokens: 57,
		},
		Turn{
			Text:         "Tokyo is on Asia/Tokyo, which is UTC+09:00 all year — Japan does not observe daylight saving, so the offset never shifts.",
			StopReason:   "end_turn",
			InputTokens:  498,
			OutputTokens: 34,
		},
	)
	client := langdag.NewWithDeps(store, provider)
	defer client.Close() //nolint:errcheck // MemStore.Close cannot fail.

	tools := []types.ToolDefinition{{
		Name:        toolName,
		Description: "Look up the IANA timezone and UTC offset for a city.",
		InputSchema: []byte(toolSchema),
	}}

	first, err := client.Prompt(ctx, "What is the current UTC offset for Tokyo?",
		langdag.WithModel(SingleModel), langdag.WithTools(tools))
	if err != nil {
		return nil, err
	}
	askedID, err := drain(first)
	if err != nil {
		return nil, err
	}

	second, err := client.PromptFrom(ctx, askedID, toolResult,
		langdag.WithModel(SingleModel), langdag.WithTools(tools))
	if err != nil {
		return nil, err
	}
	if _, err = drain(second); err != nil {
		return nil, err
	}

	rootID, err := rootOf(ctx, client, askedID)
	if err != nil {
		return nil, err
	}
	nodes, err := subtree(ctx, client, rootID)
	if err != nil {
		return nil, err
	}
	return finish(&Document{
		Schema:         FixtureSchema,
		LangdagVersion: LangdagVersion,
		Scenario:       ScenarioToolUse,
		Title:          "Tool round trip",
		Summary: "The model asks for a tool, the caller answers, the model uses the answer. " +
			"langdag stores the request as content blocks and indexes the tool id; running the tool is the caller's job, " +
			"which is why this is four nodes and the second user turn holds a tool result rather than typed text.",
		Nodes: nodes,
	}, callCheck{"provider calls", provider.Calls(), 2})
}

// buildBranch captures two divergent continuations of one node.
//
// The two continuations send the SAME message. That is the point and it is why
// the provider answers by call index rather than by matching the request: no
// request-matching mock can return two different answers to two identical
// requests, and two identical answers would not be a divergence.
func buildBranchIn(ctx context.Context, store langdag.Storage) (*Document, error) {
	provider := NewScriptedProvider(SingleProvider, SingleModel,
		Turn{
			Text:         "Two shapes fit: a token bucket, which allows a burst up to its capacity and refills at a steady rate, or a leaky bucket, which drains at a fixed rate and shapes the burst away entirely.",
			StopReason:   "end_turn",
			InputTokens:  96,
			OutputTokens: 41,
		},
		Turn{
			Text:         "Token bucket. Size the capacity to the burst you are willing to absorb and the refill rate to the sustained limit; a client that has been quiet can spend its savings immediately, which is usually what an API caller expects.",
			StopReason:   "end_turn",
			InputTokens:  151,
			OutputTokens: 48,
		},
		Turn{
			Text:         "Token bucket, but keep one bucket per client rather than one for the process, and evict an idle bucket on a timer. A single shared bucket makes one noisy caller everyone else's rate limit.",
			StopReason:   "end_turn",
			InputTokens:  151,
			OutputTokens: 43,
		},
	)
	client := langdag.NewWithDeps(store, provider)
	defer client.Close() //nolint:errcheck // MemStore.Close cannot fail.

	opening, err := client.Prompt(ctx, "I need to rate limit an HTTP API. What shape should it be?",
		langdag.WithModel(SingleModel))
	if err != nil {
		return nil, err
	}
	pivotID, err := drain(opening)
	if err != nil {
		return nil, err
	}

	const followUp = "Go with the token bucket. What should I watch out for?"
	for range 2 {
		branch, err := client.PromptFrom(ctx, pivotID, followUp, langdag.WithModel(SingleModel))
		if err != nil {
			return nil, err
		}
		if _, err = drain(branch); err != nil {
			return nil, err
		}
	}

	rootID, err := rootOf(ctx, client, pivotID)
	if err != nil {
		return nil, err
	}
	nodes, err := subtree(ctx, client, rootID)
	if err != nil {
		return nil, err
	}
	return finish(&Document{
		Schema:         FixtureSchema,
		LangdagVersion: LangdagVersion,
		Scenario:       ScenarioBranch,
		Title:          "Two continuations of one node",
		Summary: "One assistant answer, continued twice with the same follow-up and answered differently each time. " +
			"Both continuations keep the whole path above them, so the two lines of reasoning share a history and diverge only below the node they left from.",
		Nodes: nodes,
	}, callCheck{"provider calls", provider.Calls(), 3})
}

// errPrimaryOverloaded is the refusal the preferred deployment starts
// returning. Its wording matches what an overloaded API says, because the
// scenario is about a route that is up and declining work rather than one that
// is unreachable.
var errPrimaryOverloaded = errors.New("529 overloaded: upstream declined the request")

// buildFallback captures a weighted policy whose preferred route stops
// answering mid-conversation.
//
// THE WEIGHTS ARE REAL AND THE DRAW IS FIXED. The router selects by cumulative
// weight from an injected draw; this capture injects a draw that always lands
// in the preferred route's share, so the capture is byte-reproducible while the
// policy stays a real weighted selection. TestWeightedRouterHonorsWeights
// drives every value across the range and asserts the split is 80/20.
func buildFallbackIn(ctx context.Context, store langdag.Storage) (*Document, error) {
	primary := NewScriptedProvider(PrimaryProvider, PrimaryModel,
		Turn{
			Text:         "Start with the read path. A cache in front of the slowest query buys the most headroom for the least risk, because a stale read is recoverable and a stale write is not.",
			StopReason:   "end_turn",
			InputTokens:  88,
			OutputTokens: 36,
		},
	).RefuseAfter(1, errPrimaryOverloaded)

	backup := NewScriptedProvider(BackupProvider, BackupModel,
		Turn{
			Text:         "For the write path, batch on the client and make the handler idempotent before you touch the database. Batching without idempotency turns one retry into a duplicate row.",
			StopReason:   "end_turn",
			InputTokens:  144,
			OutputTokens: 33,
		},
	)

	// A draw of 0 always lands in the first cumulative band, which after the
	// router's descending sort is the preferred route. Fixed rather than
	// random: see the doc comment.
	router := NewWeightedRouter("weighted",
		func(int) int { return 0 },
		Route{Provider: primary, Weight: PrimaryWeight},
		Route{Provider: backup, Weight: BackupWeight},
	)

	client := langdag.NewWithDeps(store, router)
	defer client.Close() //nolint:errcheck // MemStore.Close cannot fail.

	opening, err := client.Prompt(ctx, "Where should I start optimizing a service that is slow under load?",
		langdag.WithModel(PrimaryModel))
	if err != nil {
		return nil, err
	}
	servedID, err := drain(opening)
	if err != nil {
		return nil, err
	}

	next, err := client.PromptFrom(ctx, servedID, "And the write path?", langdag.WithModel(PrimaryModel))
	if err != nil {
		return nil, err
	}
	if _, err = drain(next); err != nil {
		return nil, err
	}

	rootID, err := rootOf(ctx, client, servedID)
	if err != nil {
		return nil, err
	}
	nodes, err := subtree(ctx, client, rootID)
	if err != nil {
		return nil, err
	}

	// A router that never tried the preferred route would produce a tree that
	// looks the same. Assert the attempts happened rather than inferring them.
	return finish(&Document{
		Schema:         FixtureSchema,
		LangdagVersion: LangdagVersion,
		Scenario:       ScenarioFallback,
		Title:          "Weighted policy, preferred route declines",
		Summary: "A policy that sends most traffic to one deployment and keeps a second in reserve. " +
			"The preferred route answers the first turn, then declines the second, and the standby answers it. " +
			"Each answer carries the name of the deployment that served it; the declined attempt is in the attempt log rather than the tree, because a call that returned nothing produces no node.",
		Policy: []PolicyRoute{
			{Route: PrimaryProvider, Weight: PrimaryWeight},
			{Route: BackupProvider, Weight: BackupWeight},
		},
		Attempts: router.Attempts(),
		Nodes:    nodes,
	},
		callCheck{"attempts on the preferred route", primary.Calls(), 2},
		callCheck{"calls on the standby route", backup.Calls(), 1},
	)
}
