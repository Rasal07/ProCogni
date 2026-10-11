// Package intelligence defines the boundary between ProLeaf and any future
// suggestion provider. It contains an interface and a no-op implementation and
// nothing else: there is no provider integration in the skeleton, and this
// package is called from nowhere.
//
// It exists now so that the pseudonymization boundary is in the architecture
// before the first call site. Context is the only thing that crosses it, and
// Context cannot carry an identity: no names, no ids, no free text. The
// mapping from a Context back to a real student happens inside our service.
//
// The naming is the other half of the point. A suggestion goes to a teacher,
// who decides what to do with it. It is not a verdict on a student, and the
// MOE constraint that makes that distinction is encoded here as types:
// Suggestion, not Evaluation; Audience, not Subject.
package intelligence

import (
	"context"
	"errors"
)

// Trend is a coarse direction, not a computed slope. Coarseness is deliberate:
// a precise number is re-identifying when combined with a small cohort.
type Trend string

const (
	TrendImproving Trend = "improving"
	TrendSteady    Trend = "steady"
	TrendSlipping  Trend = "slipping"
	TrendUnknown   Trend = "unknown"
)

// Context is everything a provider is ever given.
//
// Adding a field here is the moment to stop and ask whether it re-identifies a
// student. Names, ids, dates of birth, class names, teacher names and the
// free-text note on an assessment are all out, permanently.
type Context struct {
	CATStage     int
	SessionCount int
	RecentTrend  Trend
	FocusAreas   []string
}

// Audience is who the suggestion is for. There is no Subject field, and that
// is not an omission.
type Audience string

const AudienceTeacher Audience = "teacher"

// Suggestion is advice offered to a teacher. It is not a score, a grade, a
// ranking or a prediction about a child.
type Suggestion struct {
	Audience Audience
	// Activities are things a teacher might do next.
	Activities []string
	// Rationale is the reasoning shown to the teacher, in their language.
	Rationale string
	// Provider names what produced this, for the audit trail.
	Provider string
}

type Provider interface {
	Suggest(ctx context.Context, c Context) (Suggestion, error)
}

// ErrNotConfigured is what the no-op provider returns. Callers are expected to
// treat it as "no suggestion available" and carry on, never as a failure of
// the request they are serving.
var ErrNotConfigured = errors.New("intelligence: no provider configured")

// NoOp is the only implementation in the skeleton.
type NoOp struct{}

func (NoOp) Suggest(context.Context, Context) (Suggestion, error) {
	return Suggestion{}, ErrNotConfigured
}

// Compile-time assertion that the no-op satisfies the interface. This is the
// whole of the skeleton's obligation for section 7.
var _ Provider = NoOp{}
