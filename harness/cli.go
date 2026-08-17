package harness

import (
	"context"
	"fmt"
	"io"
)

// Usage is printed when the command is missing or unknown.
const Usage = `langdag-lab — build langdag conversation trees and capture them as fixtures.

usage:
  langdag-lab capture [dir]   build every scenario and write its fixture
  langdag-lab verify [dir]    re-run every capture and compare with what is committed

dir defaults to ` + FixturesDir + `.
`

// Main is the whole command. It returns the process exit code and writes
// nothing to the real stdout or stderr, so the tests drive it directly rather
// than spawning a binary.
//
// A subprocess test would be measuring the same code through a slower and less
// observable seam, and it would leave the coverage of everything below here
// invisible to the gate.
func Main(args []string, stdout, stderr io.Writer) int {
	return mainWith(args, stdout, stderr, Scenarios())
}

// mainWith is Main over a supplied scenario list, so the tests can drive the
// branch where a capture refuses. See captureInto for why that seam exists.
func mainWith(args []string, stdout, stderr io.Writer, scenarios []Scenario) int {
	if len(args) < 2 {
		fmt.Fprint(stderr, Usage)
		return exitUsage
	}

	dir := FixturesDir
	if len(args) > 2 {
		dir = args[2]
	}
	ctx := context.Background()

	switch args[1] {
	case CommandCapture:
		written, err := captureInto(ctx, dir, scenarios)
		if err != nil {
			fmt.Fprintf(stderr, "capture failed: %v\n", err)
			return exitFail
		}
		for _, path := range written {
			fmt.Fprintf(stdout, "wrote %s\n", path)
		}
		fmt.Fprintf(stdout, "captured %d scenarios\n", len(written))
		return exitOK

	case CommandVerify:
		mismatches, err := verifyAgainst(ctx, dir, scenarios)
		if err != nil {
			fmt.Fprintf(stderr, "verify could not run: %v\n", err)
			return exitFail
		}
		if len(mismatches) > 0 {
			for _, m := range mismatches {
				fmt.Fprintf(stderr, "MISMATCH %s: %s\n", m.Slug, m.Reason)
			}
			fmt.Fprintf(stderr, "%d of %d scenarios do not match the committed fixtures.\n",
				len(mismatches), len(scenarios))
			fmt.Fprint(stderr, "Re-run capture and read the diff: either the harness changed, or a fixture was edited by hand.\n")
			return exitFail
		}
		fmt.Fprintf(stdout, "verified %d scenarios against the committed fixtures\n", len(scenarios))
		return exitOK

	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n", args[1])
		fmt.Fprint(stderr, Usage)
		return exitUsage
	}
}
