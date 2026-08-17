package harness

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"langdag.com/langdag/types"
)

// Turn is one scripted provider answer.
//
// A capture declares the whole conversation up front as a list of these, so the
// fixture's content is written down in one readable place rather than emerging
// from a model. Nothing here contacts a network.
type Turn struct {
	// Text is the assistant's prose. It becomes delta events.
	Text string

	// Blocks, when set, is the response content. Leave it nil for a plain text
	// answer and Text is wrapped into a single text block; set it to carry a
	// tool request, which langdag stores as JSON on the node.
	Blocks []types.ContentBlock

	// StopReason mirrors the provider vocabulary: end_turn, tool_use,
	// max_tokens.
	StopReason string

	// InputTokens and OutputTokens are the usage the response reports.
	// langdag copies them onto the node, and the demo page shows them, so they
	// are declared rather than invented at render time.
	InputTokens  int
	OutputTokens int
}

func (t Turn) blocks() []types.ContentBlock {
	if t.Blocks != nil {
		return t.Blocks
	}
	return []types.ContentBlock{{Type: "text", Text: t.Text}}
}

// ScriptedProvider answers each call with the next Turn in its script.
//
// IT IS NOT A STUB THAT RETURNS ONE CANNED STRING. The branch scenario needs
// two DIFFERENT answers to the SAME request, which no request-matching mock can
// express — the requests are byte-identical. Answering by call index is the
// only thing that can, and it is also what makes a capture reproducible: the
// n-th call always gets the n-th answer.
type ScriptedProvider struct {
	name  string
	model string

	mu          sync.Mutex
	turns       []Turn
	calls       int
	refuseAfter int
	refusalErr  error
}

// NewScriptedProvider builds a provider that will answer with turns, in order.
func NewScriptedProvider(name, model string, turns ...Turn) *ScriptedProvider {
	// refuseAfter is -1 rather than 0 so "not set" cannot be confused with
	// "refuse everything", which is what 0 means once RefuseAfter is called.
	return &ScriptedProvider{name: name, model: model, turns: turns, refuseAfter: -1}
}

// RefuseAfter makes the provider serve its first n calls and refuse every call
// after that with err.
//
// This is how a deployment that goes down MID-CONVERSATION is expressed, and
// it is a distinct condition from one that was never reachable: the fallback
// scenario's subject is a policy whose preferred route answers, then stops
// answering, which a provider that refused from the first call could not show.
// Refusing by running out of script would also work and would report "ran out
// of turns", which reads as a defect in the harness rather than an outage.
func (p *ScriptedProvider) RefuseAfter(n int, err error) *ScriptedProvider {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refuseAfter = n
	p.refusalErr = err
	return p
}

// Name identifies the provider. langdag records it on every assistant node it
// produces, which is what lets the demo attribute an answer.
func (p *ScriptedProvider) Name() string { return p.name }

// Models reports the single model this provider serves.
func (p *ScriptedProvider) Models() []types.ModelInfo {
	return []types.ModelInfo{{
		ID:                      p.model,
		Name:                    p.model,
		ContextWindow:           scriptedContextWindow,
		MaxOutput:               scriptedMaxOutput,
		SupportsFunctionCalling: true,
	}}
}

