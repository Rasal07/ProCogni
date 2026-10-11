package api_test

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/consent"
	"github.com/proleaf/proleaf-api/internal/stats"
	"github.com/proleaf/proleaf-api/internal/student"
	"github.com/proleaf/proleaf-api/internal/testsupport"
)

// The four negative tests §4 of the skeleton guide asks for, plus the
// neighbours that would otherwise be the next gap: no token at all, a teacher
// on a parent route, and a bound student whose consent has been withdrawn.

type fixture struct {
	h *testsupport.Harness

	teacher auth.Account
	parent  auth.Account
	admin   auth.Account
	// stranger is a second parent, bound to nobody.
	stranger auth.Account

	classID    uuid.UUID
	ownChild   student.Student
	otherChild student.Student

	teacherToken  string
	parentToken   string
	adminToken    string
	strangerToken string
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	h := testsupport.NewHarness()
	w := h.W

	f := &fixture{h: h}
	f.teacher = w.AddAccount(auth.RoleTeacher, "+8613800000001")
	f.parent = w.AddAccount(auth.RoleParent, "+8613800000002")
	f.admin = w.AddAccount(auth.RoleAdmin, "+8613800000003")
	f.stranger = w.AddAccount(auth.RoleParent, "+8613800000004")

	class := w.AddClass("CAT4 Blue", 4)
	f.classID = class.ID
	w.Assign(class.ID, f.teacher.ID)

	dob := time.Date(2016, 5, 1, 0, 0, 0, 0, time.UTC)
	f.ownChild = w.AddStudent(class.ID, "Aiden", 4, dob)
	f.otherChild = w.AddStudent(class.ID, "Bo", 4, dob)
	// Four more, so the cohort clears the suppression threshold.
	for _, name := range []string{"Chen", "Dai", "Eun", "Fei"} {
		w.AddStudent(class.ID, name, 4, dob)
	}

	w.Bind(f.parent.ID, f.ownChild.ID)
	w.GrantConsent(f.parent.ID, f.ownChild.ID, testsupport.PolicyVersion)

	period := stats.Month(time.Now())
	w.AddAssessment(f.ownChild.ID, f.teacher.ID, period.AddDate(0, 0, 3), 72)
	w.AddAssessment(f.otherChild.ID, f.teacher.ID, period.AddDate(0, 0, 3), 91)

	mean, median, top := 78.5, 77.0, 91.0
	w.SetStats(stats.ClassStat{
		ClassID: class.ID, Period: period,
		Mean: &mean, Median: &median, Top: &top,
		StudentCount: 6, ComputedAt: time.Now().UTC(),
	})

	f.teacherToken = h.Token(f.teacher)
	f.parentToken = h.Token(f.parent)
	f.adminToken = h.Token(f.admin)
	f.strangerToken = h.Token(f.stranger)
	return f
}

// 1. A parent token on a teacher route is 403.
func TestParentTokenOnTeacherRouteIsForbidden(t *testing.T) {
	f := newFixture(t)

	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/teacher/classes"},
		{http.MethodGet, "/teacher/classes/" + f.classID.String() + "/students"},
		{http.MethodGet, "/teacher/students/" + f.ownChild.ID.String() + "/history"},
		{http.MethodPost, "/teacher/assessments/bulk"},
	} {
		t.Run(route.path, func(t *testing.T) {
			res := f.h.Do(route.method, route.path, f.parentToken, map[string]any{})
			if res.Status != http.StatusForbidden {
				t.Fatalf("want 403, got %d: %s", res.Status, res.String())
			}
		})
	}
}

// The mirror image: staff must not reach the parent surface either.
func TestTeacherTokenOnParentRouteIsForbidden(t *testing.T) {
	f := newFixture(t)

	res := f.h.Do(http.MethodGet, "/parent/children", f.teacherToken, nil)
	if res.Status != http.StatusForbidden {
		t.Fatalf("want 403, got %d: %s", res.Status, res.String())
	}
	res = f.h.Do(http.MethodGet, "/admin/audit", f.teacherToken, nil)
	if res.Status != http.StatusForbidden {
		t.Fatalf("admin route with a teacher token: want 403, got %d", res.Status)
	}
}

