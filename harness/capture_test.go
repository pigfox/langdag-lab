package harness

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncodeAndDecodeRoundTrip(t *testing.T) {
	doc := &Document{
		Schema: FixtureSchema, LangdagVersion: LangdagVersion,
		Scenario: "x", Title: "t", Summary: "s",
	}
	encoded, err := Encode(doc)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if !strings.HasSuffix(string(encoded), "}\n") {
		t.Error("a fixture must end with a newline")
	}
	if !strings.Contains(string(encoded), "\n  \"schema\"") {
		t.Error("a fixture must be indented, or every change reads as one line changing")
	}
	back, err := Decode(encoded)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if back.Scenario != "x" || back.Title != "t" {
		t.Errorf("round trip lost content: %+v", back)
	}
}

func TestEncodeRejectsNil(t *testing.T) {
	if _, err := Encode(nil); err == nil {
		t.Error("Encode(nil) must fail")
	}
}

// A fixture whose schema stamp does not match is refused rather than read as
// the current shape.
func TestDecodeChecksTheSchemaStamp(t *testing.T) {
	if _, err := Decode([]byte(`{"schema":"something-else"}`)); err == nil {
		t.Error("a foreign schema must be refused")
	}
	if _, err := Decode([]byte(`not json`)); err == nil {
		t.Error("invalid JSON must be refused")
	}
}

func TestCaptureWritesEveryScenario(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "nested", FixturesDir)

	written, err := Capture(ctx, dir)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if len(written) != len(Scenarios()) {
		t.Fatalf("wrote %d files for %d scenarios", len(written), len(Scenarios()))
	}
	for _, scenario := range Scenarios() {
		path := FixturePath(dir, scenario.Slug)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", scenario.Slug, err)
		}
		doc, err := Decode(data)
		if err != nil {
			t.Fatalf("%s: %v", scenario.Slug, err)
		}
		if doc.Scenario != scenario.Slug {
			t.Errorf("%s holds %q", path, doc.Scenario)
		}
	}
}

func TestCaptureReportsAnUnwritableDirectory(t *testing.T) {
	// A path whose parent is a FILE cannot be made into a directory.
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if _, err := Capture(context.Background(), filepath.Join(blocker, "under")); err == nil {
		t.Error("Capture must report a directory it cannot create")
	}
}

// VERIFY MUST PASS ON WHAT CAPTURE JUST WROTE, and must fail on anything else.
// Both directions, because a verify that always passed would look identical to
// a working one on a clean tree.
func TestVerifyAgreesWithCaptureAndNoticesAnEdit(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()

	if _, err := Capture(ctx, dir); err != nil {
		t.Fatalf("Capture: %v", err)
	}
	mismatches, err := Verify(ctx, dir)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(mismatches) != 0 {
		t.Fatalf("a fresh capture did not verify: %+v", mismatches)
	}

	// Edit one fixture by hand — the exact thing a captured fixture cannot
	// otherwise detect.
	target := FixturePath(dir, ScenarioToolUse)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	edited := strings.Replace(string(data), "UTC+09:00", "UTC+10:00", 1)
	if edited == string(data) {
		t.Fatal("the edit did not apply, so this test proves nothing")
	}
	if err := os.WriteFile(target, []byte(edited), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	mismatches, err = Verify(ctx, dir)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(mismatches) != 1 || mismatches[0].Slug != ScenarioToolUse {
		t.Fatalf("a hand-edited fixture went unnoticed: %+v", mismatches)
	}
	if mismatches[0].Reason == "" {
		t.Error("a mismatch must say something")
	}
}

func TestVerifyReportsAMissingFixture(t *testing.T) {
	mismatches, err := Verify(context.Background(), t.TempDir())
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(mismatches) != len(Scenarios()) {
		t.Fatalf("an empty directory produced %d mismatches, want %d", len(mismatches), len(Scenarios()))
	}
	for _, m := range mismatches {
		if !strings.Contains(m.Reason, "unreadable") {
			t.Errorf("%s: %q", m.Slug, m.Reason)
		}
	}
}

// go.mod and the constant every fixture is stamped with must agree.
func TestCheckPinHoldsGoModAndTheStampTogether(t *testing.T) {
	if err := CheckPin(filepath.Join("..", "go.mod")); err != nil {
		t.Errorf("the module pin and the stamped version disagree: %v", err)
	}
	if err := CheckPin(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a missing go.mod must be reported")
	}

	// And it must actually be reading the version, not merely the presence of
	// the module.
	wrong := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(wrong, []byte("require langdag.com/langdag v0.0.1\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := CheckPin(wrong); err == nil {
		t.Error("a go.mod pinning a different version must be refused")
	}
}

func TestFixturePathNamesTheSlug(t *testing.T) {
	if got := FixturePath("d", "s"); got != filepath.Join("d", "s.json") {
		t.Errorf("got %q", got)
	}
}