// Calls reports how many completions have been served. The capture asserts on
// it so a scenario that silently stopped calling the provider fails.
func (p *ScriptedProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// ErrScriptExhausted means a scenario asked for more turns than it declared.
var ErrScriptExhausted = errors.New("harness: scripted provider ran out of turns")

// Complete serves the next turn as a single response.
func (p *ScriptedProvider) Complete(_ context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	turn, index, err := p.next()
	if err != nil {
		return nil, err
	}
	return p.respond(turn, index, req), nil
}

// Stream serves the next turn as delta events followed by a done event.
//
// The channel is filled and closed before it is returned. A capture is not
// testing langdag's concurrency and a provider that streams on its own
// schedule would make the capture's timing, and therefore its latency figures,
// depend on the machine.
func (p *ScriptedProvider) Stream(_ context.Context, req *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	turn, index, err := p.next()
	if err != nil {
		return nil, err
	}
	response := p.respond(turn, index, req)

	events := make(chan types.StreamEvent, len(turn.blocks())+2)
	events <- types.StreamEvent{Type: types.StreamEventStart}
	for _, block := range response.Content {
		if block.Type == "text" && block.Text != "" {
			events <- types.StreamEvent{Type: types.StreamEventDelta, Content: block.Text}
		}
	}
	events <- types.StreamEvent{Type: types.StreamEventDone, Response: response}
	close(events)
	return events, nil
}

func (p *ScriptedProvider) next() (Turn, int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.refuseAfter >= 0 && p.calls >= p.refuseAfter {
		// The refused call is counted. A router that fell through must be able
		// to prove it TRIED this route, and a provider that did not count its
		// refusals would make a router that never called it indistinguishable
		// from one that called it and was turned away.
		p.calls++
		return Turn{}, 0, p.refusalErr
	}
	if p.calls >= len(p.turns) {
		return Turn{}, 0, fmt.Errorf("%w: %s served %d", ErrScriptExhausted, p.name, p.calls)
	}
	turn := p.turns[p.calls]
	index := p.calls
	p.calls++
	return turn, index, nil
}

func (p *ScriptedProvider) respond(turn Turn, index int, req *types.CompletionRequest) *types.CompletionResponse {
	model := p.model
	if req != nil && req.Model != "" {
		model = req.Model
	}
	return &types.CompletionResponse{
		// The id is derived from the provider and the call index rather than
		// generated, so it is stable across runs and needs no normalization.
		ID:         fmt.Sprintf("%s-%d", p.name, index),
		Model:      model,
		Provider:   p.name,
		Content:    turn.blocks(),
		StopReason: turn.StopReason,
		Usage: types.Usage{
			InputTokens:  turn.InputTokens,
			OutputTokens: turn.OutputTokens,
		},
	}
}

// FailingProvider refuses every call with the same error.
//
// It stands in for a primary deployment that is down, and it refuses rather
// than returning a degraded answer because those are different failures and
// only one of them is what a fallback exists for.
type FailingProvider struct {
	name  string
	model string
	err   error

	mu    sync.Mutex
	calls int
}

// NewFailingProvider builds a provider that always refuses with err.
func NewFailingProvider(name, model string, err error) *FailingProvider {
	return &FailingProvider{name: name, model: model, err: err}
}

// Name identifies the provider.
func (p *FailingProvider) Name() string { return p.name }

// Models reports the model this deployment would serve if it were answering.
func (p *FailingProvider) Models() []types.ModelInfo {
	return []types.ModelInfo{{ID: p.model, Name: p.model}}
}

// Calls reports how many times this provider was asked. The fallback scenario
// asserts it is non-zero: a router that never tried the primary would produce
// the same tree as one that tried and failed over.
func (p *FailingProvider) Calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.calls
}

// Complete always fails.
func (p *FailingProvider) Complete(context.Context, *types.CompletionRequest) (*types.CompletionResponse, error) {
	p.count()
	return nil, p.err
}

// Stream always fails.
func (p *FailingProvider) Stream(context.Context, *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	p.count()
	return nil, p.err
}

func (p *FailingProvider) count() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
}

// Route is one deployment in a weighted routing policy.
type Route struct {
	// Provider serves the request when this route is selected.
	Provider interface {
		Complete(context.Context, *types.CompletionRequest) (*types.CompletionResponse, error)
		Stream(context.Context, *types.CompletionRequest) (<-chan types.StreamEvent, error)
		Name() string
		Models() []types.ModelInfo
	}

	// Weight is this route's share of the draw. Weights are relative; the
	// router sums them.
	Weight int
}

// Attempt records one route being tried, and how it went. It is the router's
// own log, NOT a langdag node — a refused call never becomes a node, because
// langdag only stores what a provider actually returned.
//
// The demo page needs it anyway: a tree that shows only the answer that
// succeeded cannot show that a fallback happened, which is the whole subject of
// the third scenario.
type Attempt struct {
	// Route is the provider that was tried.
	Route string `json:"route"`

	// Weight is the route's declared share, carried so the page can show the
	// policy beside the outcome rather than restating it.
	Weight int `json:"weight"`

	// OK reports whether this route served the call.
	OK bool `json:"ok"`

	// Error is the refusal, present only when OK is false.
	Error string `json:"error,omitempty"`
}

