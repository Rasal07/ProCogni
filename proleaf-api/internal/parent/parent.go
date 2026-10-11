// Package parent serves the guardian-facing API.
//
// The rule this package exists to enforce: a parent reaches student data only
// through a live row in guardian_bindings, resolved server-side on every
// request. Every Store method therefore takes the account id, so it is not
// possible to write a query here that forgets the binding. The comparison
// endpoint reads class_stats and the bound student's own rows, and nothing
// else.
//
// This package must not import internal/teacher.
package parent

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/consent"
	"github.com/proleaf/proleaf-api/internal/enroll"
	"github.com/proleaf/proleaf-api/internal/stats"
	"github.com/proleaf/proleaf-api/internal/student"
)

var (
	// ErrNotBound covers both "no such student" and "not your student". The
	// caller turns it into a 404 without distinguishing the two, because
	// confirming that a student id exists is itself a disclosure.
	ErrNotBound = errors.New("parent: no live binding")

	ErrCodeNotUsable = errors.New("parent: enrollment code is not usable")
	ErrAlreadyBound  = errors.New("parent: already bound to this student")
)

// Store is the whole of this package's database surface.
//
// Every method takes accountID and scopes its query by a live guardian_binding
// row. That is not redundant with the service-level check above it: it means a
// future handler added in a hurry still cannot read another family's data.
type Store interface {
	BoundStudent(ctx context.Context, accountID, studentID uuid.UUID) (student.Student, error)
	BoundStudents(ctx context.Context, accountID uuid.UUID) ([]student.Student, error)
	BoundAssessments(ctx context.Context, accountID, studentID uuid.UUID, limit int) ([]student.Assessment, error)
	// BoundPeriodScore returns the student's own mean score and session count
	// for one month bucket.
	BoundPeriodScore(ctx context.Context, accountID, studentID uuid.UUID, period time.Time) (*float64, int, error)
	// Bind consumes the enrollment code and creates the binding in one
	// transaction, returning the student it bound to.
	Bind(ctx context.Context, accountID uuid.UUID, codeHash string, now time.Time) (student.Student, error)
}

type Service struct {
	Store   Store
	Stats   stats.Reader
	Consent consent.Service
	// CodePepper keys the enrollment-code hash; see internal/enroll.
	CodePepper []byte
	Now        func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// ResolveBoundStudent is the authorization boundary. Nothing in this package
// reads student data without going through it first.
func (s Service) ResolveBoundStudent(ctx context.Context, accountID, requestedStudentID uuid.UUID) (student.Student, error) {
	st, err := s.Store.BoundStudent(ctx, accountID, requestedStudentID)
	if err != nil {
		return student.Student{}, err // ErrNotBound, or a real failure
	}
	if st.Archived() {
		return student.Student{}, ErrNotBound
	}
	return st, nil
}

// resolveWithConsent adds the consent gate. Both checks, in this order, guard
// every data read below.
func (s Service) resolveWithConsent(ctx context.Context, accountID, studentID uuid.UUID, scope string) (student.Student, error) {
	st, err := s.ResolveBoundStudent(ctx, accountID, studentID)
	if err != nil {
		return student.Student{}, err
	}
	if err := s.Consent.Check(ctx, accountID, st.ID, scope); err != nil {
		return student.Student{}, err
	}
	return st, nil
}

// Children lists the students this account is bound to. No consent gate: the
// list of one's own children, by name, is the thing consent is given about,
// and a parent who has withdrawn consent still needs to see the entry in order
// to grant it again.
func (s Service) Children(ctx context.Context, accountID uuid.UUID) ([]student.Student, error) {
	return s.Store.BoundStudents(ctx, accountID)
}

// Progress returns the student's own assessment history, numbers only.
func (s Service) Progress(ctx context.Context, accountID, studentID uuid.UUID, limit int) ([]student.ParentView, error) {
	st, err := s.resolveWithConsent(ctx, accountID, studentID, consent.ScopeProgress)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 60
	}
	records, err := s.Store.BoundAssessments(ctx, accountID, st.ID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]student.ParentView, 0, len(records))
	for _, a := range records {
		// Drops recorded_by and the teacher's note.
		out = append(out, a.ParentView())
	}
	return out, nil
}

