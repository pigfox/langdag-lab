package harness

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func run(args ...string) (code int, stdout, stderr string) {
	var out, errBuf bytes.Buffer
	code = Main(append([]string{"langdag-lab"}, args...), &out, &errBuf)
	return code, out.String(), errBuf.String()
}

func TestCLICaptureThenVerify(t *testing.T) {
	dir := t.TempDir()

	code, stdout, stderr := run(CommandCapture, dir)
	if code != exitOK {
		t.Fatalf("capture exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "captured 3 scenarios") {
		t.Errorf("capture said: %q", stdout)
	}
	for _, scenario := range Scenarios() {
		if !strings.Contains(stdout, scenario.Slug) {
			t.Errorf("capture did not report %s: %q", scenario.Slug, stdout)
		}
	}

	code, stdout, stderr = run(CommandVerify, dir)
	if code != exitOK {
		t.Fatalf("verify exited %d: %s", code, stderr)
	}
	if !strings.Contains(stdout, "verified 3 scenarios") {
		t.Errorf("verify said: %q", stdout)
	}
}

// A verify that found a difference must exit non-zero. A gate whose checker
// prints and proceeds is a log line.
func TestCLIVerifyExitsNonZeroOnAMismatch(t *testing.T) {
	dir := t.TempDir()
	if code, _, stderr := run(CommandCapture, dir); code != exitOK {
		t.Fatalf("capture exited %d: %s", code, stderr)
	}

	target := FixturePath(dir, ScenarioBranch)
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	edited := strings.Replace(string(data), "token bucket", "leaky bucket", 1)
	if edited == string(data) {
		t.Fatal("the edit did not apply, so this test proves nothing")
	}
	if err := os.WriteFile(target, []byte(edited), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	code, _, stderr := run(CommandVerify, dir)
	if code == exitOK {
		t.Fatal("verify passed over a hand-edited fixture")
	}
	if !strings.Contains(stderr, "MISMATCH") || !strings.Contains(stderr, ScenarioBranch) {
		t.Errorf("verify did not name the offender: %q", stderr)
	}
}

func TestCLIVerifyReportsAMissingDirectory(t *testing.T) {
	code, _, stderr := run(CommandVerify, filepath.Join(t.TempDir(), "absent"))
	if code != exitFail {
		t.Errorf("exited %d, want %d", code, exitFail)
	}
	if !strings.Contains(stderr, "MISMATCH") {
		t.Errorf("stderr: %q", stderr)
	}
}

func TestCLICaptureReportsAFailure(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	code, _, stderr := run(CommandCapture, filepath.Join(blocker, "under"))
	if code != exitFail {
		t.Errorf("exited %d, want %d", code, exitFail)
	}
	if !strings.Contains(stderr, "capture failed") {
		t.Errorf("stderr: %q", stderr)
	}
}

func TestCLIUsage(t *testing.T) {
	for _, args := range [][]string{{}, {"nonsense"}} {
		code, _, stderr := run(args...)
		if code != exitUsage {
			t.Errorf("%v exited %d, want %d", args, code, exitUsage)
		}
		if !strings.Contains(stderr, "usage:") {
			t.Errorf("%v printed no usage: %q", args, stderr)
		}
	}
}

// With no directory argument the commands work against the repository's own
// fixtures, which is how the gate invokes them.
func TestCLIDefaultsToTheFixturesDirectory(t *testing.T) {
	if !strings.Contains(Usage, FixturesDir) {
		t.Errorf("the usage text does not name the default directory: %q", Usage)
	}
	// Run verify from the module root against the committed fixtures.
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Chdir(filepath.Dir(cwd))
	code, stdout, stderr := run(CommandVerify)
	if code != exitOK {
		t.Fatalf("the committed fixtures do not verify: %s%s", stdout, stderr)
	}
}
