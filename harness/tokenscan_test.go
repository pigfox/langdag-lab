package harness

import (
	"os"
	"path/filepath"
	"testing"
)

// THE SCAN OVER THIS REPOSITORY. Its output is copied into a website whose push
// gate rejects a short list of words; catching one here costs a test run, and
// catching one there costs a failed push in a different repository.
func TestTreeCarriesNothingTheConsumerGateRejects(t *testing.T) {
	root := filepath.Join("..")
	hits, err := ScanTree(root)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	for _, hit := range hits {
		t.Errorf("%s:%d would be rejected by the consumer's gate: %s", hit.Path, hit.Line, hit.Text)
	}
}

// THE SELF-TEST, and it is not optional. A scanner that had silently stopped
// scanning reports the same clean result as a clean tree, so the pass above
// means nothing on its own.
//
// It plants the word in a temporary tree rather than anywhere real, and it
// builds the word the same way the scanner does — writing it out would put the
// thing being tested for into the file doing the testing.
func TestScanTreeCatchesAPlantedWord(t *testing.T) {
	dir := t.TempDir()
	planted := "a line containing " + bannedRoot() + " in the middle"
	if err := os.WriteFile(filepath.Join(dir, "planted.txt"), []byte("clean\n"+planted+"\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}

	hits, err := ScanTree(dir)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if len(hits) != 1 {
		t.Fatalf("got %d hits, want 1: %+v", len(hits), hits)
	}
	if hits[0].Path != "planted.txt" || hits[0].Line != 2 {
		t.Errorf("hit located at %s:%d, want planted.txt:2", hits[0].Path, hits[0].Line)
	}
	if hits[0].Text != planted {
		t.Errorf("hit text is %q", hits[0].Text)
	}
}

// Matching is case-insensitive, so the capitalized and embedded spellings the
// consumer's gate also rejects are covered by the one entry.
func TestScanTreeIsCaseInsensitiveAndMatchesEmbeddedSpellings(t *testing.T) {
	dir := t.TempDir()
	root := bannedRoot()
	upper := string(root[0]-32) + root[1:]
	for name, content := range map[string]string{
		"upper.txt":    upper + "Rotation\n",
		"hyphen.txt":   "--" + root + "-url\n",
		"suffixed.txt": root + "ing\n",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("setup %s: %v", name, err)
		}
	}
	hits, err := ScanTree(dir)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if len(hits) != 3 {
		t.Errorf("got %d hits, want 3: %+v", len(hits), hits)
	}
}

// A clean tree must report nothing, or the scan would flag everything and the
// signal would be useless.
func TestScanTreeIsQuietOnACleanTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "fine.txt"), []byte("branch and continue\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	hits, err := ScanTree(dir)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("a clean tree produced hits: %+v", hits)
	}
}

// Revision history is not editable source and is not scanned.
func TestScanTreeSkipsTheRevisionDirectory(t *testing.T) {
	dir := t.TempDir()
	buried := filepath.Join(dir, ".git", "objects")
	if err := os.MkdirAll(buried, 0o755); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if err := os.WriteFile(filepath.Join(buried, "blob"), []byte(bannedRoot()+"\n"), 0o644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	hits, err := ScanTree(dir)
	if err != nil {
		t.Fatalf("ScanTree: %v", err)
	}
	if len(hits) != 0 {
		t.Errorf("history was scanned: %+v", hits)
	}
}

func TestScanTreeReportsAMissingRoot(t *testing.T) {
	if _, err := ScanTree(filepath.Join(t.TempDir(), "absent")); err == nil {
		t.Error("a missing root must be reported rather than read as clean")
	}
}
