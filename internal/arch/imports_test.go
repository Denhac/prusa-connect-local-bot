// Package arch holds the packages of this module to the boundaries in
// docs/design.md ("What each package may know"). It has no code, only this
// test, which reads each package's imports with go/build.
package arch

import (
	"go/build"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const self = "github.com/denhac/prusa-connect-local-bot/"

// The outside modules that only one or two packages may import, so a change
// in any of them lands in one place.
const (
	slackGo = "github.com/slack-go/slack"
	sdk     = "github.com/prusa3d/prusa-connect/sdk"
)

// allowed says which packages may import which outside module.
var allowed = map[string][]string{
	slackGo: {"internal/slack"},
	sdk:     {"internal/app", "internal/connect"},
}

// stdlibOnly are packages that may import nothing outside the standard
// library and this module's own packages named here.
var stdlibOnly = map[string][]string{
	"internal/domain":     {},
	"internal/secret":     {},
	"internal/settings":   {"internal/secret", "internal/verify"},
	"internal/routes":     {"internal/domain"},
	"internal/camera":     {"internal/domain"},
	"internal/store":      {"internal/domain"},
	"internal/auditlog":   {},
	"internal/serverhttp": {"internal/domain", "internal/secret"},
}

func TestImports(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}

	packages := 0

	err = filepath.WalkDir(filepath.Join(root, "internal"), func(dir string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil || !entry.IsDir() || entry.Name() == "testdata" {
			return walkErr
		}

		pkg, importErr := build.ImportDir(dir, 0)
		if importErr != nil {
			return nil //nolint:nilerr // a directory with no Go files, such as testdata
		}

		rel, _ := filepath.Rel(root, dir)
		rel = filepath.ToSlash(rel)
		packages++

		for _, imported := range pkg.Imports {
			check(t, rel, imported)
		}

		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if packages < 6 {
		t.Fatalf("found %d packages under internal, which means the walk went wrong", packages)
	}
}

func check(t *testing.T, pkg, imported string) {
	t.Helper()

	for module, who := range allowed {
		if (imported == module || strings.HasPrefix(imported, module+"/")) && !contains(who, pkg) {
			t.Errorf("%s imports %s, which only %v may import", pkg, imported, who)
		}
	}

	own, limited := stdlibOnly[pkg]
	if !limited {
		return
	}

	if strings.HasPrefix(imported, self) {
		if !contains(own, strings.TrimPrefix(imported, self)) {
			t.Errorf("%s imports %s, and it may import only %v of this module", pkg, imported, own)
		}

		return
	}

	if first, _, _ := strings.Cut(imported, "/"); strings.Contains(first, ".") {
		t.Errorf("%s imports %s, and it may import only the standard library", pkg, imported)
	}
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}

	return false
}
