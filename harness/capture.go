package harness

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Encode renders a document the way a fixture is stored.
//
// Indented, because a fixture's whole job is to be read in a diff — a
// one-line file would report every change as "the file changed". A trailing
// newline, because it is a text file.
func Encode(doc *Document) ([]byte, error) {
	if doc == nil {
		return nil, fmt.Errorf("harness: nil document")
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// Decode parses a stored fixture and checks its schema stamp.
func Decode(data []byte) (*Document, error) {
	var doc Document
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	if doc.Schema != FixtureSchema {
		return nil, fmt.Errorf("harness: fixture schema is %q, expected %q", doc.Schema, FixtureSchema)
	}
	return &doc, nil
}

// FixturePath is where a scenario's fixture lives under dir.
func FixturePath(dir, slug string) string {
	return filepath.Join(dir, slug+fixtureFileExtension)
}

// Capture builds every scenario and writes its fixture into dir.
//
// It reports the paths written so the caller can print them; a capture that
// said only "done" would not distinguish writing three files from writing none.
func Capture(ctx context.Context, dir string) ([]string, error) {
	return captureInto(ctx, dir, Scenarios())
}

// captureInto is Capture over a supplied scenario list.
//
// The list is a parameter so the tests can drive the failure branches with a
// scenario that refuses. Without it, every "if err != nil" below would be
// unreachable — the three real scenarios cannot fail — and unreachable error
// handling is code nobody has ever seen run.
func captureInto(ctx context.Context, dir string, scenarios []Scenario) ([]string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	var written []string
	for _, scenario := range scenarios {
		doc, err := scenario.Build(ctx)
		if err != nil {
			return written, fmt.Errorf("%s: %w", scenario.Slug, err)
		}
		encoded, err := Encode(doc)
		if err != nil {
			return written, fmt.Errorf("%s: %w", scenario.Slug, err)
		}
		path := FixturePath(dir, scenario.Slug)
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			return written, fmt.Errorf("%s: %w", scenario.Slug, err)
		}
		written = append(written, path)
	}
	return written, nil
}

// Mismatch is one scenario whose re-capture did not match its committed
// fixture.
type Mismatch struct {
	// Slug names the scenario.
	Slug string

	// Reason says what went wrong in one line.
	Reason string
}

// Verify re-runs every capture and compares it byte for byte with what is
// committed in dir.
//
// THIS IS THE WHOLE POINT OF A CAPTURED FIXTURE. A committed JSON file is a
// claim that a program produced these bytes, and nothing about the file itself
// makes the claim true — a fixture edited by hand looks exactly like one that
// was captured. Verify is what turns the claim into something checkable, and it
// is why the canonical projection has to be neither too loose nor too tight: a
// projection that normalized everything would make this pass against any
// fixture at all.
func Verify(ctx context.Context, dir string) ([]Mismatch, error) {
	return verifyAgainst(ctx, dir, Scenarios())
}

// verifyAgainst is Verify over a supplied scenario list, for the same reason
// captureInto takes one.
func verifyAgainst(ctx context.Context, dir string, scenarios []Scenario) ([]Mismatch, error) {
	var out []Mismatch
	for _, scenario := range scenarios {
		path := FixturePath(dir, scenario.Slug)
		committed, err := os.ReadFile(path)
		if err != nil {
			out = append(out, Mismatch{Slug: scenario.Slug, Reason: "committed fixture unreadable: " + err.Error()})
			continue
		}
		doc, err := scenario.Build(ctx)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", scenario.Slug, err)
		}
		fresh, err := Encode(doc)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", scenario.Slug, err)
		}
		if !bytes.Equal(committed, fresh) {
			out = append(out, Mismatch{
				Slug: scenario.Slug,
				Reason: fmt.Sprintf("re-capture differs: committed %d bytes, captured %d",
					len(committed), len(fresh)),
			})
		}
	}
	return out, nil
}

// CheckPin reports whether go.mod pins the langdag version this package names.
//
// The version is stated in two places on purpose — go.mod decides what is
// compiled, and the constant is stamped into every fixture so a committed JSON
// file carries its own provenance. Two statements of one fact drift, so this
// holds them together and the gate runs it. Without it, an upgrade would leave
// three fixtures claiming a version that no longer built them.
func CheckPin(goModPath string) error {
	data, err := os.ReadFile(goModPath)
	if err != nil {
		return err
	}
	want := []byte("langdag.com/langdag " + LangdagVersion)
	if !bytes.Contains(data, want) {
		return fmt.Errorf("harness: %s does not pin %q, which is what every fixture claims produced it",
			goModPath, "langdag.com/langdag "+LangdagVersion)
	}
	return nil
}
