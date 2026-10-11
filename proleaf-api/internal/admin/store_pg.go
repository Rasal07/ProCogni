package admin

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/db"
	"github.com/proleaf/proleaf-api/internal/student"
)

type pgStore struct{ c db.Conn }

func NewStore(c db.Conn) Store { return pgStore{c: c} }

// isForeignKey reports a foreign_key_violation: an id in the request that
// names no row. Callers turn it into the matching "unknown" error so that it
// is a 404 and not a 500.
func isForeignKey(err error) bool {
	var pg *pgconn.PgError
	return errors.As(err, &pg) && pg.Code == "23503"
}

// ---------- schools ----------

func (s pgStore) CreateSchool(ctx context.Context, name string) (School, error) {
	var sc School
	err := s.c.QueryRow(ctx, `
		INSERT INTO schools (name) VALUES ($1)
		RETURNING id, name, created_at`, name).Scan(&sc.ID, &sc.Name, &sc.CreatedAt)
	if err != nil {
		return School{}, fmt.Errorf("admin: create school: %w", err)
	}
	return sc, nil
}

func (s pgStore) Schools(ctx context.Context) ([]School, error) {
	rows, err := s.c.Query(ctx, `SELECT id, name, created_at FROM schools ORDER BY name`)
	if err != nil {
		return nil, fmt.Errorf("admin: schools: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (School, error) {
		var sc School
		return sc, row.Scan(&sc.ID, &sc.Name, &sc.CreatedAt)
	})
}

func (s pgStore) RenameSchool(ctx context.Context, id uuid.UUID, name string) (School, error) {
	var sc School
	err := s.c.QueryRow(ctx, `
		UPDATE schools SET name = $2 WHERE id = $1
		RETURNING id, name, created_at`, id, name).Scan(&sc.ID, &sc.Name, &sc.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return School{}, ErrSchoolUnknown
	}
	if err != nil {
		return School{}, fmt.Errorf("admin: rename school: %w", err)
	}
	return sc, nil
}

// ---------- classes ----------

const classColumns = `c.id, c.name, c.cat_stage, c.academic_year, c.school_id,
	(SELECT COUNT(*) FROM students st WHERE st.class_id = c.id AND st.archived_at IS NULL)`

func scanClass(row pgx.Row) (student.Class, error) {
	var c student.Class
	err := row.Scan(&c.ID, &c.Name, &c.CATStage, &c.AcademicYear, &c.SchoolID, &c.StudentCount)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return student.Class{}, ErrClassUnknown
	case isForeignKey(err):
		return student.Class{}, ErrSchoolUnknown
	case err != nil:
		return student.Class{}, fmt.Errorf("admin: scan class: %w", err)
	}
	return c, nil
}

func (s pgStore) CreateClass(ctx context.Context, in NewClass) (student.Class, error) {
	return scanClass(s.c.QueryRow(ctx, `
		INSERT INTO classes AS c (name, cat_stage, academic_year, school_id)
		VALUES ($1, $2, $3, $4)
		RETURNING `+classColumns, in.Name, in.CATStage, in.AcademicYear, in.SchoolID))
}