func TestNoTokenIsUnauthorized(t *testing.T) {
	f := newFixture(t)

	for _, path := range []string{"/teacher/classes", "/parent/children", "/admin/audit"} {
		res := f.h.Do(http.MethodGet, path, "", nil)
		if res.Status != http.StatusUnauthorized {
			t.Fatalf("%s: want 401, got %d", path, res.Status)
		}
	}
}

// 2. A parent asking for a student they are not bound to is 404, not 403.
//
// The distinction is the whole point: a 403 would confirm that the id names a
// real student, which is a disclosure on its own.
func TestUnboundStudentIsNotFound(t *testing.T) {
	f := newFixture(t)

	cases := map[string]string{
		"another family's child":       f.otherChild.ID.String(),
		"an id that exists for nobody": uuid.NewString(),
		"a malformed id":               "not-a-uuid",
	}
	for name, id := range cases {
		t.Run(name, func(t *testing.T) {
			for _, suffix := range []string{"/progress", "/comparison", "/consent"} {
				res := f.h.Do(http.MethodGet, "/parent/children/"+id+suffix, f.parentToken, nil)
				if res.Status != http.StatusNotFound {
					t.Fatalf("%s: want 404, got %d: %s", suffix, res.Status, res.String())
				}
			}
		})
	}
}

// A parent bound to nobody sees an empty list, not an error and not somebody
// else's children.
func TestStrangerSeesNoChildren(t *testing.T) {
	f := newFixture(t)

	res := f.h.Do(http.MethodGet, "/parent/children", f.strangerToken, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", res.Status, res.String())
	}
	var body struct {
		Children []student.Student `json:"children"`
	}
	if err := res.JSON(&body); err != nil {
		t.Fatal(err)
	}
	if len(body.Children) != 0 {
		t.Fatalf("want no children, got %d", len(body.Children))
	}
	if strings.Contains(res.String(), f.ownChild.ID.String()) {
		t.Fatal("another family's student id appeared in the response")
	}
}

// 3. A revoked binding is 404, on every route, immediately.
func TestRevokedBindingIsNotFound(t *testing.T) {
	f := newFixture(t)

	// Sanity: it works while the binding is live.
	if res := f.h.Do(http.MethodGet, "/parent/children/"+f.ownChild.ID.String()+"/progress", f.parentToken, nil); res.Status != http.StatusOK {
		t.Fatalf("precondition failed: want 200, got %d: %s", res.Status, res.String())
	}

	f.h.W.Revoke(f.parent.ID, f.ownChild.ID)

	for _, suffix := range []string{"/progress", "/comparison", "/consent"} {
		res := f.h.Do(http.MethodGet, "/parent/children/"+f.ownChild.ID.String()+suffix, f.parentToken, nil)
		if res.Status != http.StatusNotFound {
			t.Fatalf("%s after revocation: want 404, got %d: %s", suffix, res.Status, res.String())
		}
	}
	res := f.h.Do(http.MethodGet, "/parent/children", f.parentToken, nil)
	if strings.Contains(res.String(), f.ownChild.ID.String()) {
		t.Fatal("revoked child still listed in /parent/children")
	}
}

// 4. The comparison response carries no student identifiers.
func TestComparisonLeaksNoIdentifiers(t *testing.T) {
	f := newFixture(t)

	res := f.h.Do(http.MethodGet, "/parent/children/"+f.ownChild.ID.String()+"/comparison", f.parentToken, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", res.Status, res.String())
	}
	body := res.String()

	// Nothing that names anyone, including the requesting parent's own child:
	// the client already knows which child it asked about, and echoing the id
	// back is the seam through which a top_student_id would later arrive.
	forbidden := map[string]string{
		"own student id":   f.ownChild.ID.String(),
		"other student id": f.otherChild.ID.String(),
		"own student name": f.ownChild.DisplayName,
		"class id":         f.classID.String(),
		"teacher id":       f.teacher.ID.String(),
	}
	for what, needle := range forbidden {
		if strings.Contains(body, needle) {
			t.Errorf("comparison response contains the %s: %s", what, body)
		}
	}

	// And no UUID-shaped value at all, whatever it might be called.
	if uuidPattern.MatchString(body) {
		t.Errorf("comparison response contains a UUID: %s", body)
	}

	// The keys are a closed set. A new one has to be added here deliberately.
	var decoded map[string]json.RawMessage
	if err := res.JSON(&decoded); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{
		"period": true, "own_score": true, "session_count": true,
		"class": true, "unavailable": true,
	}
	for key := range decoded {
		if !allowed[key] {
			t.Errorf("unexpected key %q in the comparison response", key)
		}
	}
}

