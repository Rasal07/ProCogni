// Command statsjob recomputes class_stats for a month bucket.
//
// It runs as a cron job rather than on the request path: the parent comparison
// endpoint reads a precomputed row and never aggregates live, which is what
// keeps a parent request from touching another student's assessments at all.
//
//	statsjob                 # current month
//	statsjob -period 2026-08 # a specific month
//	statsjob -retention-dry-run
package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"time"

	"github.com/proleaf/proleaf-api/internal/config"
	"github.com/proleaf/proleaf-api/internal/db"
	"github.com/proleaf/proleaf-api/internal/privacy"
	"github.com/proleaf/proleaf-api/internal/stats"
)

func main() {
	var (
		periodFlag    = flag.String("period", "", "month bucket to compute, YYYY-MM (default: current month)")
		retentionOnly = flag.Bool("retention-dry-run", false, "report retention candidates instead of computing stats")
		retentionDays = flag.Int("retention-days", 1095, "retention period in days for the dry run")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))

	if err := run(*periodFlag, *retentionOnly, *retentionDays); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(periodFlag string, retentionOnly bool, retentionDays int) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	if retentionOnly {
		r := privacy.Retention{
			Store:  privacy.NewStore(database.Conn()),
			Period: time.Duration(retentionDays) * 24 * time.Hour,
		}
		candidates, err := r.DryRun(ctx)
		if err != nil {
			return err
		}
		slog.Info("retention dry run (nothing deleted)",
			"before", candidates.Before,
			"assessments", candidates.Assessments,
			"archived_students", candidates.Students,
			"otp_challenges", candidates.OTPChallenges,
			"enrollment_codes", candidates.EnrollmentCodes)
		return nil
	}

	period := time.Now()
	if periodFlag != "" {
		if period, err = time.Parse("2006-01", periodFlag); err != nil {
			return err
		}
	}
	period = stats.Month(period)

	computer := stats.Computer{
		Store:     stats.NewStore(database.Conn()),
		MinCohort: cfg.StatsMinCohort,
	}
	n, err := computer.ComputeAll(ctx, period)
	if err != nil {
		return err
	}
	slog.Info("class stats computed",
		"period", period.Format("2006-01"), "classes", n, "min_cohort", cfg.StatsMinCohort)
	return nil
}
