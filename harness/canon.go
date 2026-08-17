package harness

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"

	"langdag.com/langdag/types"
)

// The canonical projection: what two captures of the same conversation must
// agree on.
//
// THIS FILE IS A CLAIM ABOUT WHAT IS NOISE, and it is the only place the claim
// is written down. Every value it rewrites is one that differs between two runs
// of the same code for reasons that have nothing to do with the conversation:
//
//	ID, ParentID, RootID   uuid.New() per node, three sites in langdag's
//	                       conversation manager.
//	OutputGroupID          uuid.New() again, assigned when a truncated answer
//	                       is continued.
//	CreatedAt              time.Now() per node.
//	LatencyMs              wall-clock around the provider call. Zero on a mock
//	                       most of the time and not reliably so — a loaded
//	                       machine crosses the millisecond boundary.
//
// THE DANGER IS OVER-NORMALIZING, NOT UNDER-NORMALIZING. A projection that
// flattened enough would make any two conversations equal, and the comparison
// would then prove nothing while reporting success — which is worse than no
// comparison at all. So every rule is tested in BOTH directions: a re-run's
// different uuids must NOT read as a difference, and a moved node, a renamed
// provider, a changed token count or an edited word MUST.
//
// WHAT IS DELIBERATELY NOT NORMALIZED, and why, because an unstated exclusion
// reads as an oversight:
//
//	Sequence, NodeType, Content, Provider, Model, StopReason, Status, Title,
//	SystemPrompt and every token counter are the substance. Rewriting any of
//	them is how a capture starts agreeing with a conversation it did not have.
//
//	Metadata passes through byte for byte. It carries langdag's model
//	resolution and a pricing snapshot read from the catalog EMBEDDED IN THE
//	PINNED MODULE, so it is a function of go.mod rather than of the clock.
//	Normalizing it would hide a langdag upgrade that changed what the library
//	records, which is exactly the kind of change a consumer of these fixtures
//	needs to see.

// ErrNoRoot means the node set has no parentless node.
var ErrNoRoot = errors.New("harness: node set has no root")

// ErrManyRoots means the node set has more than one parentless node, so it is
// not one tree.
var ErrManyRoots = errors.New("harness: node set has more than one root")

// ErrAmbiguousSiblings means two children of one parent have identical
// subtrees, so no deterministic order between them exists.
//
// It is an ERROR rather than a silent tiebreak on the raw uuid. A tiebreak
// there would produce a canonical form that depends on the very identifiers
// this projection exists to remove — it would pass, and it would pass
// differently on the next run. Two genuinely identical subtrees are also a
// capture worth looking at rather than ordering around: it means a scenario
// asked the same question twice and got the same answer, which is not the
// divergence it was written to show.
var ErrAmbiguousSiblings = errors.New("harness: siblings are indistinguishable, so no canonical order exists")

// ErrOrphanNode means a node's parent is not in the set, so the input is not a
// complete subtree.
var ErrOrphanNode = errors.New("harness: node's parent is not in the set")

// ErrCycle means nodes below the root name each other as parents, so the set is
// not a tree.
//
// It cannot arise through langdag, whose nodes always name a parent that
// already exists. It is detected anyway because the alternative is not a wrong
// answer but a stack overflow: the shape key recurses through children, and a
// cycle among non-root nodes satisfies every other check in this function —
// each node's parent IS present, and there IS exactly one parentless node.
var ErrCycle = errors.New("harness: nodes below the root form a cycle")

