package docs_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

var persianRootDocs = []string{
	"README.md",
	"STATUS.md",
	"PLAN.md",
	"TEST-RESULTS.md",
	"KNOWN-LIMITATIONS.md",
	"BLOCKERS.md",
	"novelty-matrix.md",
}

func TestPersianMarkdownIsRTLAndCodeBlocksAreLTR(t *testing.T) {
	root := repoRoot(t)
	var files []string
	for _, name := range persianRootDocs {
		files = append(files, filepath.Join(root, name))
	}
	err := filepath.WalkDir(filepath.Join(root, "docs", "fa"), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(strings.ToLower(d.Name()), ".md") {
			files = append(files, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range files {
		path := path
		t.Run(filepath.ToSlash(strings.TrimPrefix(path, root+string(filepath.Separator))), func(t *testing.T) {
			b, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			s := strings.TrimSpace(string(b))
			if !strings.HasPrefix(s, `<div dir="rtl" align="right" lang="fa">`) {
				t.Fatal("Persian Markdown must start with the canonical RTL wrapper")
			}
			if !strings.HasSuffix(s, "</div>") {
				t.Fatal("Persian Markdown must end by closing the RTL wrapper")
			}

			lines := strings.Split(s, "\n")
			inCode := false
			ltrDepth := 0
			for i, raw := range lines {
				line := strings.TrimSpace(raw)
				if strings.HasPrefix(line, `<div dir="ltr"`) {
					ltrDepth++
				}
				if strings.HasPrefix(line, "```") {
					if !inCode {
						if ltrDepth == 0 {
							t.Fatalf("line %d: fenced code block must be inside an LTR wrapper", i+1)
						}
						inCode = true
					} else {
						inCode = false
					}
				}
				if line == "</div>" && ltrDepth > 0 && !inCode {
					ltrDepth--
				}
			}
			if inCode {
				t.Fatal("unterminated fenced code block")
			}
		})
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repository root")
		}
		dir = parent
	}
}
