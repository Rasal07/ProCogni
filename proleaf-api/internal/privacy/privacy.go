// Package privacy holds the two obligations the skeleton is asked to scaffold
// rather than finish: retention/deletion, and data-subject requests for
// access, correction and deletion.
//
// The request intake is real, because a request that arrives before the
// workflow exists still has to be recorded with its timestamp. The fulfilment
// side is deliberately not implemented: a half-working deleter is worse than
// an obvious gap, and the retention policy itself is still open.
package privacy

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
)

// Request kinds, matching the subject_requests check constraint.
const (
	KindAccess     = "access"
	KindCorrection = "correction"
	KindDeletion   = "deletion"
)

const (
	StatusReceived   = "received"
	StatusInProgress = "in_progress"
	StatusFulfilled  = "fulfilled"
	StatusRefused    = "refused"
)

var (
	ErrUnknownKind    = errors.New("privacy: unknown request kind")
	ErrRequestUnknown = errors.New("privacy: no such request")
	// ErrNotImplemented marks the parts this phase does not build.
	ErrNotImplemented = errors.New("privacy: not implemented in the skeleton")
)

func ValidKind(kind string) bool {
	switch kind {
	case KindAccess, KindCorrection, KindDeletion:
		return true
	}
	return false
}

type Request struct {
	ID         uuid.UUID  `json:"id"`
	AccountID  uuid.UUID  `json:"account_id"`
	StudentID  *uuid.UUID `json:"student_id,omitempty"`
	Kind       string     `json:"kind"`
	Status     string     `json:"status"`
	Detail     string     `json:"detail,omitempty"`
	CreatedAt  time.Time  `json:"created_at"`
	ResolvedAt *time.Time `json:"resolved_at,omitempty"`
}

type Store interface {
	CreateRequest(ctx context.Context, r Request) error
	ListRequests(ctx context.Context, status string, limit int) ([]Request, error)
	SetRequestStatus(ctx context.Context, id uuid.UUID, status string, at time.Time) error

	// RetentionCandidates counts what a retention run would touch, without
	// touching it.
	RetentionCandidates(ctx context.Context, before time.Time) (Candidates, error)
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

// Submit records a data-subject request. The account and student are supplied
// by the caller after its own authorization check; this package does not do
// authorization.
func (s Service) Submit(ctx context.Context, accountID uuid.UUID, studentID *uuid.UUID, kind, detail string) (Request, error) {
	if !ValidKind(kind) {
		return Request{}, ErrUnknownKind
	}
	r := Request{
		ID:        uuid.New(),
		AccountID: accountID,
		StudentID: studentID,
		Kind:      kind,
		Status:    StatusReceived,
		Detail:    detail,
		CreatedAt: s.now().UTC(),
	}
	if err := s.Store.CreateRequest(ctx, r); err != nil {
		return Request{}, err
	}
	return r, nil
}

func (s Service) List(ctx context.Context, status string, limit int) ([]Request, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	return s.Store.ListRequests(ctx, status, limit)
}

// Fulfil is the scaffold. Exporting, correcting or erasing a student's record
// touches assessments, consent_events and the audit log, and each has a
// different answer: the audit log in particular is append-only and is retained
// against its own schedule rather than erased on request. That decision is not
// made, so this does not guess at it.
func (s Service) Fulfil(context.Context, uuid.UUID) error {
	return ErrNotImplemented
}

// Candidates is what a retention run would act on.
type Candidates struct {
	Assessments     int       `json:"assessments"`
	Students        int       `json:"students"`
	OTPChallenges   int       `json:"otp_challenges"`
	EnrollmentCodes int       `json:"enrollment_codes"`
	Before          time.Time `json:"before"`
}

// Retention is the deletion job. Run reports; it does not delete.
//
// The academic-year retention period is not agreed, and deleting assessment
// history is not reversible. The job therefore ships as a dry run that makes
// the size of the problem visible, and Apply is the piece to write once the
// period is decided.
type Retention struct {
	Store  Store
	Period time.Duration
	Now    func() time.Time
}

func (r Retention) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r Retention) DryRun(ctx context.Context) (Candidates, error) {
	if r.Period <= 0 {
		return Candidates{}, errors.New("privacy: retention period is not set")
	}
	return r.Store.RetentionCandidates(ctx, r.now().UTC().Add(-r.Period))
}

func (r Retention) Apply(context.Context) error { return ErrNotImplemented }
