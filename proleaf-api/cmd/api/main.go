// Command api is the ProLeaf backend entrypoint. Wiring only.
package main

import (
	"context"
	"errors"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/proleaf/proleaf-api/internal/admin"
	"github.com/proleaf/proleaf-api/internal/api"
	"github.com/proleaf/proleaf-api/internal/audit"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/config"
	"github.com/proleaf/proleaf-api/internal/db"
	"github.com/proleaf/proleaf-api/internal/intelligence"
	"github.com/proleaf/proleaf-api/internal/parent"
	"github.com/proleaf/proleaf-api/internal/privacy"
	"github.com/proleaf/proleaf-api/internal/stats"
	"github.com/proleaf/proleaf-api/internal/teacher"
	"github.com/proleaf/proleaf-api/pkg/sms"
)

func main() {
	var (
		migrateOnly    = flag.Bool("migrate-only", false, "apply migrations and exit")
		bootstrapAdmin = flag.String("bootstrap-admin", "", "create an admin account for this phone number and exit")
	)
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})))

	if err := run(*migrateOnly, *bootstrapAdmin); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run(migrateOnly bool, bootstrapAdmin string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer database.Close()

	if err := db.Migrate(ctx, database); err != nil {
		return err
	}
	if migrateOnly {
		slog.Info("migrations applied")
		return nil
	}

	accounts := auth.NewStore(database.Conn())

	if bootstrapAdmin != "" {
		// The first admin cannot be created through /admin/accounts, because
		// that route needs an admin. This is the only other way in, it runs
		// from an operator's shell, and it exits without serving traffic.
		phone, err := auth.NormalizePhone(bootstrapAdmin)
		if err != nil {
			return err
		}
		acct, err := accounts.CreateAccount(ctx, phone, auth.RoleAdmin, "bootstrap admin")
		if err != nil {
			return err
		}
		slog.Info("bootstrap admin ready", "account_id", acct.ID, "role", acct.Role)
		return nil
	}

	sender, err := sms.New(cfg.SMSProvider, cfg.IsDev())
	if err != nil {
		return err
	}

	tokens := auth.Tokens{Secret: cfg.TokenSecret, TTL: cfg.TokenTTL, Issuer: cfg.TokenIssuer}
	guard := auth.Guard{Tokens: tokens, Accounts: accounts}

	statsStore := stats.NewStore(database.Conn())
	privacyService := privacy.Service{Store: privacy.NewStore(database.Conn())}

	handler := api.New(api.Deps{
		Guard: guard,
		Auth: auth.API{
			Tokens: tokens,
			OTP: auth.OTP{
				Store:          accounts,
				Sender:         sender,
				Pepper:         cfg.OTPPepper,
				TTL:            cfg.OTPTTL,
				MaxAttempts:    cfg.OTPMaxAttempts,
				ResendInterval: cfg.OTPResendInterval,
				Length:         cfg.OTPLength,
			},
		},
		Teacher: teacher.API{
			Service: teacher.Service{Store: teacher.NewStore(database)},
		},
		Parent: parent.API{
			PolicyVersion: cfg.PolicyVersion,
			Privacy:       privacyService,
			Service: parent.Service{
				Store:      parent.NewStore(database),
				Stats:      statsStore,
				Consent:    newConsentService(database, cfg.PolicyVersion),
				CodePepper: cfg.EnrollmentCodePepper,
			},
		},
		Admin: admin.API{
			Audit:   audit.NewReader(database.Conn()),
			Privacy: privacyService,
			Service: admin.Service{
				Store:      admin.NewStore(database.Conn()),
				Accounts:   accounts,
				CodePepper: cfg.EnrollmentCodePepper,
				CodeTTL:    cfg.EnrollmentCodeTTL,
			},
		},
		Audit: audit.NewSink(database.Conn()),
	})

	// Section 7: the provider exists, compiles, and is called from nowhere.
	// The assignment is here so that removing the package breaks the build.
	var _ intelligence.Provider = intelligence.NoOp{}

	server := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	errc := make(chan error, 1)
	go func() {
		slog.Info("listening", "addr", cfg.HTTPAddr, "env", cfg.Env, "policy_version", cfg.PolicyVersion)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errc <- err
		}
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		slog.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		return server.Shutdown(shutdownCtx)
	}
}