// ClassBand is the aggregate half of a comparison. It carries no student
// identifiers, by construction: it is built from a class_stats row, and that
// table has no column that could supply one.
type ClassBand struct {
	Mean         *float64 `json:"mean_score"`
	Median       *float64 `json:"median_score"`
	Top          *float64 `json:"top_score"`
	StudentCount int      `json:"student_count"`
}

// Comparison is the response body of /parent/children/{id}/comparison.
//
// It deliberately has no student id, no class id, no name and no teacher
// field. If you are about to add one, that is the change §4 of the skeleton
// guide is asking you not to make.
type Comparison struct {
	Period       string     `json:"period"`
	OwnScore     *float64   `json:"own_score"`
	SessionCount int        `json:"session_count"`
	Class        *ClassBand `json:"class"`
	// Unavailable explains a nil Class: "not_computed" or "cohort_too_small".
	Unavailable string `json:"unavailable,omitempty"`
}

func (s Service) Comparison(ctx context.Context, accountID, studentID uuid.UUID, period time.Time) (Comparison, error) {
	st, err := s.resolveWithConsent(ctx, accountID, studentID, consent.ScopeComparison)
	if err != nil {
		return Comparison{}, err
	}
	period = stats.Month(period)

	own, sessions, err := s.Store.BoundPeriodScore(ctx, accountID, st.ID, period)
	if err != nil {
		return Comparison{}, err
	}
	out := Comparison{
		Period:       period.Format("2006-01"),
		OwnScore:     own,
		SessionCount: sessions,
	}

	cs, err := s.Stats.Get(ctx, st.ClassID, period)
	switch {
	case errors.Is(err, stats.ErrNoStats):
		out.Unavailable = "not_computed"
		return out, nil
	case err != nil:
		return Comparison{}, err
	case !cs.Published():
		out.Unavailable = "cohort_too_small"
		return out, nil
	}
	out.Class = &ClassBand{
		Mean:         cs.Mean,
		Median:       cs.Median,
		Top:          cs.Top,
		StudentCount: cs.StudentCount,
	}
	return out, nil
}

// BindResult is what a successful /parent/bind returns.
type BindResult struct {
	Student student.Student  `json:"student"`
	Consent []consent.Status `json:"consent"`
}

// Bind consumes an enrollment code and records the consent decision that came
// with it. The binding and the consent events are the same act, so a bind that
// cannot record consent is not a bind.
func (s Service) Bind(ctx context.Context, accountID uuid.UUID, rawCode string, acceptedScopes []string) (BindResult, error) {
	codeHash, err := enroll.HashRaw(s.CodePepper, rawCode)
	if err != nil {
		return BindResult{}, ErrCodeNotUsable
	}
	st, err := s.Store.Bind(ctx, accountID, codeHash, s.now().UTC())
	if err != nil {
		return BindResult{}, err
	}

	accepted := map[string]bool{}
	for _, sc := range acceptedScopes {
		accepted[sc] = true
	}
	for _, scope := range consent.BindScopes {
		// A scope the parent declined is recorded as a refusal, not left
		// absent: "they said no" and "we never asked" are different facts.
		if err := s.Consent.Record(ctx, accountID, st.ID, scope, accepted[scope]); err != nil {
			return BindResult{}, err
		}
	}
	status, err := s.Consent.StatusFor(ctx, accountID, st.ID, consent.BindScopes)
	if err != nil {
		return BindResult{}, err
	}
	return BindResult{Student: st, Consent: status}, nil
}

// SetConsent records a later change of mind for one scope.
func (s Service) SetConsent(ctx context.Context, accountID, studentID uuid.UUID, scope string, granted bool) ([]consent.Status, error) {
	st, err := s.ResolveBoundStudent(ctx, accountID, studentID)
	if err != nil {
		return nil, err
	}
	if err := s.Consent.Record(ctx, accountID, st.ID, scope, granted); err != nil {
		return nil, err
	}
	return s.Consent.StatusFor(ctx, accountID, st.ID, consent.BindScopes)
}

// ConsentStatus reports the current decisions for a bound student.
func (s Service) ConsentStatus(ctx context.Context, accountID, studentID uuid.UUID) ([]consent.Status, error) {
	st, err := s.ResolveBoundStudent(ctx, accountID, studentID)
	if err != nil {
		return nil, err
	}
	return s.Consent.StatusFor(ctx, accountID, st.ID, consent.BindScopes)
}