// WeightedRouter picks a route by weight and falls through to the remaining
// routes, in weight order, when the pick refuses.
//
// THE WEIGHTING IS REAL AND THE DRAW IS INJECTED. A router that drew from
// math/rand would route correctly and capture differently every run, so the
// draw is a function supplied by the caller. The capture supplies a fixed
// sequence; TestWeightedRouterHonorsWeights supplies every value across the
// range and asserts the split is the declared one. Determinism and a real
// policy are both kept, rather than one being traded for the other.
//
// THIS ROUTER IS THIS REPOSITORY'S, NOT langdag'S. langdag has its own
// deployment-aware routing, and it is configured through langdag.New, which
// builds a SQLite-backed client and real provider credentials. NewWithDeps —
// which is what a capture must use to stay offline and file-free — takes a
// single Provider. So the policy is implemented here, as a Provider that wraps
// other Providers, and the page says so.
type WeightedRouter struct {
	name   string
	routes []Route
	draw   func(total int) int

	mu       sync.Mutex
	attempts []Attempt
}

// NewWeightedRouter builds a router over routes, using draw to pick.
//
// draw receives the total weight and returns a value in [0, total); the route
// whose cumulative weight first exceeds it is selected. Routes are ordered by
// descending weight first, so fallthrough after a refusal goes to the next
// largest share rather than to whichever route happened to be declared next.
func NewWeightedRouter(name string, draw func(total int) int, routes ...Route) *WeightedRouter {
	ordered := make([]Route, len(routes))
	copy(ordered, routes)
	// A stable sort keeps equal weights in declaration order, so a policy with
	// two equal routes is still read top to bottom.
	for i := 1; i < len(ordered); i++ {
		for j := i; j > 0 && ordered[j].Weight > ordered[j-1].Weight; j-- {
			ordered[j], ordered[j-1] = ordered[j-1], ordered[j]
		}
	}
	return &WeightedRouter{name: name, routes: ordered, draw: draw}
}

// Name identifies the router.
func (r *WeightedRouter) Name() string { return r.name }

// Models reports every model any route serves, in route order.
func (r *WeightedRouter) Models() []types.ModelInfo {
	var out []types.ModelInfo
	for _, route := range r.routes {
		out = append(out, route.Provider.Models()...)
	}
	return out
}

// Attempts returns the log of routes tried, in order.
func (r *WeightedRouter) Attempts() []Attempt {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Attempt, len(r.attempts))
	copy(out, r.attempts)
	return out
}

// ErrNoRoutes means the router was built with nothing to route to.
var ErrNoRoutes = errors.New("harness: router has no routes")

// ErrAllRoutesFailed means every route refused.
var ErrAllRoutesFailed = errors.New("harness: every route refused")

// selection returns the route indices to try, best pick first.
func (r *WeightedRouter) selection() []int {
	total := 0
	for _, route := range r.routes {
		total += route.Weight
	}
	first := 0
	if total > 0 && r.draw != nil {
		at := r.draw(total)
		running := 0
		for i, route := range r.routes {
			running += route.Weight
			if at < running {
				first = i
				break
			}
		}
	}
	order := []int{first}
	for i := range r.routes {
		if i != first {
			order = append(order, i)
		}
	}
	return order
}

// Complete routes one completion.
func (r *WeightedRouter) Complete(ctx context.Context, req *types.CompletionRequest) (*types.CompletionResponse, error) {
	if len(r.routes) == 0 {
		return nil, ErrNoRoutes
	}
	for _, i := range r.selection() {
		route := r.routes[i]
		resp, err := route.Provider.Complete(ctx, req)
		r.record(route, err)
		if err == nil {
			return resp, nil
		}
	}
	return nil, ErrAllRoutesFailed
}

// Stream routes one streaming completion, falling through on refusal.
func (r *WeightedRouter) Stream(ctx context.Context, req *types.CompletionRequest) (<-chan types.StreamEvent, error) {
	if len(r.routes) == 0 {
		return nil, ErrNoRoutes
	}
	for _, i := range r.selection() {
		route := r.routes[i]
		events, err := route.Provider.Stream(ctx, req)
		r.record(route, err)
		if err == nil {
			return events, nil
		}
	}
	return nil, ErrAllRoutesFailed
}

func (r *WeightedRouter) record(route Route, err error) {
	attempt := Attempt{Route: route.Provider.Name(), Weight: route.Weight, OK: err == nil}
	if err != nil {
		attempt.Error = err.Error()
	}
	r.mu.Lock()
	r.attempts = append(r.attempts, attempt)
	r.mu.Unlock()
}
