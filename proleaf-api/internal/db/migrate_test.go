package db

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

var migrationName = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)

// Migrations are numbered and forward-only. This test is cheap insurance
// against the two ways that goes wrong: a gap or a duplicate in the numbering,
// and a down-migration creeping in.
func TestMigrationsAreNumberedContiguously(t *testing.T) {
	names, err := MigrationNames()
	if err != nil {
		t.Fatal(err)
	}
	if len(names) == 0 {
		t.Fatal("no migrations are embedded")
	}

	for i, name := range names {
		m := migrationName.FindStringSubmatch(name)
		if m == nil {
			t.Fatalf("%q does not match NNNN_lower_snake.sql", name)
		}
		want := fmt.Sprintf("%04d", i+1)
		if m[1] != want {
			t.Fatalf("migration %d is numbered %s; want %s (gap or duplicate)", i+1, m[1], want)
		}
	}
}

func TestMigrationsAreForwardOnly(t *testing.T) {
	all, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range all {
		if strings.Contains(strings.ToLower(m.name), "down") {
			t.Errorf("%s looks like a down migration; the scheme is forward-only", m.name)
		}
		if strings.TrimSpace(m.body) == "" {
			t.Errorf("%s is empty", m.name)
		}
	}
}

// The safety property from §2: class_stats must not gain a column that points
// at a student.
func TestClassStatsHasNoStudentReference(t *testing.T) {
	all, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}

	var ddl string
	for _, m := range all {
		lower := strings.ToLower(m.body)
		idx := strings.Index(lower, "create table class_stats")
		if idx < 0 {
			continue
		}
		rest := m.body[idx:]
		end := strings.Index(rest, ");")
		if end < 0 {
			t.Fatalf("could not find the end of the class_stats definition in %s", m.name)
		}
		ddl = strings.ToLower(rest[:end])
	}
	if ddl == "" {
		t.Fatal("class_stats is not created by any migration")
	}

	for _, forbidden := range []string{"student_id", "references students", "top_student"} {
		if strings.Contains(ddl, forbidden) {
			t.Errorf("class_stats contains %q; the absence of a student reference is the safety property", forbidden)
		}
	}
	// It must still carry the aggregate columns the parent endpoint reads.
	for _, required := range []string{"mean_score", "median_score", "top_score", "student_count"} {
		if !strings.Contains(ddl, required) {
			t.Errorf("class_stats is missing %q", required)
		}
	}
}

// audit_log has no UPDATE or DELETE path in the application, and migration
// 0002 makes that true in the database too.
func TestAuditLogIsProtectedAgainstMutation(t *testing.T) {
	all, err := loadMigrations()
	if err != nil {
		t.Fatal(err)
	}
	var combined strings.Builder
	for _, m := range all {
		combined.WriteString(strings.ToLower(m.body))
	}
	body := combined.String()

	if !strings.Contains(body, "before update or delete or truncate on audit_log") {
		t.Error("no trigger guards audit_log against mutation")
	}
}
