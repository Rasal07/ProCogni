// Package student holds the domain types both teacher/ and parent/ need, and
// the pure logic over them. It contains no SQL: each caller owns its own
// queries so that the scoping of those queries is visible in the package that
// has to justify it.
package student

import (
	"time"

	"github.com/google/uuid"
)

// SensitiveMinorAge is the threshold at which PIPL treats a child's personal
// information as sensitive personal information, requiring separate guardian
// consent. Every student in the CAT 3-5 range is expected to be under it; the
// check exists so the assumption is enforced rather than assumed.
const SensitiveMinorAge = 14

type Student struct {
	ID          uuid.UUID  `json:"id"`
	DisplayName string     `json:"display_name"`
	CATStage    int        `json:"cat_stage"`
	ClassID     uuid.UUID  `json:"class_id"`
	DateOfBirth time.Time  `json:"-"` // never serialised: not needed by any client
	ArchivedAt  *time.Time `json:"archived_at,omitempty"`
}

func (s Student) Archived() bool { return s.ArchivedAt != nil }

// AgeAt returns whole years completed at t.
func (s Student) AgeAt(t time.Time) int {
	years := t.Year() - s.DateOfBirth.Year()
	anniversary := s.DateOfBirth.AddDate(years, 0, 0)
	if t.Before(anniversary) {
		years--
	}
	return years
}

// IsSensitiveMinor reports whether this student's data is sensitive personal
// information under PIPL at time t.
func (s Student) IsSensitiveMinor(t time.Time) bool {
	return s.AgeAt(t) < SensitiveMinorAge
}

type Class struct {
	ID           uuid.UUID `json:"id"`
	Name         string    `json:"name"`
	CATStage     int       `json:"cat_stage"`
	AcademicYear string    `json:"academic_year"`
	StudentCount int       `json:"student_count"`
	// SchoolID is set on the admin surface only.
	SchoolID *uuid.UUID `json:"school_id,omitempty"`
}

// Assessment is one teacher-entered record. Note is teacher-facing free text
// and is not exposed on parent routes.
type Assessment struct {
	ID            uuid.UUID `json:"id"`
	StudentID     uuid.UUID `json:"student_id"`
	RecordedBy    uuid.UUID `json:"recorded_by"`
	SessionDate   time.Time `json:"session_date"`
	Score         *float64  `json:"score"`
	Participation *int      `json:"participation"`
	Note          string    `json:"note,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

// ParentView is the subset of an assessment a parent may see: the numbers,
// never the teacher's note.
type ParentView struct {
	SessionDate   time.Time `json:"session_date"`
	Score         *float64  `json:"score"`
	Participation *int      `json:"participation"`
}

func (a Assessment) ParentView() ParentView {
	return ParentView{SessionDate: a.SessionDate, Score: a.Score, Participation: a.Participation}
}

// ValidCATStage matches the schema constraint. Kept here so handlers can
// reject bad input before it reaches Postgres.
func ValidCATStage(stage int) bool { return stage >= 3 && stage <= 5 }
