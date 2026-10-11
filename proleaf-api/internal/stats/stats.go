// Package stats computes the class aggregates parents read.
//
// The important property is negative: nothing in a class_stats row identifies
// a student. The computation therefore reduces to a plain []float64 before it
// gets anywhere near the table, and the store deliberately cannot return
// student ids alongside scores.
package stats

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/google/uuid"
)

var ErrNoStats = errors.New("stats: not computed for this class and period")

// ClassStat is one month bucket for one class. Mean, Median and Top are nil
// when the cohort was too small to publish; StudentCount is populated either
// way so a caller can explain the absence.
type ClassStat struct {
	ClassID      uuid.UUID
	Period       time.Time
	Mean         *float64
	Median       *float64
	Top          *float64
	StudentCount int
	ComputedAt   time.Time
}

// Published reports whether this row carries usable aggregates.
func (c ClassStat) Published() bool { return c.Mean != nil }

type Store interface {
	// ClassScores returns one score per student with assessments in the
	// period: the student's mean for the month. It returns bare floats, with
	// no student ids, on purpose.
	ClassScores(ctx context.Context, classID uuid.UUID, period time.Time) ([]float64, error)
	ActiveClasses(ctx context.Context, period time.Time) ([]uuid.UUID, error)
	Upsert(ctx context.Context, s ClassStat) error
	Get(ctx context.Context, classID uuid.UUID, period time.Time) (ClassStat, error)
}

// Reader is the read-only half, which is all the parent package is given.
type Reader interface {
	Get(ctx context.Context, classID uuid.UUID, period time.Time) (ClassStat, error)
}

// Month truncates t to the first day of its month, UTC. Every period value in
// the system goes through here.
func Month(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// Aggregate is the pure part: no context, no store, no clock.
type Aggregate struct {
	Mean         float64
	Median       float64
	Top          float64
	StudentCount int
}

// Compute reduces per-student scores to the published aggregate. The bool is
// false for an empty input; small-cohort suppression is the Computer's call,
// not this function's, so tests can exercise the arithmetic on its own.
func Compute(scores []float64) (Aggregate, bool) {
	if len(scores) == 0 {
		return Aggregate{}, false
	}
	sorted := append([]float64(nil), scores...)
	sort.Float64s(sorted)

	var sum float64
	for _, s := range sorted {
		sum += s
	}
	n := len(sorted)
	median := sorted[n/2]
	if n%2 == 0 {
		median = (sorted[n/2-1] + sorted[n/2]) / 2
	}
	return Aggregate{
		Mean:         round2(sum / float64(n)),
		Median:       round2(median),
		Top:          round2(sorted[n-1]),
		StudentCount: n,
	}, true
}

// round2 matches the NUMERIC(5,2) the column stores, so a value read back
// equals the value computed.
func round2(f float64) float64 { return math.Round(f*100) / 100 }

// Computer runs the job.
type Computer struct {
	Store Store
	// MinCohort suppresses aggregates for classes below this size. A mean over
	// three students is close enough to an individual score to be one.
	MinCohort int
	Now       func() time.Time
}

func (c Computer) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// ComputeClass recomputes one class for one period and writes the row.
func (c Computer) ComputeClass(ctx context.Context, classID uuid.UUID, period time.Time) (ClassStat, error) {
	period = Month(period)
	scores, err := c.Store.ClassScores(ctx, classID, period)
	if err != nil {
		return ClassStat{}, err
	}

	stat := ClassStat{
		ClassID:      classID,
		Period:       period,
		StudentCount: len(scores),
		ComputedAt:   c.now().UTC(),
	}
	if agg, ok := Compute(scores); ok && len(scores) >= c.MinCohort {
		stat.Mean = &agg.Mean
		stat.Median = &agg.Median
		stat.Top = &agg.Top
	}
	// A suppressed row is still written: "computed, too small to publish" and
	// "never computed" are different answers for the parent endpoint to give.
	if err := c.Store.Upsert(ctx, stat); err != nil {
		return ClassStat{}, err
	}
	return stat, nil
}

// ComputeAll recomputes every class with activity in the period.
func (c Computer) ComputeAll(ctx context.Context, period time.Time) (int, error) {
	period = Month(period)
	classes, err := c.Store.ActiveClasses(ctx, period)
	if err != nil {
		return 0, err
	}
	for _, id := range classes {
		if _, err := c.ComputeClass(ctx, id, period); err != nil {
			return 0, err
		}
	}
	return len(classes), nil
}
