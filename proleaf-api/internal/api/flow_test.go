package api_test

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/audit"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/enroll"
	"github.com/proleaf/proleaf-api/internal/stats"
	"github.com/proleaf/proleaf-api/internal/testsupport"
)

var (
	uuidPattern = regexp.MustCompile(`[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}`)
	codePattern = regexp.MustCompile(`\b(\d{6})\b`)
)

// TestSignInFlow covers the definition-of-done item for OTP: request, verify,
// and a usable token, against the stubbed SMS adapter.
func TestSignInFlow(t *testing.T) {
	h := testsupport.NewHarness()
	staff := h.W.AddAccount(auth.RoleTeacher, "+8613900000001")

	if res := h.Do(http.MethodPost, "/auth/otp/request", "", map[string]string{"phone": "13900000001"}); res.Status != http.StatusAccepted {
		t.Fatalf("otp request: want 202, got %d: %s", res.Status, res.String())
	}

	msg, ok := h.SMS.Last("+8613900000001")
	if !ok {
		t.Fatal("no SMS was sent")
	}
	code := codePattern.FindString(msg.Message)
	if code == "" {
		t.Fatalf("no code in the message: %q", msg.Message)
	}

	// A wrong code is refused, and says nothing about why.
	res := h.Do(http.MethodPost, "/auth/otp/verify", "", map[string]string{
		"phone": "13900000001", "code": "000000",
	})
	if res.Status != http.StatusUnauthorized {
		t.Fatalf("wrong code: want 401, got %d", res.Status)
	}
	if !strings.Contains(res.String(), "otp_invalid") {
		t.Fatalf("want a single generic failure code, got %s", res.String())
	}

	res = h.Do(http.MethodPost, "/auth/otp/verify", "", map[string]string{
		"phone": "13900000001", "code": code,
	})
	if res.Status != http.StatusOK {
		t.Fatalf("verify: want 200, got %d: %s", res.Status, res.String())
	}
	var issued struct {
		Token string    `json:"token"`
		Role  auth.Role `json:"role"`
	}
	if err := res.JSON(&issued); err != nil {
		t.Fatal(err)
	}
	// The role comes from the provisioned account, not from the sign-in.
	if issued.Role != auth.RoleTeacher {
		t.Fatalf("want the teacher role, got %q", issued.Role)
	}
	if res := h.Do(http.MethodGet, "/teacher/classes", issued.Token, nil); res.Status != http.StatusOK {
		t.Fatalf("issued token was rejected: %d %s", res.Status, res.String())
	}

	// The code is single use.
	if res := h.Do(http.MethodPost, "/auth/otp/verify", "", map[string]string{
		"phone": "13900000001", "code": code,
	}); res.Status != http.StatusUnauthorized {
		t.Fatalf("code replay: want 401, got %d", res.Status)
	}

	_ = staff
}

// An unknown phone becomes a parent, never staff.
func TestFirstSignInCreatesParentAccount(t *testing.T) {
	h := testsupport.NewHarness()

	h.Do(http.MethodPost, "/auth/otp/request", "", map[string]string{"phone": "13900000009"})
	msg, _ := h.SMS.Last("+8613900000009")
	code := codePattern.FindString(msg.Message)

	res := h.Do(http.MethodPost, "/auth/otp/verify", "", map[string]string{
		"phone": "13900000009", "code": code,
	})
	var issued struct {
		Token string    `json:"token"`
		Role  auth.Role `json:"role"`
	}
	if err := res.JSON(&issued); err != nil {
		t.Fatal(err)
	}
	if issued.Role != auth.RoleParent {
		t.Fatalf("a new phone must become a parent, got %q", issued.Role)
	}
	if res := h.Do(http.MethodGet, "/teacher/classes", issued.Token, nil); res.Status != http.StatusForbidden {
		t.Fatalf("a self-registered account reached a teacher route: %d", res.Status)
	}
}

