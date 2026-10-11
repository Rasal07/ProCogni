package api_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// These tests read the source rather than run it. They encode two rules from
// the skeleton guide that are otherwise only conventions, and conventions are
// what erode at month six.

const moduleRoot = ".."

// §1: "teacher/ and parent/ never import each other. Shared logic goes in
// student/ or stats/."
func TestTeacherAndParentDoNotImportEachOther(t *testing.T) {
	forbidden := map[string][]string{
		"teacher": {"internal/parent"},
		"parent":  {"internal/teacher"},
		// admin provisions; it has no business reading either surface's logic.
		"admin": {"internal/parent", "internal/teacher"},
	}
	for pkg, banned := range forbidden {
		for _, imp := range packageImports(t, filepath.Join(moduleRoot, pkg)) {
			for _, b := range banned {
				if strings.HasSuffix(imp, b) {
					t.Errorf("%s imports %s; shared logic belongs in student/ or stats/", pkg, imp)
				}
			}
		}
	}
}

// The intelligence provider must stay unreferenced: §9 requires that it
// "compiles with a no-op impl and is called nowhere". cmd/api holds a single
// compile-time assertion, which is the one allowed mention.
func TestIntelligenceIsCalledNowhere(t *testing.T) {
	for _, pkg := range []string{"parent", "teacher", "admin", "stats", "student", "consent", "auth", "api"} {
		for _, imp := range packageImports(t, filepath.Join(moduleRoot, pkg)) {
			if strings.HasSuffix(imp, "internal/intelligence") {
				t.Errorf("%s imports internal/intelligence; the skeleton calls it from nowhere", pkg)
			}
		}
	}
}

var (
	// A SQL reference to a table we care about, in any clause that reads it.
	sensitiveTable = regexp.MustCompile(`(?is)\b(from|join|into|update)\s+(students|assessments)\b`)
)

// §4: "verify this by grepping the parent/ package for references to students
// and assessments tables and confirming every one is scoped by a resolved
// binding." This is that grep, as a test.
func TestParentQueriesAreBindingScoped(t *testing.T) {
	assertScoped(t, filepath.Join(moduleRoot, "parent"), "guardian_bindings")
}

// The same discipline on the staff side: a teacher reaches a student only
// through a class assignment.
func TestTeacherQueriesAreAssignmentScoped(t *testing.T) {
	assertScoped(t, filepath.Join(moduleRoot, "teacher"), "class_teachers")
}

func assertScoped(t *testing.T, dir, scopeTable string) {
	t.Helper()

	checked := 0
	for file, literals := range packageStringLiterals(t, dir) {
		for _, lit := range literals {
			if !sensitiveTable.MatchString(lit) {
				continue
			}
			checked++
			if !strings.Contains(lit, scopeTable) {
				t.Errorf("%s: a query touching students/assessments is not scoped by %s:\n%s",
					filepath.Base(file), scopeTable, lit)
			}
		}
	}
	if checked == 0 {
		t.Fatalf("no queries found in %s; the check is not looking at anything", dir)
	}
}

func packageImports(t *testing.T, dir string) []string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, parser.ImportsOnly)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	var out []string
	for _, pkg := range pkgs {
		for _, file := range pkg.Files {
			for _, imp := range file.Imports {
				path, err := strconv.Unquote(imp.Path.Value)
				if err != nil {
					continue
				}
				out = append(out, path)
			}
		}
	}
	return out
}

// packageStringLiterals returns every string literal per file, which for these
// packages means every SQL fragment.
func packageStringLiterals(t *testing.T, dir string) map[string][]string {
	t.Helper()
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", dir, err)
	}
	out := map[string][]string{}
	for _, pkg := range pkgs {
		for name, file := range pkg.Files {
			if strings.HasSuffix(name, "_test.go") {
				continue
			}
			ast.Inspect(file, func(n ast.Node) bool {
				lit, ok := n.(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(lit.Value)
				if err != nil {
					return true
				}
				out[name] = append(out[name], value)
				return true
			})
		}
	}
	return out
}