// Consent is a separate gate from the binding, and withdrawing it takes effect
// on the next request.
func TestWithdrawnConsentBlocksData(t *testing.T) {
	f := newFixture(t)
	path := "/parent/children/" + f.ownChild.ID.String()

	f.h.W.WithdrawConsent(f.parent.ID, f.ownChild.ID, consent.ScopeComparison, testsupport.PolicyVersion)

	res := f.h.Do(http.MethodGet, path+"/comparison", f.parentToken, nil)
	if res.Status != http.StatusForbidden {
		t.Fatalf("want 403 after withdrawal, got %d: %s", res.Status, res.String())
	}
	if !strings.Contains(res.String(), "consent_required") {
		t.Fatalf("want a consent_required code, got %s", res.String())
	}

	// The other scope is untouched: withdrawal is per scope.
	if res := f.h.Do(http.MethodGet, path+"/progress", f.parentToken, nil); res.Status != http.StatusOK {
		t.Fatalf("progress should still be readable: got %d: %s", res.Status, res.String())
	}
}

// A parent bound with no consent recorded at all is refused, not defaulted to
// permitted.
func TestAbsentConsentBlocksData(t *testing.T) {
	f := newFixture(t)
	f.h.W.Bind(f.stranger.ID, f.otherChild.ID) // bound, never consented

	res := f.h.Do(http.MethodGet, "/parent/children/"+f.otherChild.ID.String()+"/progress", f.strangerToken, nil)
	if res.Status != http.StatusForbidden {
		t.Fatalf("want 403 with no consent on record, got %d: %s", res.Status, res.String())
	}
}

// A consent granted under a superseded policy version does not count.
func TestStaleConsentBlocksData(t *testing.T) {
	f := newFixture(t)
	f.h.W.Bind(f.stranger.ID, f.otherChild.ID)
	f.h.W.GrantConsent(f.stranger.ID, f.otherChild.ID, "an-older-policy")

	res := f.h.Do(http.MethodGet, "/parent/children/"+f.otherChild.ID.String()+"/progress", f.strangerToken, nil)
	if res.Status != http.StatusForbidden {
		t.Fatalf("want 403 for a superseded policy version, got %d: %s", res.Status, res.String())
	}
}

// A teacher cannot record against a class they are not assigned to.
func TestTeacherCannotRecordForAnotherClass(t *testing.T) {
	f := newFixture(t)
	otherClass := f.h.W.AddClass("CAT5 Red", 5)
	outsider := f.h.W.AddStudent(otherClass.ID, "Gao", 5, time.Date(2015, 1, 1, 0, 0, 0, 0, time.UTC))

	score := 80.0
	res := f.h.Do(http.MethodPost, "/teacher/assessments", f.teacherToken, map[string]any{
		"student_id": outsider.ID,
		"score":      score,
	})
	if res.Status != http.StatusNotFound {
		t.Fatalf("want 404, got %d: %s", res.Status, res.String())
	}

	// And the history route is equally closed.
	if res := f.h.Do(http.MethodGet, "/teacher/students/"+outsider.ID.String()+"/history", f.teacherToken, nil); res.Status != http.StatusNotFound {
		t.Fatalf("history: want 404, got %d", res.Status)
	}
}

// A parent may not see the teacher's free-text note, even on their own child.
func TestProgressOmitsTeacherNotes(t *testing.T) {
	f := newFixture(t)

	res := f.h.Do(http.MethodGet, "/parent/children/"+f.ownChild.ID.String()+"/progress", f.parentToken, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", res.Status, res.String())
	}
	if strings.Contains(res.String(), "teacher-only note") {
		t.Fatalf("parent progress leaked the teacher note: %s", res.String())
	}
	if strings.Contains(res.String(), f.teacher.ID.String()) {
		t.Fatalf("parent progress leaked the recording teacher's id: %s", res.String())
	}
}