// TestTeacherBulkEntryToParentComparison is the end-to-end path the skeleton
// exists to prove: admin issues a code, a teacher records a day of scores in
// one call, the stats job runs, a parent binds and reads a comparison.
func TestTeacherBulkEntryToParentComparison(t *testing.T) {
	h := testsupport.NewHarness()
	w := h.W

	adminAcct := w.AddAccount(auth.RoleAdmin, "+8613700000001")
	teacherAcct := w.AddAccount(auth.RoleTeacher, "+8613700000002")
	parentAcct := w.AddAccount(auth.RoleParent, "+8613700000003")
	adminToken, teacherToken, parentToken := h.Token(adminAcct), h.Token(teacherAcct), h.Token(parentAcct)

	class := w.AddClass("CAT3 Green", 3)
	w.Assign(class.ID, teacherAcct.ID)

	dob := time.Date(2017, 3, 2, 0, 0, 0, 0, time.UTC)
	names := []string{"Ana", "Bai", "Cui", "Du", "Er", "Fan"}
	students := make([]uuid.UUID, 0, len(names))
	for _, n := range names {
		students = append(students, w.AddStudent(class.ID, n, 3, dob).ID)
	}

	// Admin issues an enrollment code for the first student.
	res := h.Do(http.MethodPost, "/admin/enrollment-codes", adminToken, map[string]any{
		"student_id": students[0],
	})
	if res.Status != http.StatusCreated {
		t.Fatalf("issue code: want 201, got %d: %s", res.Status, res.String())
	}
	var issued struct {
		Code string `json:"code"`
	}
	if err := res.JSON(&issued); err != nil {
		t.Fatal(err)
	}
	if issued.Code == "" {
		t.Fatal("no code returned")
	}
	// The plaintext is not what was stored.
	normalized, err := enroll.Normalize(issued.Code)
	if err != nil {
		t.Fatalf("issued code does not normalize: %v", err)
	}
	if _, stored := w.Codes[normalized]; stored {
		t.Fatal("the plaintext code was stored")
	}
	if _, stored := w.Codes[enroll.Hash(testsupport.CodePepper, normalized)]; !stored {
		t.Fatal("the hashed code was not stored")
	}

	// A day of scores in one call.
	session := stats.Month(time.Now()).AddDate(0, 0, 2)
	entries := make([]map[string]any, 0, len(students))
	for i, id := range students {
		entries = append(entries, map[string]any{
			"student_id":    id,
			"score":         60 + float64(i)*6,
			"participation": 3,
		})
	}
	res = h.Do(http.MethodPost, "/teacher/assessments/bulk", teacherToken, map[string]any{
		"session_date": session.Format("2006-01-02"),
		"entries":      entries,
	})
	if res.Status != http.StatusCreated {
		t.Fatalf("bulk entry: want 201, got %d: %s", res.Status, res.String())
	}

	// The stats job, run in-process against the same fixture.
	w.ComputeStats(class.ID, session, 5)

	// The parent binds with the code and consents.
	res = h.Do(http.MethodPost, "/parent/bind", parentToken, map[string]any{
		"enrollment_code": issued.Code,
		"policy_version":  testsupport.PolicyVersion,
		"accepted_scopes": []string{"progress", "comparison"},
	})
	if res.Status != http.StatusCreated {
		t.Fatalf("bind: want 201, got %d: %s", res.Status, res.String())
	}

	// A second bind with the same code fails: single use.
	if res := h.Do(http.MethodPost, "/parent/bind", parentToken, map[string]any{
		"enrollment_code": issued.Code,
		"policy_version":  testsupport.PolicyVersion,
		"accepted_scopes": []string{"progress", "comparison"},
	}); res.Status != http.StatusNotFound {
		t.Fatalf("code reuse: want 404, got %d: %s", res.Status, res.String())
	}

	// And the comparison reads.
	res = h.Do(http.MethodGet,
		"/parent/children/"+students[0].String()+"/comparison?period="+session.Format("2006-01"),
		parentToken, nil)
	if res.Status != http.StatusOK {
		t.Fatalf("comparison: want 200, got %d: %s", res.Status, res.String())
	}
	var comparison struct {
		Period   string   `json:"period"`
		OwnScore *float64 `json:"own_score"`
		Class    *struct {
			Mean         *float64 `json:"mean_score"`
			StudentCount int      `json:"student_count"`
		} `json:"class"`
	}
	if err := res.JSON(&comparison); err != nil {
		t.Fatal(err)
	}
	if comparison.OwnScore == nil || *comparison.OwnScore != 60 {
		t.Fatalf("want an own score of 60, got %v", comparison.OwnScore)
	}
	if comparison.Class == nil {
		t.Fatalf("class aggregates missing: %s", res.String())
	}
	if comparison.Class.StudentCount != 6 {
		t.Fatalf("want 6 students in the cohort, got %d", comparison.Class.StudentCount)
	}
	if uuidPattern.MatchString(res.String()) {
		t.Fatalf("comparison leaked an identifier: %s", res.String())
	}
}

