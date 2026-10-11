package student

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestAgeAt(t *testing.T) {
	s := Student{DateOfBirth: time.Date(2016, 5, 20, 0, 0, 0, 0, time.UTC)}

	cases := map[string]struct {
		at   time.Time
		want int
	}{
		"the day before the birthday": {time.Date(2026, 5, 19, 0, 0, 0, 0, time.UTC), 9},
		"on the birthday":             {time.Date(2026, 5, 20, 0, 0, 0, 0, time.UTC), 10},
		"the day after":               {time.Date(2026, 5, 21, 0, 0, 0, 0, time.UTC), 10},
		"years later":                 {time.Date(2031, 1, 1, 0, 0, 0, 0, time.UTC), 14},
	}
	for name, c := range cases {
		if got := s.AgeAt(c.at); got != c.want {
			t.Errorf("%s: want %d, got %d", name, c.want, got)
		}
	}
}

// Under 14 is sensitive personal information under PIPL, and the boundary is
// the part worth pinning.
func TestIsSensitiveMinorBoundary(t *testing.T) {
	s := Student{DateOfBirth: time.Date(2012, 6, 1, 0, 0, 0, 0, time.UTC)}

	dayBefore14 := time.Date(2026, 5, 31, 0, 0, 0, 0, time.UTC)
	if !s.IsSensitiveMinor(dayBefore14) {
		t.Error("a student one day short of 14 must still be sensitive")
	}
	on14 := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	if s.IsSensitiveMinor(on14) {
		t.Error("a student on their 14th birthday is no longer a sensitive minor")
	}
}

func TestParentViewDropsTeacherFields(t *testing.T) {
	score, participation := 82.5, 4
	a := Assessment{
		SessionDate:   time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		Score:         &score,
		Participation: &participation,
		Note:          "struggles with fractions; called home",
	}

	encoded, err := json.Marshal(a.ParentView())
	if err != nil {
		t.Fatal(err)
	}
	body := string(encoded)
	for _, forbidden := range []string{"struggles", "note", "recorded_by", "student_id", "id"} {
		if strings.Contains(body, forbidden) {
			t.Errorf("parent view contains %q: %s", forbidden, body)
		}
	}
	if !strings.Contains(body, "82.5") {
		t.Errorf("parent view lost the score: %s", body)
	}
}

// A student's date of birth is never serialised: no client needs it, and it is
// the field that makes the rest identifying.
func TestStudentJSONOmitsDateOfBirth(t *testing.T) {
	s := Student{
		DisplayName: "Aiden",
		CATStage:    4,
		DateOfBirth: time.Date(2016, 5, 20, 0, 0, 0, 0, time.UTC),
	}
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "2016") || strings.Contains(string(encoded), "date_of_birth") {
		t.Fatalf("student JSON exposes the date of birth: %s", encoded)
	}
}

func TestDateRoundTrip(t *testing.T) {
	var d Date
	if err := json.Unmarshal([]byte(`"2026-09-20"`), &d); err != nil {
		t.Fatal(err)
	}
	if d.String() != "2026-09-20" {
		t.Fatalf("want 2026-09-20, got %s", d)
	}

	encoded, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != `"2026-09-20"` {
		t.Fatalf("want a bare date, got %s", encoded)
	}

	// A timestamp is not a date.
	if err := json.Unmarshal([]byte(`"2026-09-20T10:00:00Z"`), &d); err == nil {
		t.Fatal("an RFC3339 timestamp was accepted as a date")
	}
}

func TestDateZeroMarshalsNull(t *testing.T) {
	encoded, err := json.Marshal(Date{})
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != "null" {
		t.Fatalf("want null, got %s", encoded)
	}
}

func TestValidCATStage(t *testing.T) {
	for stage, want := range map[int]bool{2: false, 3: true, 4: true, 5: true, 6: false} {
		if got := ValidCATStage(stage); got != want {
			t.Errorf("stage %d: want %v, got %v", stage, want, got)
		}
	}
}
