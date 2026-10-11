// Package teacher serves the staff-facing API: the classes a teacher is
// assigned to, the students in them, and assessment entry.
//
// The scoping rule here mirrors parent/: a teacher reaches a student only
// through a live class_teachers assignment, resolved server-side. Every Store
// method takes the account id for the same reason.
//
// This package must not import internal/parent.
package teacher

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/student"
)

var (
	// ErrNotTaught covers "no such class or student" and "not yours". As on
	// the parent side, the caller answers 404 without distinguishing them.
	ErrNotTaught = errors.New("teacher: not assigned to this class")

	ErrEmptyBatch = errors.New("teacher: batch contains no entries")
)

// MaxBatchSize caps one bulk submission. High enough for a full class day,
// low enough that a runaway client cannot hold a transaction open.
const MaxBatchSize = 500

// NewAssessment is one row a teacher is submitting. SessionDate is filled in
// from the batch default when the entry omits it.
type NewAssessment struct {
	StudentID     uuid.UUID     `json:"student_id"`
	SessionDate   *student.Date `json:"session_date,omitempty"`
	Score         *float64      `json:"score"`
	Participation *int          `json:"participation"`
	Note          string        `json:"note,omitempty"`
}

// Validate checks what can be checked without the database. The schema
// re-checks the ranges; this exists so a teacher gets a useful message instead
// of a constraint violation.
func (n NewAssessment) Validate() error {
	if n.StudentID == uuid.Nil {
		return errors.New("student_id is required")
	}
	if n.Score == nil && n.Participation == nil && n.Note == "" {
		return errors.New("an entry must carry at least one of score, participation or note")
	}
	if n.Score != nil && (*n.Score < 0 || *n.Score > 100) {
		return errors.New("score must be between 0 and 100")
	}
	if n.Participation != nil && (*n.Participation < 0 || *n.Participation > 5) {
		return errors.New("participation must be between 0 and 5")
	}
	if len([]rune(n.Note)) > 2000 {
		return errors.New("note is too long")
	}
	return nil
}

type Store interface {
	Classes(ctx context.Context, accountID uuid.UUID) ([]student.Class, error)
	ClassStudents(ctx context.Context, accountID, classID uuid.UUID) ([]student.Student, error)
	StudentHistory(ctx context.Context, accountID, studentID uuid.UUID, limit int) ([]student.Assessment, error)
	// RecordAssessments writes the whole batch or none of it. The index of the
	// first entry whose student is not taught by this account is returned with
	// ErrNotTaught.
	RecordAssessments(ctx context.Context, accountID uuid.UUID, entries []NewAssessment) ([]uuid.UUID, int, error)
}

type Service struct {
	Store Store
	Now   func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

func (s Service) Classes(ctx context.Context, accountID uuid.UUID) ([]student.Class, error) {
	return s.Store.Classes(ctx, accountID)
}

func (s Service) ClassStudents(ctx context.Context, accountID, classID uuid.UUID) ([]student.Student, error) {
	return s.Store.ClassStudents(ctx, accountID, classID)
}

func (s Service) StudentHistory(ctx context.Context, accountID, studentID uuid.UUID, limit int) ([]student.Assessment, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	return s.Store.StudentHistory(ctx, accountID, studentID, limit)
}

// Record writes a batch. The single-entry endpoint is a batch of one, so there
// is exactly one write path to reason about.
//
// The second return value is the index of the entry that failed, or -1.
func (s Service) Record(ctx context.Context, accountID uuid.UUID, defaultDate student.Date, entries []NewAssessment) ([]uuid.UUID, int, error) {
	if len(entries) == 0 {
		return nil, -1, ErrEmptyBatch
	}
	if len(entries) > MaxBatchSize {
		return nil, -1, errors.New("teacher: batch is too large")
	}

	today := student.NewDate(s.now())
	normalized := make([]NewAssessment, len(entries))
	for i, e := range entries {
		if e.SessionDate == nil {
			d := defaultDate
			if d.IsZero() {
				d = today
			}
			e.SessionDate = &d
		}
		if e.SessionDate.After(today) {
			return nil, i, errors.New("session_date is in the future")
		}
		if err := e.Validate(); err != nil {
			return nil, i, err
		}
		normalized[i] = e
	}
	return s.Store.RecordAssessments(ctx, accountID, normalized)
}
