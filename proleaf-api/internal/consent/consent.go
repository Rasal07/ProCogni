// Package consent records consent as a sequence of events and answers the one
// question the rest of the service asks: may this account see this student's
// data, under the policy currently in force?
//
// A refusal and a withdrawal are recorded events too. There is no boolean
// anywhere in this package.
package consent

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Scopes. Keep them coarse enough that a parent can understand what they are
// agreeing to, and fine enough that withdrawing one does not withdraw all.
const (
	// ScopeProgress covers the student's own assessment records.
	ScopeProgress = "progress"
	// ScopeComparison covers showing the student's score against class
	// aggregates. Separable because some guardians object to ranking while
	// accepting progress reporting.
	ScopeComparison = "comparison"
)

// BindScopes are granted together at /parent/bind. Adding a scope here means
// existing consents no longer cover it, which is the intended behaviour: a new
// use of the data needs a new act of consent.
var BindScopes = []string{ScopeProgress, ScopeComparison}

var (
	ErrNoConsent      = errors.New("consent: no event recorded")
	ErrNotGranted     = errors.New("consent: not granted")
	ErrPolicyOutdated = errors.New("consent: granted under a superseded policy version")
)

type Event struct {
	ID            uuid.UUID
	AccountID     uuid.UUID
	StudentID     uuid.UUID
	PolicyVersion string
	Scope         string
	Granted       bool
	OccurredAt    time.Time
}

type Store interface {
	Append(ctx context.Context, e Event) error
	// Latest returns the most recent event for the triple, or ErrNoConsent.
	//
	// Where two events share a timestamp — a coarse clock, or two decisions
	// recorded in one request — a refusal wins. The tie has to break somewhere
	// and the safe side is the one that withholds data.
	Latest(ctx context.Context, accountID, studentID uuid.UUID, scope string) (Event, error)
}

// Service is the check every parent-facing student-data handler runs before
// returning anything.
type Service struct {
	Store         Store
	PolicyVersion string
	Now           func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Record appends one event. Granting and refusing use the same path, because
// they are the same kind of fact.
func (s Service) Record(ctx context.Context, accountID, studentID uuid.UUID, scope string, granted bool) error {
	return s.Store.Append(ctx, Event{
		ID:            uuid.New(),
		AccountID:     accountID,
		StudentID:     studentID,
		PolicyVersion: s.PolicyVersion,
		Scope:         scope,
		Granted:       granted,
		OccurredAt:    s.now().UTC(),
	})
}

// RecordAll appends one event per scope, all with the same decision.
func (s Service) RecordAll(ctx context.Context, accountID, studentID uuid.UUID, scopes []string, granted bool) error {
	for _, scope := range scopes {
		if err := s.Record(ctx, accountID, studentID, scope, granted); err != nil {
			return err
		}
	}
	return nil
}

// Check returns nil only when the latest event for the scope is a grant under
// the current policy version. Every other state, including a grant under a
// superseded policy, is a refusal to serve the data.
func (s Service) Check(ctx context.Context, accountID, studentID uuid.UUID, scope string) error {
	e, err := s.Store.Latest(ctx, accountID, studentID, scope)
	if err != nil {
		return err
	}
	if !e.Granted {
		return ErrNotGranted
	}
	if e.PolicyVersion != s.PolicyVersion {
		return ErrPolicyOutdated
	}
	return nil
}

// Status is the read-back a client needs to know what to prompt for.
type Status struct {
	Scope         string    `json:"scope"`
	Granted       bool      `json:"granted"`
	Current       bool      `json:"current"`
	PolicyVersion string    `json:"policy_version"`
	OccurredAt    time.Time `json:"occurred_at"`
}

func (s Service) StatusFor(ctx context.Context, accountID, studentID uuid.UUID, scopes []string) ([]Status, error) {
	out := make([]Status, 0, len(scopes))
	for _, scope := range scopes {
		e, err := s.Store.Latest(ctx, accountID, studentID, scope)
		if errors.Is(err, ErrNoConsent) {
			out = append(out, Status{Scope: scope})
			continue
		}
		if err != nil {
			return nil, err
		}
		out = append(out, Status{
			Scope:         scope,
			Granted:       e.Granted,
			Current:       e.Granted && e.PolicyVersion == s.PolicyVersion,
			PolicyVersion: e.PolicyVersion,
			OccurredAt:    e.OccurredAt,
		})
	}
	return out, nil
}