// A class below the suppression threshold publishes no aggregates: the mean of
// three scores is close enough to an individual one.
func TestSmallCohortIsSuppressed(t *testing.T) {
	h := testsupport.NewHarness()
	w := h.W

	teacherAcct := w.AddAccount(auth.RoleTeacher, "+8613600000001")
	parentAcct := w.AddAccount(auth.RoleParent, "+8613600000002")
	class := w.AddClass("CAT5 Tiny", 5)
	w.Assign(class.ID, teacherAcct.ID)

	dob := time.Date(2015, 9, 9, 0, 0, 0, 0, time.UTC)
	session := stats.Month(time.Now()).AddDate(0, 0, 4)
	var first uuid.UUID
	for i, n := range []string{"Hu", "Ito", "Jin"} {
		st := w.AddStudent(class.ID, n, 5, dob)
		if i == 0 {
			first = st.ID
		}
		w.AddAssessment(st.ID, teacherAcct.ID, session, 50+float64(i)*10)
	}
	w.ComputeStats(class.ID, session, 5)

	w.Bind(parentAcct.ID, first)
	w.GrantConsent(parentAcct.ID, first, testsupport.PolicyVersion)

	res := h.Do(http.MethodGet,
		"/parent/children/"+first.String()+"/comparison?period="+session.Format("2006-01"),
		h.Token(parentAcct), nil)
	if res.Status != http.StatusOK {
		t.Fatalf("want 200, got %d: %s", res.Status, res.String())
	}
	var body struct {
		Class       *struct{} `json:"class"`
		Unavailable string    `json:"unavailable"`
	}
	if err := res.JSON(&body); err != nil {
		t.Fatal(err)
	}
	if body.Class != nil {
		t.Fatalf("a three-student cohort published aggregates: %s", res.String())
	}
	if body.Unavailable != "cohort_too_small" {
		t.Fatalf("want cohort_too_small, got %q", body.Unavailable)
	}
}

// Every route behind a role gate writes at least one audit row, and the admin
// surface writes to its own stream.
func TestEveryStudentDataRouteIsAudited(t *testing.T) {
	f := newFixture(t)
	child := f.ownChild.ID.String()

	cases := []struct {
		name, method, path, token string
		body                      any
		wantStream                string
	}{
		{"teacher classes", http.MethodGet, "/teacher/classes", f.teacherToken, nil, audit.StreamApp},
		{"teacher class students", http.MethodGet, "/teacher/classes/" + f.classID.String() + "/students", f.teacherToken, nil, audit.StreamApp},
		{"teacher history", http.MethodGet, "/teacher/students/" + child + "/history", f.teacherToken, nil, audit.StreamApp},
		{"teacher record", http.MethodPost, "/teacher/assessments", f.teacherToken,
			map[string]any{"student_id": f.ownChild.ID, "score": 70.0}, audit.StreamApp},
		{"parent children", http.MethodGet, "/parent/children", f.parentToken, nil, audit.StreamApp},
		{"parent progress", http.MethodGet, "/parent/children/" + child + "/progress", f.parentToken, nil, audit.StreamApp},
		{"parent comparison", http.MethodGet, "/parent/children/" + child + "/comparison", f.parentToken, nil, audit.StreamApp},
		{"parent consent", http.MethodGet, "/parent/children/" + child + "/consent", f.parentToken, nil, audit.StreamApp},
		{"admin audit", http.MethodGet, "/admin/audit", f.adminToken, nil, audit.StreamAdmin},
		{"admin enrollment code", http.MethodPost, "/admin/enrollment-codes", f.adminToken,
			map[string]any{"student_id": f.ownChild.ID}, audit.StreamAdmin},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			before := len(f.h.W.AuditEntries())
			res := f.h.Do(c.method, c.path, c.token, c.body)
			if res.Status < 200 || res.Status >= 300 {
				t.Fatalf("precondition: want 2xx, got %d: %s", res.Status, res.String())
			}
			entries := f.h.W.AuditEntries()
			if len(entries) <= before {
				t.Fatal("handler returned 2xx without writing an audit row")
			}
			for _, e := range entries[before:] {
				if e.Stream != c.wantStream {
					t.Errorf("want stream %q, got %q", c.wantStream, e.Stream)
				}
				if e.ActorAccount == uuid.Nil {
					t.Error("audit row has no actor")
				}
				if e.RequestID == "" {
					t.Error("audit row has no request id")
				}
				if e.Action == "" {
					t.Error("audit row has no action")
				}
			}
		})
	}
}

// A refused access attempt is not silently unlogged either: the identity that
// made it is known, so the row is worth having. (A 404 writes nothing today
// because the handler never reaches Record; this test pins the behaviour so a
// future change to it is deliberate.)
func TestDeniedRequestWritesNoStudentRow(t *testing.T) {
	f := newFixture(t)
	before := len(f.h.W.AuditEntries())

	f.h.Do(http.MethodGet, "/parent/children/"+f.otherChild.ID.String()+"/progress", f.parentToken, nil)

	for _, e := range f.h.W.AuditEntries()[before:] {
		if e.SubjectStudent != nil && *e.SubjectStudent == f.otherChild.ID {
			t.Fatal("a denied request recorded a view against the student it was denied")
		}
	}
}
