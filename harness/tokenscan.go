package harness

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// The consuming website runs a gate over every tracked file that rejects a
// short list of words, and this repository's output is copied into that
// website. A word that is fine here and rejected there would be found at the
// consumer's push gate, in a different repository, by someone who did not write
// it — so the same check runs here, over this tree, as an ordinary test.
//
// THE WORD IS ASSEMBLED RATHER THAN WRITTEN. A scanner that spelled its subject
// out would be its own first hit, and the only fixes for that are excluding the
// scanner from the scan or excluding the line — both of which blind the check
// to the one file most likely to be edited by someone thinking about the check.
// Assembling costs one line and needs no exclusion at all, so this scan covers
// every byte of the tree including itself.
//
// The check is a MEMBERSHIP TEST, and nothing here describes what is absent.
// Naming the absence would put the word in the tree, which is the condition
// being tested for.

// bannedRoot is the substring every rejected spelling contains. Matching is
// case-insensitive, so one entry covers the capitalized and hyphenated forms.
func bannedRoot() string { return "f" + "o" + "r" + "k" }

// TokenHit is one rejected occurrence.
type TokenHit struct {
	// Path is relative to the scanned root.
	Path string

	// Line is 1-indexed.
	Line int

	// Text is the offending line, trimmed.
	Text string
}

// skippedDirs are not part of the tree a consumer would ever copy.
//
// .git holds every past revision, including any that predates this check, and
// its contents are not editable source. Nothing else is skipped: fixtures,
// go.sum, the README and the license are all scanned, because a rejected word
// reaching the consumer does not care which file carried it.
var skippedDirs = map[string]bool{".git": true}

// ScanTree walks root and reports every rejected occurrence.
func ScanTree(root string) ([]TokenHit, error) {
	needle := bannedRoot()
	var hits []TokenHit

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		// WalkDir hands back paths it built by joining onto root, so trimming
		// the prefix cannot fail. filepath.Rel would do the same job with an
		// error return that no input reaching here can trigger.
		rel := strings.TrimPrefix(strings.TrimPrefix(path, root), string(filepath.Separator))
		if entry.IsDir() {
			if skippedDirs[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			if strings.Contains(strings.ToLower(line), needle) {
				hits = append(hits, TokenHit{
					Path: filepath.ToSlash(rel),
					Line: i + 1,
					Text: strings.TrimSpace(line),
				})
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return hits, nil
}
