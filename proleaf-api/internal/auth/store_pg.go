package auth

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proleaf/proleaf-api/internal/db"
)

type pgStore struct{ c db.Conn }

// NewStore returns the Postgres-backed auth store.
func NewStore(c db.Conn) Store { return pgStore{c: c} }

func (s pgStore) InsertChallenge(ctx context.Context, ch Challenge) error {
	_, err := s.c.Exec(ctx, `
		INSERT INTO otp_challenges (id, phone, code_hash, issued_at, expires_at)
		VALUES ($1, $2, $3, $4, $5)`,
		ch.ID, ch.Phone, ch.CodeHash, ch.IssuedAt, ch.ExpiresAt)
	if err != nil {
		return fmt.Errorf("auth: insert challenge: %w", err)
	}
	return nil
}

func (s pgStore) LatestChallenge(ctx context.Context, phone string) (Challenge, error) {
	var ch Challenge
	err := s.c.QueryRow(ctx, `
		SELECT id, phone, code_hash, issued_at, expires_at, attempts, consumed_at
		FROM otp_challenges
		WHERE phone = $1
		ORDER BY issued_at DESC
		LIMIT 1`, phone).
		Scan(&ch.ID, &ch.Phone, &ch.CodeHash, &ch.IssuedAt, &ch.ExpiresAt, &ch.Attempts, &ch.ConsumedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Challenge{}, ErrNoChallenge
	}
	if err != nil {
		return Challenge{}, fmt.Errorf("auth: latest challenge: %w", err)
	}
	return ch, nil
}

func (s pgStore) RecordAttempt(ctx context.Context, id uuid.UUID) (int, error) {
	var attempts int
	err := s.c.QueryRow(ctx, `
		UPDATE otp_challenges SET attempts = attempts + 1
		WHERE id = $1
		RETURNING attempts`, id).Scan(&attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNoChallenge
	}
	if err != nil {
		return 0, fmt.Errorf("auth: record attempt: %w", err)
	}
	return attempts, nil
}

func (s pgStore) ConsumeChallenge(ctx context.Context, id uuid.UUID) error {
	tag, err := s.c.Exec(ctx, `
		UPDATE otp_challenges SET consumed_at = now()
		WHERE id = $1 AND consumed_at IS NULL`, id)
	if err != nil {
		return fmt.Errorf("auth: consume challenge: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Lost a race with a concurrent verify of the same code.
		return ErrNoChallenge
	}
	return nil
}

const accountColumns = `id, phone, role, COALESCE(display_name, ''), disabled_at`

func (s pgStore) AccountByPhone(ctx context.Context, phone string) (Account, error) {
	return scanAccount(s.c.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE phone = $1`, phone))
}

func (s pgStore) AccountByID(ctx context.Context, id uuid.UUID) (Account, error) {
	return scanAccount(s.c.QueryRow(ctx,
		`SELECT `+accountColumns+` FROM accounts WHERE id = $1`, id))
}

func (s pgStore) CreateAccount(ctx context.Context, phone string, role Role, displayName string) (Account, error) {
	if !role.Valid() {
		return Account{}, fmt.Errorf("auth: invalid role %q", role)
	}
	// ON CONFLICT covers two devices verifying the same new number at once;
	// it never changes an existing role.
	return scanAccount(s.c.QueryRow(ctx, `
		INSERT INTO accounts (phone, role, display_name)
		VALUES ($1, $2, NULLIF($3, ''))
		ON CONFLICT (phone) DO UPDATE SET phone = EXCLUDED.phone
		RETURNING `+accountColumns, phone, string(role), displayName))
}

func scanAccount(row pgx.Row) (Account, error) {
	var a Account
	var role string
	err := row.Scan(&a.ID, &a.Phone, &role, &a.DisplayName, &a.DisabledAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Account{}, ErrAccountNotFound
	}
	if err != nil {
		return Account{}, fmt.Errorf("auth: scan account: %w", err)
	}
	a.Role = Role(role)
	return a, nil
}
