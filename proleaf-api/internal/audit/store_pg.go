package audit

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/db"
)

type pgSink struct{ c db.Conn }

// NewSink returns the Postgres-backed append-only sink.
func NewSink(c db.Conn) Sink { return pgSink{c: c} }

func (s pgSink) Append(ctx context.Context, entries []Entry) error {
	if len(entries) == 0 {
		return nil
	}
	// One statement per request-batch. INSERT only: there is no update path in
	// this package, and migration 0002 makes that true at the database too.
	for _, e := range entries {
		if _, err := s.c.Exec(ctx, `
			INSERT INTO audit_log
				(actor_account, actor_role, action, subject_student, request_id, stream, occurred_at)
			VALUES ($1, $2, $3, $4, $5, $6, $7)`,
			e.ActorAccount, e.ActorRole, e.Action, e.SubjectStudent, e.RequestID, e.Stream, e.OccurredAt,
		); err != nil {
			return fmt.Errorf("audit: append: %w", err)
		}
	}
	return nil
}

// Filter narrows a read of the log. Admin-only; see internal/admin.
type Filter struct {
	Stream         string
	ActorAccount   *uuid.UUID
	SubjectStudent *uuid.UUID
	Since          *time.Time
	Until          *time.Time
	Limit          int
}

// Reader reads the log back. Separate from Sink so that nothing holding a Sink
// can accidentally be given read access to the whole log.
type Reader interface {
	Query(ctx context.Context, f Filter) ([]Entry, error)
}

type pgReader struct{ c db.Conn }

func NewReader(c db.Conn) Reader { return pgReader{c: c} }

func (s pgReader) Query(ctx context.Context, f Filter) ([]Entry, error) {
	if f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 200
	}
	rows, err := s.c.Query(ctx, `
		SELECT actor_account, actor_role, action, subject_student, request_id, stream, occurred_at
		FROM audit_log
		WHERE ($1::text IS NULL OR stream = $1)
		  AND ($2::uuid IS NULL OR actor_account = $2)
		  AND ($3::uuid IS NULL OR subject_student = $3)
		  AND ($4::timestamptz IS NULL OR occurred_at >= $4)
		  AND ($5::timestamptz IS NULL OR occurred_at < $5)
		ORDER BY occurred_at DESC, id DESC
		LIMIT $6`,
		nullString(f.Stream), f.ActorAccount, f.SubjectStudent, f.Since, f.Until, f.Limit)
	if err != nil {
		return nil, fmt.Errorf("audit: query: %w", err)
	}
	defer rows.Close()

	out := []Entry{} // never null in the response
	for rows.Next() {
		var e Entry
		if err := rows.Scan(&e.ActorAccount, &e.ActorRole, &e.Action,
			&e.SubjectStudent, &e.RequestID, &e.Stream, &e.OccurredAt); err != nil {
			return nil, fmt.Errorf("audit: scan: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func nullString(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
