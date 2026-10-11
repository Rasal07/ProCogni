package privacy

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/db"
)

type pgStore struct{ c db.Conn }

func NewStore(c db.Conn) Store { return pgStore{c: c} }

func (s pgStore) CreateRequest(ctx context.Context, r Request) error {
	_, err := s.c.Exec(ctx, `
		INSERT INTO subject_requests (id, account_id, student_id, kind, status, detail, created_at)
		VALUES ($1, $2, $3, $4, $5, NULLIF($6, ''), $7)`,
		r.ID, r.AccountID, r.StudentID, r.Kind, r.Status, r.Detail, r.CreatedAt)
	if err != nil {
		return fmt.Errorf("privacy: create request: %w", err)
	}
	return nil
}

func (s pgStore) ListRequests(ctx context.Context, status string, limit int) ([]Request, error) {
	rows, err := s.c.Query(ctx, `
		SELECT id, account_id, student_id, kind, status, COALESCE(detail, ''), created_at, resolved_at
		FROM subject_requests
		WHERE ($1::text IS NULL OR status = $1)
		ORDER BY created_at DESC
		LIMIT $2`, nullString(status), limit)
	if err != nil {
		return nil, fmt.Errorf("privacy: list requests: %w", err)
	}
	defer rows.Close()

	out := []Request{}
	for rows.Next() {
		var r Request
		if err := rows.Scan(&r.ID, &r.AccountID, &r.StudentID, &r.Kind, &r.Status,
			&r.Detail, &r.CreatedAt, &r.ResolvedAt); err != nil {
			return nil, fmt.Errorf("privacy: scan request: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s pgStore) SetRequestStatus(ctx context.Context, id uuid.UUID, status string, at time.Time) error {
	resolved := status == StatusFulfilled || status == StatusRefused
	tag, err := s.c.Exec(ctx, `
		UPDATE subject_requests
		SET status = $2, resolved_at = CASE WHEN $3 THEN $4 ELSE NULL END
		WHERE id = $1`, id, status, resolved, at)
	if err != nil {
		return fmt.Errorf("privacy: set status: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrRequestUnknown
	}
	return nil
}

func (s pgStore) RetentionCandidates(ctx context.Context, before time.Time) (Candidates, error) {
	c := Candidates{Before: before}
	err := s.c.QueryRow(ctx, `
		SELECT
			(SELECT COUNT(*) FROM assessments       WHERE created_at < $1),
			(SELECT COUNT(*) FROM students          WHERE archived_at IS NOT NULL AND archived_at < $1),
			(SELECT COUNT(*) FROM otp_challenges    WHERE expires_at < $1),
			(SELECT COUNT(*) FROM enrollment_codes  WHERE expires_at < $1)`, before).
		Scan(&c.Assessments, &c.Students, &c.OTPChallenges, &c.EnrollmentCodes)
	if err != nil {
		return Candidates{}, fmt.Errorf("privacy: retention candidates: %w", err)
	}
	return c, nil
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
