package api_test

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/testsupport"
)

// decode fails the test unless the response has the wanted status, then
// decodes the body.
func decode[T any](t *testing.T, res testsupport.Response, want int) T {
	t.Helper()
	var out T
	if res.Status != want {
		t.Fatalf("want %d, got %d: %s", want, res.Status, res.String())
	}
	if len(res.Body) > 0 {
		if err := res.JSON(&out); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

type obj = map[string]any

// School -> class -> teacher -> student, then the edits an office actually
// makes during a year: rename, move class, archive, restore.
func TestAdminManagesSchoolClassAndStudent(t *testing.T) {
	f := newFixture(t)
	do := func(method, path string, body any) testsupport.Response {
		return f.h.Do(method, path, f.adminToken, body)
	}

	school := decode[obj](t, do("POST", "/admin/schools", obj{"name": "  浦东实验小学 "}), 201)
	if school["name"] != "浦东实验小学" {
		t.Fatalf("name was not trimmed: %q", school["name"])
	}
	decode[obj](t, do("POST", "/admin/schools", obj{"name": " "}), 400)
	decode[obj](t, do("PATCH", "/admin/schools/"+school["id"].(string), obj{"name": "浦东实验小学（东校区）"}), 200)
	decode[obj](t, do("PATCH", "/admin/schools/"+uuid.NewString(), obj{"name": "x"}), 404)

	class := decode[obj](t, do("POST", "/admin/classes", obj{
		"name": "CAT3 Green", "cat_stage": 3, "academic_year": "2026", "school_id": school["id"],
	}), 201)
	classID := class["id"].(string)
	decode[obj](t, do("POST", "/admin/classes", obj{
		"name": "Orphan", "cat_stage": 3, "academic_year": "2026", "school_id": uuid.NewString(),
	}), 404)

	listed := decode[obj](t, do("GET", "/admin/classes?school_id="+school["id"].(string), nil), 200)
	if n := len(listed["classes"].([]any)); n != 1 {
		t.Fatalf("want 1 class in the school, got %d", n)
	}
	renamed := decode[obj](t, do("PATCH", "/admin/classes/"+classID, obj{"name": "CAT3 Jade"}), 200)
	if renamed["name"] != "CAT3 Jade" || renamed["academic_year"] != "2026" {
		t.Fatalf("patch must change only what it names: %v", renamed)
	}

	// Teachers on and off the class.
	teachers := "/admin/classes/" + classID + "/teachers"
	decode[obj](t, do("POST", teachers, obj{"account_id": f.teacher.ID}), 201)
	decode[obj](t, do("POST", teachers, obj{"account_id": f.parent.ID}), 400) // not a teacher
	decode[obj](t, do("POST", "/admin/classes/"+uuid.NewString()+"/teachers", obj{"account_id": f.teacher.ID}), 404)
	detail := decode[obj](t, do("GET", "/admin/classes/"+classID, nil), 200)
	if n := len(detail["teachers"].([]any)); n != 1 {
		t.Fatalf("want 1 teacher, got %d", n)
	}
	decode[obj](t, do("DELETE", teachers+"/"+f.teacher.ID.String(), nil), 204)
	decode[obj](t, do("DELETE", teachers+"/"+f.teacher.ID.String(), nil), 404)

	// A student, moved into a class of another stage: the stage follows.
	st := decode[obj](t, do("POST", "/admin/students", obj{
		"display_name": "Gao", "cat_stage": 3, "class_id": classID, "date_of_birth": "2017-03-02",
	}), 201)
	stID := st["id"].(string)
	decode[obj](t, do("POST", "/admin/students", obj{
		"display_name": "Wrong", "cat_stage": 5, "class_id": classID, "date_of_birth": "2017-03-02",
	}), 400)
	moved := decode[obj](t, do("PATCH", "/admin/students/"+stID, obj{"class_id": f.classID}), 200)
	if moved["cat_stage"] != float64(4) || moved["class_id"] != f.classID.String() {
		t.Fatalf("a class move must take the class's stage: %v", moved)
	}
	if strings.Contains(do("GET", "/admin/students/"+stID, nil).String(), "2017") {
		t.Fatal("date of birth was serialised")
	}

	roster := func(extra string) int {
		body := decode[obj](t, do("GET", "/admin/students?class_id="+f.classID.String()+extra, nil), 200)
		return len(body["students"].([]any))
	}
	before := roster("")
	decode[obj](t, do("POST", "/admin/students/"+stID+"/archive", nil), 200)
	if roster("") != before-1 || roster("&include_archived=true") != before {
		t.Fatal("an archived student must leave the roster and stay findable with include_archived")
	}
	decode[obj](t, do("POST", "/admin/students/"+stID+"/restore", nil), 200)
	if roster("") != before {
		t.Fatal("restore did not bring the student back")
	}
	decode[obj](t, do("GET", "/admin/students", nil), 400) // class_id is required
}

// Revoking a binding through the API closes the parent's access at once.
func TestAdminRevokesGuardianBinding(t *testing.T) {
	f := newFixture(t)
	child := f.ownChild.ID.String()

	detail := decode[obj](t, f.h.Do("GET", "/admin/students/"+child, f.adminToken, nil), 200)
	guardians := detail["guardians"].([]any)
	if len(guardians) != 1 || guardians[0].(obj)["account_id"] != f.parent.ID.String() {
		t.Fatalf("want the one bound parent, got %v", guardians)
	}

	path := "/admin/students/" + child + "/guardians/" + f.parent.ID.String()
	decode[obj](t, f.h.Do("DELETE", path, f.adminToken, nil), 204)
	decode[obj](t, f.h.Do("GET", "/parent/children/"+child+"/progress", f.parentToken, nil), 404)
	decode[obj](t, f.h.Do("DELETE", path, f.adminToken, nil), 404)
}

func TestAdminManagesAccounts(t *testing.T) {
	f := newFixture(t)
	do := func(method, path string, body any) testsupport.Response {
		return f.h.Do(method, path, f.adminToken, body)
	}

	created := decode[obj](t, do("POST", "/admin/accounts", obj{
		"phone": "13900139000", "role": "teacher", "display_name": "李老师",
	}), 201)
	if created["phone"] != "+8613900139000" || created["display_name"] != "李老师" {
		t.Fatalf("unexpected account: %v", created)
	}
	decode[obj](t, do("POST", "/admin/accounts", obj{"phone": "13900139001", "role": "parent"}), 400)

	teachers := decode[obj](t, do("GET", "/admin/accounts?role=teacher", nil), 200)
	if n := len(teachers["accounts"].([]any)); n != 2 {
		t.Fatalf("want 2 teachers, got %d", n)
	}
	byPhone := decode[obj](t, do("GET", "/admin/accounts?phone=13900139000", nil), 200)
	if n := len(byPhone["accounts"].([]any)); n != 1 {
		t.Fatalf("want 1 account by phone, got %d", n)
	}
	decode[obj](t, do("GET", "/admin/accounts?role=janitor", nil), 400)

	id := f.teacher.ID.String()
	decode[obj](t, do("PATCH", "/admin/accounts/"+id, obj{"display_name": "王老师"}), 200)
	if got := decode[obj](t, do("GET", "/admin/accounts/"+id, nil), 200); got["display_name"] != "王老师" {
		t.Fatalf("rename did not stick: %v", got)
	}

	// Disabling bites on the very next request, with the token still unexpired.
	decode[obj](t, f.h.Do("GET", "/teacher/classes", f.teacherToken, nil), 200)
	disabled := decode[obj](t, do("POST", "/admin/accounts/"+id+"/disable", nil), 200)
	if disabled["disabled_at"] == nil {
		t.Fatal("disabled_at is missing")
	}
	decode[obj](t, f.h.Do("GET", "/teacher/classes", f.teacherToken, nil), 403)
	decode[obj](t, do("POST", "/admin/accounts/"+id+"/enable", nil), 200)
	decode[obj](t, f.h.Do("GET", "/teacher/classes", f.teacherToken, nil), 200)

	// An admin cannot lock themself out.
	decode[obj](t, do("POST", "/admin/accounts/"+f.admin.ID.String()+"/disable", nil), 400)
	decode[obj](t, do("POST", "/admin/accounts/"+uuid.NewString()+"/disable", nil), 404)
}

// Generate, list, revoke, expire: every state a code can be in, and the proof
// that only an active one binds.
func TestEnrollmentCodeLifecycle(t *testing.T) {
	f := newFixture(t)
	admin := func(method, path string, body any) testsupport.Response {
		return f.h.Do(method, path, f.adminToken, body)
	}
	bind := func(code string) int {
		return f.h.Do("POST", "/parent/bind", f.strangerToken, obj{
			"enrollment_code": code,
			"policy_version":  testsupport.PolicyVersion,
			"accepted_scopes": []string{"progress"},
		}).Status
	}
	child := f.otherChild.ID

	// The class sheet: one code per active student, each named.
	sheet := decode[obj](t, admin("POST", "/admin/classes/"+f.classID.String()+"/enrollment-codes", nil), 201)
	codes := sheet["codes"].([]any)
	if len(codes) != 6 {
		t.Fatalf("want a code for each of the 6 students, got %d", len(codes))
	}
	if name, _ := codes[0].(obj)["display_name"].(string); name == "" {
		t.Fatal("the class sheet must name each student")
	}
	decode[obj](t, admin("POST", "/admin/classes/"+uuid.NewString()+"/enrollment-codes", nil), 404)
	// Clear the sheet's code for this child so the counts below are exact.
	for _, c := range codes {
		if c.(obj)["student_id"] == child.String() {
			decode[obj](t, admin("POST", "/admin/enrollment-codes/"+c.(obj)["id"].(string)+"/revoke", nil), 200)
		}
	}

	revoked := decode[obj](t, admin("POST", "/admin/enrollment-codes", obj{"student_id": child}), 201)
	used := decode[obj](t, admin("POST", "/admin/enrollment-codes", obj{"student_id": child}), 201)
	spare := decode[obj](t, admin("POST", "/admin/enrollment-codes", obj{"student_id": child}), 201)
	f.h.W.AddCode("an-expired-hash", child, time.Now().Add(-time.Hour))

	// Revoked: refused at bind, and cannot be revoked twice.
	revoke := "/admin/enrollment-codes/" + revoked["id"].(string) + "/revoke"
	decode[obj](t, admin("POST", revoke, nil), 200)
	if got := bind(revoked["code"].(string)); got != 404 {
		t.Fatalf("a revoked code bound: %d", got)
	}
	decode[obj](t, admin("POST", revoke, nil), 409)
	decode[obj](t, admin("POST", "/admin/enrollment-codes/"+uuid.NewString()+"/revoke", nil), 404)

	// Consumed: binds once, and is then too late to revoke.
	if got := bind(used["code"].(string)); got != 201 {
		t.Fatalf("an active code did not bind: %d", got)
	}
	decode[obj](t, admin("POST", "/admin/enrollment-codes/"+used["id"].(string)+"/revoke", nil), 409)

	listed := decode[obj](t, admin("GET", "/admin/enrollment-codes?student_id="+child.String(), nil), 200)
	counts := map[string]int{}
	for _, c := range listed["codes"].([]any) {
		counts[c.(obj)["status"].(string)]++
	}
	want := map[string]int{"active": 1, "revoked": 2, "consumed": 1, "expired": 1}
	for status, n := range want {
		if counts[status] != n {
			t.Fatalf("want %v, got %v", want, counts)
		}
	}
	// The listing never carries a usable code.
	body := admin("GET", "/admin/enrollment-codes?class_id="+f.classID.String(), nil).String()
	if strings.Contains(body, `"code"`) || strings.Contains(body, spare["code"].(string)) {
		t.Fatalf("the listing leaked a plaintext code: %s", body)
	}
	decode[obj](t, admin("GET", "/admin/enrollment-codes", nil), 400)

	// An archived student cannot be issued a code.
	decode[obj](t, admin("POST", "/admin/students/"+child.String()+"/archive", nil), 200)
	decode[obj](t, admin("POST", "/admin/enrollment-codes", obj{"student_id": child}), 404)
}
