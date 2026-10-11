package consent

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proleaf/proleaf-api/internal/db"
)

type pgStore struct{ c db.Conn }

func NewStore(c db.Conn) Store { return pgStore{c: c} }

func (s pgStore) Append(ctx context.Context, e Event) error {
	_, err := s.c.Exec(ctx, `
		INSERT INTO consent_events
			(id, account_id, student_id, policy_version, scope, granted, occurred_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.ID, e.AccountID, e.StudentID, e.PolicyVersion, e.Scope, e.Granted, e.OccurredAt)
	if err != nil {
		return fmt.Errorf("consent: append: %w", err)
	}
	return nil
}

func (s pgStore) Latest(ctx context.Context, accountID, studentID uuid.UUID, scope string) (Event, error) {
	var e Event
	err := s.c.QueryRow(ctx, `
		SELECT id, account_id, student_id, policy_version, scope, granted, occurred_at
		FROM consent_events
		WHERE account_id = $1 AND student_id = $2 AND scope = $3
		-- granted ASC puts a refusal ahead of a grant at the same instant.
		ORDER BY occurred_at DESC, granted ASC, id DESC
		LIMIT 1`, accountID, studentID, scope).
		Scan(&e.ID, &e.AccountID, &e.StudentID, &e.PolicyVersion, &e.Scope, &e.Granted, &e.OccurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Event{}, ErrNoConsent
	}
	if err != nil {
		return Event{}, fmt.Errorf("consent: latest: %w", err)
	}
	return e, nil
}
