package parent

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proleaf/proleaf-api/internal/db"
	"github.com/proleaf/proleaf-api/internal/student"
)

// Every query in this file joins guardian_bindings and filters on
// revoked_at IS NULL. TestParentQueriesAreBindingScoped in store_scope_test.go
// parses this file and fails if a query mentioning students or assessments
// does not also mention guardian_bindings, so the property is checked rather
// than remembered.

type pgStore struct {
	db *db.DB
}

// NewStore returns the Postgres-backed parent store. It takes the pool rather
// than a Conn because Bind needs its own transaction.
func NewStore(d *db.DB) Store { return pgStore{db: d} }

const studentColumns = `st.id, st.display_name, st.cat_stage, st.class_id, st.date_of_birth, st.archived_at`

func (s pgStore) BoundStudent(ctx context.Context, accountID, studentID uuid.UUID) (student.Student, error) {
	return scanStudent(s.db.Conn().QueryRow(ctx, `
		SELECT `+studentColumns+`
		FROM students st
		JOIN guardian_bindings gb ON gb.student_id = st.id
		WHERE gb.account_id = $1
		  AND st.id = $2
		  AND gb.revoked_at IS NULL`, accountID, studentID))
}

func (s pgStore) BoundStudents(ctx context.Context, accountID uuid.UUID) ([]student.Student, error) {
	rows, err := s.db.Conn().Query(ctx, `
		SELECT `+studentColumns+`
		FROM students st
		JOIN guardian_bindings gb ON gb.student_id = st.id
		WHERE gb.account_id = $1
		  AND gb.revoked_at IS NULL
		  AND st.archived_at IS NULL
		ORDER BY st.display_name`, accountID)
	if err != nil {
		return nil, fmt.Errorf("parent: bound students: %w", err)
	}
	defer rows.Close()

	out := []student.Student{}
	for rows.Next() {
		st, err := scanStudentRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s pgStore) BoundAssessments(ctx context.Context, accountID, studentID uuid.UUID, limit int) ([]student.Assessment, error) {
	rows, err := s.db.Conn().Query(ctx, `
		SELECT a.id, a.student_id, a.recorded_by, a.session_date,
		       a.score::float8, a.participation, a.created_at
		FROM assessments a
		JOIN guardian_bindings gb ON gb.student_id = a.student_id
		WHERE gb.account_id = $1
		  AND a.student_id = $2
		  AND gb.revoked_at IS NULL
		ORDER BY a.session_date DESC, a.created_at DESC
		LIMIT $3`, accountID, studentID, limit)
	if err != nil {
		return nil, fmt.Errorf("parent: bound assessments: %w", err)
	}
	defer rows.Close()

	out := []student.Assessment{}
	for rows.Next() {
		var a student.Assessment
		if err := rows.Scan(&a.ID, &a.StudentID, &a.RecordedBy, &a.SessionDate,
			&a.Score, &a.Participation, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("parent: scan assessment: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s pgStore) BoundPeriodScore(ctx context.Context, accountID, studentID uuid.UUID, period time.Time) (*float64, int, error) {
	var mean *float64
	var sessions int
	err := s.db.Conn().QueryRow(ctx, `
		SELECT AVG(a.score)::float8, COUNT(*)
		FROM assessments a
		JOIN guardian_bindings gb ON gb.student_id = a.student_id
		WHERE gb.account_id = $1
		  AND a.student_id = $2
		  AND gb.revoked_at IS NULL
		  AND a.session_date >= $3
		  AND a.session_date <  ($3::date + INTERVAL '1 month')`,
		accountID, studentID, period).Scan(&mean, &sessions)
	if err != nil {
		return nil, 0, fmt.Errorf("parent: period score: %w", err)
	}
	return mean, sessions, nil
}

// Bind consumes the code and writes the binding in one transaction. The UPDATE
// on enrollment_codes carries the usability conditions in its WHERE clause, so
// two devices racing the same code produce exactly one binding.
func (s pgStore) Bind(ctx context.Context, accountID uuid.UUID, codeHash string, now time.Time) (student.Student, error) {
	var bound student.Student
	err := s.db.InTx(ctx, func(c db.Conn) error {
		var studentID uuid.UUID
		err := c.QueryRow(ctx, `
			UPDATE enrollment_codes
			SET consumed_at = $2, consumed_by = $3
			WHERE code_hash = $1
			  AND consumed_at IS NULL
			  AND revoked_at IS NULL
			  AND expires_at > $2
			RETURNING student_id`, codeHash, now, accountID).Scan(&studentID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCodeNotUsable
		}
		if err != nil {
			return fmt.Errorf("parent: consume code: %w", err)
		}

		// A re-bind after a revocation reactivates the existing row rather
		// than creating a second one; the unique constraint requires it.
		tag, err := c.Exec(ctx, `
			INSERT INTO guardian_bindings (account_id, student_id, bound_at)
			VALUES ($1, $2, $3)
			ON CONFLICT (account_id, student_id)
			DO UPDATE SET revoked_at = NULL, bound_at = EXCLUDED.bound_at
			WHERE guardian_bindings.revoked_at IS NOT NULL`,
			accountID, studentID, now)
		if err != nil {
			return fmt.Errorf("parent: create binding: %w", err)
		}
		if tag.RowsAffected() == 0 {
			return ErrAlreadyBound
		}

		bound, err = scanStudent(c.QueryRow(ctx, `
			SELECT `+studentColumns+`
			FROM students st
			JOIN guardian_bindings gb ON gb.student_id = st.id
			WHERE gb.account_id = $1
			  AND st.id = $2
			  AND gb.revoked_at IS NULL`, accountID, studentID))
		return err
	})
	if err != nil {
		return student.Student{}, err
	}
	return bound, nil
}

func scanStudent(row pgx.Row) (student.Student, error) {
	var st student.Student
	err := row.Scan(&st.ID, &st.DisplayName, &st.CATStage, &st.ClassID, &st.DateOfBirth, &st.ArchivedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return student.Student{}, ErrNotBound
	}
	if err != nil {
		return student.Student{}, fmt.Errorf("parent: scan student: %w", err)
	}
	return st, nil
}

func scanStudentRow(rows pgx.Rows) (student.Student, error) {
	var st student.Student
	if err := rows.Scan(&st.ID, &st.DisplayName, &st.CATStage, &st.ClassID,
		&st.DateOfBirth, &st.ArchivedAt); err != nil {
		return student.Student{}, fmt.Errorf("parent: scan student: %w", err)
	}
	return st, nil
}
