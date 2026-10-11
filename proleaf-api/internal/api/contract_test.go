package api_test

import (
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/proleaf/proleaf-api/internal/testsupport"
)

var (
	routePattern = regexp.MustCompile(`^(GET|POST|PUT|PATCH|DELETE) (/\S+)$`)
	specPath     = regexp.MustCompile(`^  (/\S+):$`)
	specMethod   = regexp.MustCompile(`^    (get|post|put|patch|delete):$`)
)

// The OpenAPI document is written by hand, so this is what keeps it honest:
// every route a package registers is documented, and every documented route
// exists. It reads the route patterns out of the source the same way the
// architecture tests read SQL.
func TestOpenAPIMatchesRegisteredRoutes(t *testing.T) {
	registered := map[string]bool{}
	for _, pkg := range []string{"auth", "teacher", "parent", "admin"} {
		for _, literals := range packageStringLiterals(t, filepath.Join(moduleRoot, pkg)) {
			for _, lit := range literals {
				if m := routePattern.FindStringSubmatch(lit); m != nil {
					registered[m[1]+" "+m[2]] = true
				}
			}
		}
	}
	if len(registered) == 0 {
		t.Fatal("no routes found in the source; the check is not looking at anything")
	}

	spec, err := os.ReadFile(filepath.Join(moduleRoot, "..", "docs", "openapi.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	documented := map[string]bool{}
	var path string
	for _, line := range strings.Split(strings.ReplaceAll(string(spec), "\r\n", "\n"), "\n") {
		if m := specPath.FindStringSubmatch(line); m != nil {
			path = m[1]
		} else if m := specMethod.FindStringSubmatch(line); m != nil {
			documented[strings.ToUpper(m[1])+" "+path] = true
		}
	}

	var problems []string
	for r := range registered {
		if !documented[r] {
			problems = append(problems, "registered but not in docs/openapi.yaml: "+r)
		}
	}
	for r := range documented {
		if !registered[r] {
			problems = append(problems, "in docs/openapi.yaml but not registered: "+r)
		}
	}
	sort.Strings(problems)
	for _, p := range problems {
		t.Error(p)
	}
}

// Every error, including the ones the router itself produces, uses the one
// envelope: {"error":{"code","message"},"request_id"}.
func TestErrorsUseTheEnvelope(t *testing.T) {
	f := newFixture(t)

	cases := []struct {
		name, method, path, token string
		raw                       bool
		wantStatus                int
		wantCode                  string
	}{
		{"unknown route under /v1", http.MethodGet, "/nope", "", false, 404, "not_found"},
		{"unknown route at the root", http.MethodGet, "/nope", "", true, 404, "not_found"},
		{"unversioned path is gone", http.MethodGet, "/teacher/classes", f.teacherToken, true, 404, "not_found"},
		{"unknown route behind a role gate", http.MethodGet, "/teacher/nope", f.teacherToken, false, 404, "not_found"},
		{"wrong method", http.MethodDelete, "/teacher/classes", f.teacherToken, false, 405, "method_not_allowed"},
		{"wrong method on a public route", http.MethodGet, "/auth/otp/request", "", false, 405, "method_not_allowed"},
		{"no token", http.MethodGet, "/teacher/classes", "", false, 401, "unauthorized"},
		{"wrong role", http.MethodGet, "/admin/audit", f.teacherToken, false, 403, "forbidden"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			do := f.h.Do
			if c.raw {
				do = f.h.DoRaw
			}
			res := do(c.method, c.path, c.token, nil)
			if res.Status != c.wantStatus {
				t.Fatalf("want %d, got %d: %s", c.wantStatus, res.Status, res.String())
			}
			var body struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
				RequestID string `json:"request_id"`
			}
			if err := res.JSON(&body); err != nil {
				t.Fatalf("body is not the JSON envelope: %v: %s", err, res.String())
			}
			if body.Error.Code != c.wantCode || body.Error.Message == "" || body.RequestID == "" {
				t.Fatalf("want code %q with a message and request id, got %s", c.wantCode, res.String())
			}
		})
	}
}

func TestOpenAPIDocumentIsServed(t *testing.T) {
	h := testsupport.NewHarness()
	res := h.Do(http.MethodGet, "/openapi.yaml", "", nil)
	if res.Status != http.StatusOK || !strings.HasPrefix(res.String(), "openapi: 3.") {
		t.Fatalf("want the spec, got %d: %.80s", res.Status, res.String())
	}
	if res := h.DoRaw(http.MethodGet, "/healthz", "", nil); res.Status != http.StatusOK {
		t.Fatalf("healthz must stay unversioned: got %d", res.Status)
	}
}
