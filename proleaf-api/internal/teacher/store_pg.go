package teacher

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proleaf/proleaf-api/internal/db"
	"github.com/proleaf/proleaf-api/internal/student"
)

// Every query here joins class_teachers on the acting account and filters on
// unassigned_at IS NULL. TestTeacherQueriesAreAssignmentScoped enforces it.

type pgStore struct {
	db *db.DB
}

func NewStore(d *db.DB) Store { return pgStore{db: d} }

func (s pgStore) Classes(ctx context.Context, accountID uuid.UUID) ([]student.Class, error) {
	rows, err := s.db.Conn().Query(ctx, `
		SELECT c.id, c.name, c.cat_stage, c.academic_year,
		       COUNT(st.id) FILTER (WHERE st.archived_at IS NULL)
		FROM classes c
		JOIN class_teachers ct ON ct.class_id = c.id
		LEFT JOIN students st ON st.class_id = c.id
		WHERE ct.account_id = $1
		  AND ct.unassigned_at IS NULL
		GROUP BY c.id, c.name, c.cat_stage, c.academic_year
		ORDER BY c.academic_year DESC, c.name`, accountID)
	if err != nil {
		return nil, fmt.Errorf("teacher: classes: %w", err)
	}
	defer rows.Close()

	out := []student.Class{}
	for rows.Next() {
		var c student.Class
		if err := rows.Scan(&c.ID, &c.Name, &c.CATStage, &c.AcademicYear, &c.StudentCount); err != nil {
			return nil, fmt.Errorf("teacher: scan class: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s pgStore) ClassStudents(ctx context.Context, accountID, classID uuid.UUID) ([]student.Student, error) {
	// Assignment is confirmed first, so an unassigned class is reported as
	// missing rather than as an empty class.
	var taught bool
	if err := s.db.Conn().QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM class_teachers ct
			WHERE ct.account_id = $1 AND ct.class_id = $2 AND ct.unassigned_at IS NULL
		)`, accountID, classID).Scan(&taught); err != nil {
		return nil, fmt.Errorf("teacher: check assignment: %w", err)
	}
	if !taught {
		return nil, ErrNotTaught
	}

	rows, err := s.db.Conn().Query(ctx, `
		SELECT st.id, st.display_name, st.cat_stage, st.class_id, st.date_of_birth, st.archived_at
		FROM students st
		JOIN class_teachers ct ON ct.class_id = st.class_id
		WHERE ct.account_id = $1
		  AND st.class_id = $2
		  AND ct.unassigned_at IS NULL
		  AND st.archived_at IS NULL
		ORDER BY st.display_name`, accountID, classID)
	if err != nil {
		return nil, fmt.Errorf("teacher: class students: %w", err)
	}
	defer rows.Close()

	out := []student.Student{}
	for rows.Next() {
		var st student.Student
		if err := rows.Scan(&st.ID, &st.DisplayName, &st.CATStage, &st.ClassID,
			&st.DateOfBirth, &st.ArchivedAt); err != nil {
			return nil, fmt.Errorf("teacher: scan student: %w", err)
		}
		out = append(out, st)
	}
	return out, rows.Err()
}

func (s pgStore) StudentHistory(ctx context.Context, accountID, studentID uuid.UUID, limit int) ([]student.Assessment, error) {
	rows, err := s.db.Conn().Query(ctx, `
		SELECT a.id, a.student_id, a.recorded_by, a.session_date,
		       a.score::float8, a.participation, COALESCE(a.note, ''), a.created_at
		FROM assessments a
		JOIN students st ON st.id = a.student_id
		JOIN class_teachers ct ON ct.class_id = st.class_id
		WHERE ct.account_id = $1
		  AND a.student_id = $2
		  AND ct.unassigned_at IS NULL
		ORDER BY a.session_date DESC, a.created_at DESC
		LIMIT $3`, accountID, studentID, limit)
	if err != nil {
		return nil, fmt.Errorf("teacher: student history: %w", err)
	}
	defer rows.Close()

	out := []student.Assessment{}
	for rows.Next() {
		var a student.Assessment
		if err := rows.Scan(&a.ID, &a.StudentID, &a.RecordedBy, &a.SessionDate,
			&a.Score, &a.Participation, &a.Note, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("teacher: scan assessment: %w", err)
		}
		out = append(out, a)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		// Either the student has no records or they are not this teacher's.
		// Confirm assignment so an empty history is not mistaken for a 404.
		var taught bool
		if err := s.db.Conn().QueryRow(ctx, `
			SELECT EXISTS (
				SELECT 1
				FROM students st
				JOIN class_teachers ct ON ct.class_id = st.class_id
				WHERE ct.account_id = $1 AND st.id = $2 AND ct.unassigned_at IS NULL
			)`, accountID, studentID).Scan(&taught); err != nil {
			return nil, fmt.Errorf("teacher: check student assignment: %w", err)
		}
		if !taught {
			return nil, ErrNotTaught
		}
	}
	return out, nil
}

// RecordAssessments inserts the batch in one transaction. The INSERT ... SELECT
// carries the assignment check in its own WHERE clause, so an entry naming a
// student this teacher does not teach inserts zero rows and aborts the batch.
func (s pgStore) RecordAssessments(ctx context.Context, accountID uuid.UUID, entries []NewAssessment) ([]uuid.UUID, int, error) {
	ids := make([]uuid.UUID, 0, len(entries))
	failedIndex := -1

	err := s.db.InTx(ctx, func(c db.Conn) error {
		for i, e := range entries {
			var id uuid.UUID
			err := c.QueryRow(ctx, `
				INSERT INTO assessments
					(student_id, recorded_by, session_date, score, participation, note)
				SELECT st.id, $2, $3, $4, $5, NULLIF($6, '')
				FROM students st
				JOIN class_teachers ct ON ct.class_id = st.class_id
				WHERE st.id = $1
				  AND ct.account_id = $2
				  AND ct.unassigned_at IS NULL
				  AND st.archived_at IS NULL
				RETURNING id`,
				e.StudentID, accountID, e.SessionDate.Time(), e.Score, e.Participation, e.Note).Scan(&id)
			if errors.Is(err, pgx.ErrNoRows) {
				failedIndex = i
				return ErrNotTaught
			}
			if err != nil {
				return fmt.Errorf("teacher: insert assessment: %w", err)
			}
			ids = append(ids, id)
		}
		return nil
	})
	if err != nil {
		return nil, failedIndex, err
	}
	return ids, -1, nil
}
