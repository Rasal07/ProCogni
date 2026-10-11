package stats

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestComputeAggregates(t *testing.T) {
	cases := []struct {
		name              string
		scores            []float64
		mean, median, top float64
		count             int
		ok                bool
	}{
		{name: "empty", scores: nil, ok: false},
		{name: "single", scores: []float64{70}, mean: 70, median: 70, top: 70, count: 1, ok: true},
		{name: "odd count", scores: []float64{50, 90, 70}, mean: 70, median: 70, top: 90, count: 3, ok: true},
		{name: "even count", scores: []float64{60, 80, 70, 90}, mean: 75, median: 75, top: 90, count: 4, ok: true},
		{name: "rounds to two places", scores: []float64{1, 2, 2}, mean: 1.67, median: 2, top: 2, count: 3, ok: true},
		{name: "unsorted input", scores: []float64{91, 12, 55, 78, 33}, mean: 53.8, median: 55, top: 91, count: 5, ok: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := Compute(c.scores)
			if ok != c.ok {
				t.Fatalf("want ok=%v, got %v", c.ok, ok)
			}
			if !ok {
				return
			}
			if got.Mean != c.mean || got.Median != c.median || got.Top != c.top || got.StudentCount != c.count {
				t.Fatalf("want {mean:%v median:%v top:%v n:%d}, got %+v",
					c.mean, c.median, c.top, c.count, got)
			}
		})
	}
}

// Compute must not reorder the caller's slice: the caller is a store handing
// over a scratch buffer.
func TestComputeDoesNotMutateInput(t *testing.T) {
	scores := []float64{90, 10, 50}
	Compute(scores)
	if scores[0] != 90 || scores[1] != 10 || scores[2] != 50 {
		t.Fatalf("Compute sorted its input in place: %v", scores)
	}
}

func TestMonthBucket(t *testing.T) {
	in := time.Date(2026, 9, 20, 17, 45, 3, 99, time.FixedZone("CST", 8*3600))
	got := Month(in)
	want := time.Date(2026, 9, 20, 17, 45, 3, 99, time.FixedZone("CST", 8*3600)).UTC()
	want = time.Date(want.Year(), want.Month(), 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("want %v, got %v", want, got)
	}
	if got.Location() != time.UTC {
		t.Fatalf("period must be UTC, got %v", got.Location())
	}
}

type fakeStore struct {
	scores  []float64
	classes []uuid.UUID
	written []ClassStat
}

func (f *fakeStore) ClassScores(context.Context, uuid.UUID, time.Time) ([]float64, error) {
	return f.scores, nil
}
func (f *fakeStore) ActiveClasses(context.Context, time.Time) ([]uuid.UUID, error) {
	return f.classes, nil
}
func (f *fakeStore) Upsert(_ context.Context, s ClassStat) error {
	f.written = append(f.written, s)
	return nil
}
func (f *fakeStore) Get(context.Context, uuid.UUID, time.Time) (ClassStat, error) {
	return ClassStat{}, ErrNoStats
}

func TestComputerSuppressesSmallCohorts(t *testing.T) {
	store := &fakeStore{scores: []float64{50, 60, 70}}
	c := Computer{Store: store, MinCohort: 5}

	stat, err := c.ComputeClass(context.Background(), uuid.New(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stat.Published() {
		t.Fatalf("a 3-student cohort was published: %+v", stat)
	}
	// The row is still written, so "too small" and "not computed" stay
	// distinguishable downstream.
	if len(store.written) != 1 {
		t.Fatalf("want one row written, got %d", len(store.written))
	}
	if store.written[0].StudentCount != 3 {
		t.Fatalf("want the count recorded, got %d", store.written[0].StudentCount)
	}
	if store.written[0].Mean != nil || store.written[0].Median != nil || store.written[0].Top != nil {
		t.Fatal("suppressed row carries aggregates")
	}
}

func TestComputerPublishesAtThreshold(t *testing.T) {
	store := &fakeStore{scores: []float64{50, 60, 70, 80, 90}}
	c := Computer{Store: store, MinCohort: 5}

	stat, err := c.ComputeClass(context.Background(), uuid.New(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !stat.Published() {
		t.Fatal("a cohort at the threshold was suppressed")
	}
	if *stat.Mean != 70 || *stat.Median != 70 || *stat.Top != 90 {
		t.Fatalf("unexpected aggregates: %+v", stat)
	}
}

func TestComputerHandlesEmptyClass(t *testing.T) {
	store := &fakeStore{}
	c := Computer{Store: store, MinCohort: 5}

	stat, err := c.ComputeClass(context.Background(), uuid.New(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if stat.Published() || stat.StudentCount != 0 {
		t.Fatalf("unexpected result for an empty class: %+v", stat)
	}
}

func TestComputeAllVisitsEveryActiveClass(t *testing.T) {
	store := &fakeStore{
		scores:  []float64{70, 70, 70, 70, 70},
		classes: []uuid.UUID{uuid.New(), uuid.New(), uuid.New()},
	}
	c := Computer{Store: store, MinCohort: 5}

	n, err := c.ComputeAll(context.Background(), time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 || len(store.written) != 3 {
		t.Fatalf("want 3 classes computed, got n=%d written=%d", n, len(store.written))
	}
}
