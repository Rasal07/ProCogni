package stats

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/proleaf/proleaf-api/internal/db"
)

type pgStore struct{ c db.Conn }

func NewStore(c db.Conn) Store { return pgStore{c: c} }

// ClassScores returns each student's mean score for the month. The SELECT list
// holds no student id: grouping by one and projecting only the aggregate is
// what keeps identities out of everything downstream.
func (s pgStore) ClassScores(ctx context.Context, classID uuid.UUID, period time.Time) ([]float64, error) {
	period = Month(period)
	rows, err := s.c.Query(ctx, `
		SELECT AVG(a.score)::float8
		FROM assessments a
		JOIN students st ON st.id = a.student_id
		WHERE st.class_id = $1
		  AND st.archived_at IS NULL
		  AND a.score IS NOT NULL
		  AND a.session_date >= $2
		  AND a.session_date <  ($2::date + INTERVAL '1 month')
		GROUP BY a.student_id`, classID, period)
	if err != nil {
		return nil, fmt.Errorf("stats: class scores: %w", err)
	}
	defer rows.Close()

	var out []float64
	for rows.Next() {
		var v float64
		if err := rows.Scan(&v); err != nil {
			return nil, fmt.Errorf("stats: scan score: %w", err)
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (s pgStore) ActiveClasses(ctx context.Context, period time.Time) ([]uuid.UUID, error) {
	period = Month(period)
	rows, err := s.c.Query(ctx, `
		SELECT DISTINCT st.class_id
		FROM assessments a
		JOIN students st ON st.id = a.student_id
		WHERE st.archived_at IS NULL
		  AND a.session_date >= $1
		  AND a.session_date <  ($1::date + INTERVAL '1 month')`, period)
	if err != nil {
		return nil, fmt.Errorf("stats: active classes: %w", err)
	}
	defer rows.Close()

	var out []uuid.UUID
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("stats: scan class: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (s pgStore) Upsert(ctx context.Context, st ClassStat) error {
	_, err := s.c.Exec(ctx, `
		INSERT INTO class_stats
			(class_id, period, mean_score, median_score, top_score, student_count, computed_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (class_id, period) DO UPDATE SET
			mean_score    = EXCLUDED.mean_score,
			median_score  = EXCLUDED.median_score,
			top_score     = EXCLUDED.top_score,
			student_count = EXCLUDED.student_count,
			computed_at   = EXCLUDED.computed_at`,
		st.ClassID, Month(st.Period), st.Mean, st.Median, st.Top, st.StudentCount, st.ComputedAt)
	if err != nil {
		return fmt.Errorf("stats: upsert: %w", err)
	}
	return nil
}

func (s pgStore) Get(ctx context.Context, classID uuid.UUID, period time.Time) (ClassStat, error) {
	st := ClassStat{}
	err := s.c.QueryRow(ctx, `
		SELECT class_id, period,
		       mean_score::float8, median_score::float8, top_score::float8,
		       student_count, computed_at
		FROM class_stats
		WHERE class_id = $1 AND period = $2`, classID, Month(period)).
		Scan(&st.ClassID, &st.Period, &st.Mean, &st.Median, &st.Top, &st.StudentCount, &st.ComputedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClassStat{}, ErrNoStats
	}
	if err != nil {
		return ClassStat{}, fmt.Errorf("stats: get: %w", err)
	}
	return st, nil
}