// Canonicalize projects nodes into the form two captures are compared in.
//
// The input is what langdag's GetSubtree returned. The output is ordered by the
// walk, which is the order the ordinals were assigned in, so a consumer can
// read the slice top to bottom and see the conversation.
func Canonicalize(nodes []*types.Node) ([]types.Node, error) {
	if len(nodes) == 0 {
		return nil, ErrNoRoot
	}

	byID := make(map[string]*types.Node, len(nodes))
	for _, n := range nodes {
		if n == nil {
			return nil, errors.New("harness: nil node in set")
		}
		if _, dup := byID[n.ID]; dup {
			return nil, fmt.Errorf("harness: duplicate node id %s", n.ID)
		}
		byID[n.ID] = n
	}

	children := map[string][]*types.Node{}
	var roots []*types.Node
	for _, n := range nodes {
		if n.ParentID == "" {
			roots = append(roots, n)
			continue
		}
		if _, ok := byID[n.ParentID]; !ok {
			return nil, fmt.Errorf("%w: %s", ErrOrphanNode, n.ID)
		}
		children[n.ParentID] = append(children[n.ParentID], n)
	}
	switch {
	case len(roots) == 0:
		return nil, ErrNoRoot
	case len(roots) > 1:
		return nil, fmt.Errorf("%w: %d", ErrManyRoots, len(roots))
	}

	// Order every sibling group by the substance, never by the identifier.
	//
	// THE KEY IS THE WHOLE SUBTREE, NOT THE NODE, and that is not a
	// refinement — it is what makes the two-branch scenario expressible at
	// all. Branching twice from one node with the SAME message produces two
	// user nodes identical in sequence, type and content; only their
	// descendants differ. A node-only comparison declares them ambiguous and
	// refuses a tree that is perfectly well determined.
	keys := map[string]string{}
	// visiting guards the recursion. A set can hold a cycle among non-root
	// nodes — a and b each naming the other as parent, alongside a real root —
	// and every check above passes it: both parents ARE in the set, and there
	// is exactly one parentless node. Without this the walk would recurse until
	// the stack ran out, which is a crash rather than a refusal.
	visiting := map[string]bool{}
	cyclic := false
	var shapeKey func(n *types.Node) string
	shapeKey = func(n *types.Node) string {
		if k, done := keys[n.ID]; done {
			return k
		}
		if visiting[n.ID] {
			cyclic = true
			return ""
		}
		visiting[n.ID] = true
		defer delete(visiting, n.ID)
		kids := children[n.ID]
		childKeys := make([]string, 0, len(kids))
		for _, kid := range kids {
			childKeys = append(childKeys, shapeKey(kid))
		}
		sort.Strings(childKeys)

		h := sha256.New()
		// Every field written here is one a difference must survive into the
		// ordering. Length-prefixing each keeps two adjacent fields from
		// running together — without it, provider "ab" + model "c" and
		// provider "a" + model "bc" would hash the same.
		for _, field := range append([]string{
			strconv.Itoa(n.Sequence),
			string(n.NodeType),
			n.Content,
			n.Provider,
			n.Model,
			n.StopReason,
			n.Status,
			n.Title,
			n.SystemPrompt,
			strconv.Itoa(n.TokensIn),
			strconv.Itoa(n.TokensOut),
			string(n.Metadata),
		}, childKeys...) {
			fmt.Fprintf(h, "%d:%s|", len(field), field)
		}
		k := hex.EncodeToString(h.Sum(nil))
		keys[n.ID] = k
		return k
	}
	for _, n := range nodes {
		shapeKey(n)
	}
	if cyclic {
		return nil, ErrCycle
	}
	for parent, kids := range children {
		sort.SliceStable(kids, func(i, j int) bool { return keys[kids[i].ID] < keys[kids[j].ID] })
		for i := 1; i < len(kids); i++ {
			if keys[kids[i-1].ID] == keys[kids[i].ID] {
				return nil, fmt.Errorf("%w: two children of %s", ErrAmbiguousSiblings, parent)
			}
		}
	}

	// Depth-first from the root, children in the order just fixed. Depth-first
	// rather than breadth-first because it keeps a conversation's turns
	// adjacent in the output, which is how a reader follows it.
	ordinal := map[string]string{}
	group := map[string]string{}
	out := make([]types.Node, 0, len(nodes))

	var walk func(n *types.Node)
	walk = func(n *types.Node) {
		ordinal[n.ID] = nodeOrdinalPrefix + strconv.Itoa(len(ordinal))
		canon := *n
		canon.ID = ordinal[n.ID]
		canon.ParentID = ordinal[n.ParentID]
		canon.RootID = ordinal[roots[0].ID]
		if n.OutputGroupID != "" {
			if _, seen := group[n.OutputGroupID]; !seen {
				group[n.OutputGroupID] = groupOrdinalPrefix + strconv.Itoa(len(group))
			}
			canon.OutputGroupID = group[n.OutputGroupID]
		}
		canon.CreatedAt = time.Time{}
		canon.LatencyMs = 0
		out = append(out, canon)
		for _, kid := range children[n.ID] {
			walk(kid)
		}
	}
	walk(roots[0])

	// No "did the walk reach everything" guard, and its absence is a decision.
	// Every non-root node's parent is verified present, there is exactly one
	// parentless node, and a cycle is refused above — so following parents from
	// any node terminates at the root and the walk reaches all of them. A guard
	// here could not fail, and a branch that cannot fail is one no test can
	// cover and no reader can trust.
	return out, nil
}