func (s pgStore) Classes(ctx context.Context, schoolID *uuid.UUID) ([]student.Class, error) {
	rows, err := s.c.Query(ctx, `
		SELECT `+classColumns+`
		FROM classes c
		WHERE ($1::uuid IS NULL OR c.school_id = $1)
		ORDER BY c.academic_year DESC, c.name`, schoolID)
	if err != nil {
		return nil, fmt.Errorf("admin: classes: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (student.Class, error) { return scanClass(row) })
}

func (s pgStore) Class(ctx context.Context, id uuid.UUID) (student.Class, error) {
	return scanClass(s.c.QueryRow(ctx, `SELECT `+classColumns+` FROM classes c WHERE c.id = $1`, id))
}

func (s pgStore) UpdateClass(ctx context.Context, id uuid.UUID, p ClassPatch) (student.Class, error) {
	return scanClass(s.c.QueryRow(ctx, `
		UPDATE classes c
		SET name          = COALESCE($2, c.name),
		    academic_year = COALESCE($3, c.academic_year),
		    school_id     = COALESCE($4, c.school_id)
		WHERE c.id = $1
		RETURNING `+classColumns, id, p.Name, p.AcademicYear, p.SchoolID))
}

func (s pgStore) ClassTeachers(ctx context.Context, classID uuid.UUID) ([]auth.Account, error) {
	rows, err := s.c.Query(ctx, `
		SELECT `+accountColumns+`
		FROM accounts a
		JOIN class_teachers ct ON ct.account_id = a.id
		WHERE ct.class_id = $1 AND ct.unassigned_at IS NULL
		ORDER BY a.display_name, a.phone`, classID)
	if err != nil {
		return nil, fmt.Errorf("admin: class teachers: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (auth.Account, error) { return scanAccount(row) })
}

func (s pgStore) AssignTeacher(ctx context.Context, classID, accountID uuid.UUID) error {
	// Re-assigning a previously removed teacher clears unassigned_at rather
	// than inserting a second row.
	_, err := s.c.Exec(ctx, `
		INSERT INTO class_teachers (class_id, account_id)
		VALUES ($1, $2)
		ON CONFLICT (class_id, account_id)
		DO UPDATE SET unassigned_at = NULL, assigned_at = now()`, classID, accountID)
	if isForeignKey(err) {
		// The service has already confirmed the account, so it is the class.
		return ErrClassUnknown
	}
	if err != nil {
		return fmt.Errorf("admin: assign teacher: %w", err)
	}
	return nil
}

func (s pgStore) UnassignTeacher(ctx context.Context, classID, accountID uuid.UUID, now time.Time) error {
	tag, err := s.c.Exec(ctx, `
		UPDATE class_teachers SET unassigned_at = $3
		WHERE class_id = $1 AND account_id = $2 AND unassigned_at IS NULL`, classID, accountID, now)
	if err != nil {
		return fmt.Errorf("admin: unassign teacher: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoLink
	}
	return nil
}

// ---------- students ----------

const studentColumns = `st.id, st.display_name, st.cat_stage, st.class_id, st.date_of_birth, st.archived_at`

func scanStudent(row pgx.Row) (student.Student, error) {
	var st student.Student
	err := row.Scan(&st.ID, &st.DisplayName, &st.CATStage, &st.ClassID, &st.DateOfBirth, &st.ArchivedAt)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return student.Student{}, ErrStudentUnknown
	case isForeignKey(err):
		return student.Student{}, ErrClassUnknown
	case err != nil:
		return student.Student{}, fmt.Errorf("admin: scan student: %w", err)
	}
	return st, nil
}

func (s pgStore) CreateStudent(ctx context.Context, in NewStudent) (student.Student, error) {
	return scanStudent(s.c.QueryRow(ctx, `
		INSERT INTO students AS st (display_name, cat_stage, class_id, date_of_birth)
		VALUES ($1, $2, $3, $4)
		RETURNING `+studentColumns, in.DisplayName, in.CATStage, in.ClassID, in.DateOfBirth.Time()))
}

func (s pgStore) Students(ctx context.Context, classID uuid.UUID, includeArchived bool) ([]student.Student, error) {
	rows, err := s.c.Query(ctx, `
		SELECT `+studentColumns+`
		FROM students st
		WHERE st.class_id = $1 AND ($2 OR st.archived_at IS NULL)
		ORDER BY st.display_name`, classID, includeArchived)
	if err != nil {
		return nil, fmt.Errorf("admin: students: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (student.Student, error) { return scanStudent(row) })
}

func (s pgStore) Student(ctx context.Context, id uuid.UUID) (student.Student, error) {
	return scanStudent(s.c.QueryRow(ctx, `SELECT `+studentColumns+` FROM students st WHERE st.id = $1`, id))
}

func (s pgStore) UpdateStudent(ctx context.Context, id uuid.UUID, p StudentPatch, stage *int) (student.Student, error) {
	return scanStudent(s.c.QueryRow(ctx, `
		UPDATE students st
		SET display_name = COALESCE($2, st.display_name),
		    class_id     = COALESCE($3, st.class_id),
		    cat_stage    = COALESCE($4, st.cat_stage)
		WHERE st.id = $1
		RETURNING `+studentColumns, id, p.DisplayName, p.ClassID, stage))
}

func (s pgStore) SetStudentArchived(ctx context.Context, id uuid.UUID, archivedAt *time.Time) (student.Student, error) {
	return scanStudent(s.c.QueryRow(ctx, `
		UPDATE students st SET archived_at = $2 WHERE st.id = $1
		RETURNING `+studentColumns, id, archivedAt))
}

func (s pgStore) Guardians(ctx context.Context, studentID uuid.UUID) ([]Guardian, error) {
	rows, err := s.c.Query(ctx, `
		SELECT gb.account_id, a.phone, gb.bound_at
		FROM guardian_bindings gb
		JOIN accounts a ON a.id = gb.account_id
		WHERE gb.student_id = $1 AND gb.revoked_at IS NULL
		ORDER BY gb.bound_at`, studentID)
	if err != nil {
		return nil, fmt.Errorf("admin: guardians: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Guardian, error) {
		var g Guardian
		return g, row.Scan(&g.AccountID, &g.Phone, &g.BoundAt)
	})
}

func (s pgStore) RevokeBinding(ctx context.Context, studentID, accountID uuid.UUID, now time.Time) error {
	tag, err := s.c.Exec(ctx, `
		UPDATE guardian_bindings SET revoked_at = $3
		WHERE student_id = $1 AND account_id = $2 AND revoked_at IS NULL`, studentID, accountID, now)
	if err != nil {
		return fmt.Errorf("admin: revoke binding: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNoLink
	}
	return nil
}

// ---------- accounts ----------

const accountColumns = `a.id, a.phone, a.role, COALESCE(a.display_name, ''), a.disabled_at`

func scanAccount(row pgx.Row) (auth.Account, error) {
	var a auth.Account
	var role string
	err := row.Scan(&a.ID, &a.Phone, &role, &a.DisplayName, &a.DisabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.Account{}, ErrAccountUnknown
	}
	if err != nil {
		return auth.Account{}, fmt.Errorf("admin: scan account: %w", err)
	}
	a.Role = auth.Role(role)
	return a, nil
}

func (s pgStore) Accounts(ctx context.Context, f AccountFilter) ([]auth.Account, error) {
	rows, err := s.c.Query(ctx, `
		SELECT `+accountColumns+`
		FROM accounts a
		WHERE ($1 = '' OR a.role = $1)
		  AND ($2 = '' OR a.phone = $2)
		ORDER BY a.created_at DESC
		LIMIT $3`, string(f.Role), f.Phone, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("admin: accounts: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (auth.Account, error) { return scanAccount(row) })
}

func (s pgStore) RenameAccount(ctx context.Context, id uuid.UUID, displayName string) (auth.Account, error) {
	return scanAccount(s.c.QueryRow(ctx, `
		UPDATE accounts a SET display_name = $2 WHERE a.id = $1
		RETURNING `+accountColumns, id, displayName))
}

func (s pgStore) SetAccountDisabled(ctx context.Context, id uuid.UUID, disabledAt *time.Time) (auth.Account, error) {
	return scanAccount(s.c.QueryRow(ctx, `
		UPDATE accounts a SET disabled_at = $2 WHERE a.id = $1
		RETURNING `+accountColumns, id, disabledAt))
}

// ---------- enrollment codes ----------

func (s pgStore) CreateEnrollmentCode(ctx context.Context, id, studentID, issuedBy uuid.UUID, codeHash string, expiresAt time.Time) error {
	_, err := s.c.Exec(ctx, `
		INSERT INTO enrollment_codes (id, code_hash, student_id, issued_by, expires_at)
		VALUES ($1, $2, $3, $4, $5)`, id, codeHash, studentID, issuedBy, expiresAt)
	if err != nil {
		return fmt.Errorf("admin: create enrollment code: %w", err)
	}
	return nil
}

func (s pgStore) EnrollmentCodes(ctx context.Context, studentID, classID *uuid.UUID) ([]Code, error) {
	rows, err := s.c.Query(ctx, `
		SELECT ec.id, ec.student_id, ec.issued_at, ec.expires_at, ec.consumed_at, ec.revoked_at
		FROM enrollment_codes ec
		JOIN students st ON st.id = ec.student_id
		WHERE ($1::uuid IS NULL OR ec.student_id = $1)
		  AND ($2::uuid IS NULL OR st.class_id = $2)
		ORDER BY ec.issued_at DESC`, studentID, classID)
	if err != nil {
		return nil, fmt.Errorf("admin: enrollment codes: %w", err)
	}
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Code, error) {
		var c Code
		return c, row.Scan(&c.ID, &c.StudentID, &c.IssuedAt, &c.ExpiresAt, &c.ConsumedAt, &c.RevokedAt)
	})
}

func (s pgStore) RevokeEnrollmentCode(ctx context.Context, id, revokedBy uuid.UUID, now time.Time) error {
	tag, err := s.c.Exec(ctx, `
		UPDATE enrollment_codes SET revoked_at = $3, revoked_by = $2
		WHERE id = $1 AND consumed_at IS NULL AND revoked_at IS NULL`, id, revokedBy, now)
	if err != nil {
		return fmt.Errorf("admin: revoke enrollment code: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// Nothing changed: tell "no such code" from "too late to revoke".
	var exists bool
	if err := s.c.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM enrollment_codes WHERE id = $1)`, id).Scan(&exists); err != nil {
		return fmt.Errorf("admin: revoke enrollment code: %w", err)
	}
	if !exists {
		return ErrCodeUnknown
	}
	return ErrCodeSpent
}
