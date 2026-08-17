package harness

import "os"

// symlinkTo is os.Symlink behind a name, so the one test that needs it reads
// plainly and its failure is a skip rather than a hard error on a filesystem
// that has no symlinks.
func symlinkTo(target, link string) error { return os.Symlink(target, link) }
