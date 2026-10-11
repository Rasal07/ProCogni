package main

import (
	"net/http"
	"testing"
)

type obj = map[string]any

// The paths the frontend will walk first: sign in with the fixed code as each
// seeded role, see seeded data, and bind a new parent with a seeded code.
func TestMockIsUsableOutOfTheBox(t *testing.T) {
	h := newMock()

	signIn := func(phone string) (token, role string) {
		t.Helper()
		if res := h.Do(http.MethodPost, "/auth/otp/request", "", obj{"phone": phone}); res.Status != 202 {
			t.Fatalf("otp request for %s: %d %s", phone, res.Status, res.String())
		}
		res := h.Do(http.MethodPost, "/auth/otp/verify", "", obj{"phone": phone, "code": otpCode})
		var out struct{ Token, Role string }
		if err := res.JSON(&out); err != nil || res.Status != 200 {
			t.Fatalf("verify for %s: %d %s", phone, res.Status, res.String())
		}
		return out.Token, out.Role
	}
	get := func(path, token string, want int) obj {
		t.Helper()
		res := h.Do(http.MethodGet, path, token, nil)
		var out obj
		if err := res.JSON(&out); err != nil || res.Status != want {
			t.Fatalf("GET %s: want %d, got %d %s", path, want, res.Status, res.String())
		}
		return out
	}

	teacher, role := signIn(teacherPhone)
	if role != "teacher" {
		t.Fatalf("want teacher, got %q", role)
	}
	classes := get("/teacher/classes", teacher, 200)["classes"].([]any)
	if len(classes) != 2 {
		t.Fatalf("want 2 seeded classes, got %d", len(classes))
	}
	class := classes[0].(obj)
	roster := get("/teacher/classes/"+class["id"].(string)+"/students", teacher, 200)["students"].([]any)
	if len(roster) == 0 || int(class["student_count"].(float64)) != len(roster) {
		t.Fatalf("roster of %d does not match student_count %v", len(roster), class["student_count"])
	}
	history := get("/teacher/students/"+roster[0].(obj)["id"].(string)+"/history", teacher, 200)["sessions"].([]any)
	if len(history) != 8 {
		t.Fatalf("want 8 weeks of history, got %d", len(history))
	}

	parent, _ := signIn(parentPhone)
	children := get("/parent/children", parent, 200)["children"].([]any)
	if len(children) != 2 {
		t.Fatalf("want 2 seeded children, got %d", len(children))
	}
	// One child shows a full comparison, the other the consent prompt. The
	// list is name-ordered, so find them by outcome rather than by position.
	var full, gated int
	for _, c := range children {
		res := h.Do(http.MethodGet, "/parent/children/"+c.(obj)["id"].(string)+"/comparison", parent, nil)
		var body obj
		_ = res.JSON(&body)
		switch {
		case res.Status == 200 && body["class"] != nil:
			full++
		case res.Status == 403:
			gated++
		default:
			t.Fatalf("unexpected comparison: %d %s", res.Status, res.String())
		}
	}
	if full != 1 || gated != 1 {
		t.Fatalf("want one full comparison and one consent_required, got %d and %d", full, gated)
	}

	admin, role := signIn(adminPhone)
	if role != "admin" || len(get("/admin/schools", admin, 200)["schools"].([]any)) != 1 {
		t.Fatal("the seeded admin cannot see the seeded school")
	}

	// A number nobody has seen becomes a parent and binds with a seeded code.
	fresh, role := signIn("13912345678")
	if role != "parent" {
		t.Fatalf("want parent, got %q", role)
	}
	res := h.Do(http.MethodPost, "/parent/bind", fresh, obj{
		"enrollment_code": enrollmentCodes[0], "policy_version": policyVersion,
		"accepted_scopes": []string{"progress", "comparison"},
	})
	if res.Status != 201 {
		t.Fatalf("bind with a seeded code: %d %s", res.Status, res.String())
	}
}
