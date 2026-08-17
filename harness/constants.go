package harness

// Every literal the harness needs, named once.
//
// The rule this file exists for: a value that appears in a fixture, in an
// assertion and in a page is three copies of one decision, and the copies drift
// silently. Anything a test or a consumer compares against is declared here.
const (
	// FixtureSchema is stamped into every captured document. A consumer that
	// reads a fixture checks it before trusting the shape, so a change to the
	// capture format cannot be mistaken for a change to the conversation.
	FixtureSchema = "langdag-lab/v1"

	// LangdagVersion is the module version these captures were produced with.
	// go.mod pins it; this restates it INTO the fixture so a committed JSON
	// file carries its own provenance, and CheckPin holds the two together so
	// the restatement cannot drift.
	LangdagVersion = "v0.10.0"

	// scriptedContextWindow and scriptedMaxOutput describe the mock model.
	// They are plausible rather than borrowed from a real model card: nothing
	// here calls a real model, and copying a real one's numbers would invite
	// the page to present them as measured.
	scriptedContextWindow = 200000
	scriptedMaxOutput     = 8192
)

// Provider and model names used by the captures.
//
// PrimaryProvider is named for the vendor whose deployment it stands in for,
// because the third scenario's subject is a policy that prefers one vendor and
// falls back to another, and a policy over "provider-a" and "provider-b"
// teaches nothing. It is still a mock: nothing in this repository holds a
// credential or opens a socket.
const (
	PrimaryProvider  = "anthropic"
	BackupProvider   = "backup"
	PrimaryModel     = "claude-haiku-4-5"
	BackupModel      = "backup-small"
	SingleProvider   = "anthropic"
	SingleModel      = "claude-haiku-4-5"
	assistantRoleTag = "assistant"
)

// The declared weights of the fallback policy.
const (
	PrimaryWeight = 80
	BackupWeight  = 20
)

// Scenario slugs. They name the fixture files and are the ids a consumer keys
// on, so they are part of the contract and not display strings.
const (
	ScenarioToolUse      = "tool-use"
	ScenarioBranch       = "branch-compare"
	ScenarioFallback     = "weighted-fallback"
	fixtureFileExtension = ".json"
)

// Canonical projection settings.
const (
	// nodeOrdinalPrefix and groupOrdinalPrefix replace the two generated
	// identifiers. They are short because they are read in a diff.
	nodeOrdinalPrefix  = "n"
	groupOrdinalPrefix = "g"
)

// CLI vocabulary.
const (
	CommandCapture = "capture"
	CommandVerify  = "verify"

	exitOK    = 0
	exitUsage = 2
	exitFail  = 1
)

// FixturesDir is where captures are written and read, relative to the module
// root.
const FixturesDir = "fixtures"
