package consent

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memStore struct{ events []Event }

func (m *memStore) Append(_ context.Context, e Event) error {
	m.events = append(m.events, e)
	return nil
}

func (m *memStore) Latest(_ context.Context, accountID, studentID uuid.UUID, scope string) (Event, error) {
	var best Event
	found := false
	for _, e := range m.events {
		if e.AccountID != accountID || e.StudentID != studentID || e.Scope != scope {
			continue
		}
		if !found || e.OccurredAt.After(best.OccurredAt) ||
			(e.OccurredAt.Equal(best.OccurredAt) && !e.Granted) {
			best, found = e, true
		}
	}
	if !found {
		return Event{}, ErrNoConsent
	}
	return best, nil
}

func newService(store *memStore, now time.Time) Service {
	return Service{Store: store, PolicyVersion: "v2", Now: func() time.Time { return now }}
}

func TestCheckRequiresAnEvent(t *testing.T) {
	s := newService(&memStore{}, time.Now())
	err := s.Check(context.Background(), uuid.New(), uuid.New(), ScopeProgress)
	if !errors.Is(err, ErrNoConsent) {
		t.Fatalf("absent consent must fail closed, got %v", err)
	}
}

func TestCheckHonoursTheLatestEvent(t *testing.T) {
	store := &memStore{}
	now := time.Date(2026, 9, 20, 12, 0, 0, 0, time.UTC)
	account, student := uuid.New(), uuid.New()

	s := newService(store, now)
	if err := s.Record(context.Background(), account, student, ScopeProgress, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Check(context.Background(), account, student, ScopeProgress); err != nil {
		t.Fatalf("a fresh grant should pass: %v", err)
	}

	// Withdrawal, one second later.
	later := newService(store, now.Add(time.Second))
	if err := later.Record(context.Background(), account, student, ScopeProgress, false); err != nil {
		t.Fatal(err)
	}
	if err := later.Check(context.Background(), account, student, ScopeProgress); !errors.Is(err, ErrNotGranted) {
		t.Fatalf("want ErrNotGranted after withdrawal, got %v", err)
	}

	// And a re-grant after that.
	latest := newService(store, now.Add(2*time.Second))
	if err := latest.Record(context.Background(), account, student, ScopeProgress, true); err != nil {
		t.Fatal(err)
	}
	if err := latest.Check(context.Background(), account, student, ScopeProgress); err != nil {
		t.Fatalf("a re-grant should pass: %v", err)
	}

	// Every decision is still on the record; nothing was overwritten.
	if len(store.events) != 3 {
		t.Fatalf("want 3 events retained, got %d", len(store.events))
	}
}

func TestCheckRejectsSupersededPolicy(t *testing.T) {
	store := &memStore{}
	account, student := uuid.New(), uuid.New()

	old := Service{Store: store, PolicyVersion: "v1"}
	if err := old.Record(context.Background(), account, student, ScopeProgress, true); err != nil {
		t.Fatal(err)
	}

	current := Service{Store: store, PolicyVersion: "v2"}
	if err := current.Check(context.Background(), account, student, ScopeProgress); !errors.Is(err, ErrPolicyOutdated) {
		t.Fatalf("want ErrPolicyOutdated, got %v", err)
	}
}

func TestScopesAreIndependent(t *testing.T) {
	store := &memStore{}
	s := newService(store, time.Now())
	account, student := uuid.New(), uuid.New()

	if err := s.RecordAll(context.Background(), account, student, BindScopes, true); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(context.Background(), account, student, ScopeComparison, false); err != nil {
		t.Fatal(err)
	}

	if err := s.Check(context.Background(), account, student, ScopeProgress); err != nil {
		t.Fatalf("progress should be unaffected: %v", err)
	}
	if err := s.Check(context.Background(), account, student, ScopeComparison); err == nil {
		t.Fatal("comparison should be refused")
	}
}

func TestStatusForReportsEveryScope(t *testing.T) {
	store := &memStore{}
	s := newService(store, time.Now())
	account, student := uuid.New(), uuid.New()

	if err := s.Record(context.Background(), account, student, ScopeProgress, true); err != nil {
		t.Fatal(err)
	}

	status, err := s.StatusFor(context.Background(), account, student, BindScopes)
	if err != nil {
		t.Fatal(err)
	}
	if len(status) != len(BindScopes) {
		t.Fatalf("want a row per scope, got %d", len(status))
	}
	for _, st := range status {
		switch st.Scope {
		case ScopeProgress:
			if !st.Granted || !st.Current {
				t.Errorf("progress: want granted and current, got %+v", st)
			}
		case ScopeComparison:
			if st.Granted || st.Current {
				t.Errorf("comparison: want nothing recorded, got %+v", st)
			}
		}
	}
}

// A refusal is a recorded event, not an absence.
func TestRefusalIsRecorded(t *testing.T) {
	store := &memStore{}
	s := newService(store, time.Now())

	if err := s.Record(context.Background(), uuid.New(), uuid.New(), ScopeComparison, false); err != nil {
		t.Fatal(err)
	}
	if len(store.events) != 1 {
		t.Fatalf("want the refusal stored, got %d events", len(store.events))
	}
	if store.events[0].Granted {
		t.Fatal("the refusal was stored as a grant")
	}
}
