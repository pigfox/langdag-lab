// Command langdag-lab builds langdag conversation trees against mock providers
// and captures them as deterministic fixtures.
//
// Everything it does lives in the harness package, so the whole of it is
// reachable from tests. This file is three lines because a func main cannot be
// covered by go test, and code that cannot be covered should be the smallest
// thing that still compiles.
package main

import (
	"os"

	"github.com/pigfox/langdag-lab/harness"
)

func main() {
	os.Exit(harness.Main(os.Args, os.Stdout, os.Stderr))
}
