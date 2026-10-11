package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/httpx"
)

// Guard mounts the role gates. It re-reads the account on every request, so
// disabling an account takes effect immediately rather than at token expiry.
type Guard struct {
	Tokens   Tokens
	Accounts Store
}

// RequireTeacher, RequireParent and RequireAdmin are the three mount-time
// gates. Every route that touches student data sits behind exactly one.
func (g Guard) RequireTeacher() httpx.Middleware { return g.require(RoleTeacher) }
func (g Guard) RequireParent() httpx.Middleware  { return g.require(RoleParent) }
func (g Guard) RequireAdmin() httpx.Middleware   { return g.require(RoleAdmin) }

func (g Guard) require(want Role) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			claims, err := g.authenticate(r)
			if err != nil {
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			// A role mismatch is 403, not 404: the caller is known, the route
			// is simply not theirs. Object-level misses inside parent/ are the
			// ones that have to stay 404.
			if claims.Role != want {
				slog.WarnContext(r.Context(), "role mismatch",
					"have", claims.Role, "want", want, "path", r.URL.Path,
					"request_id", httpx.RequestID(r.Context()))
				httpx.WriteError(w, r, httpx.ErrForbidden)
				return
			}

			accountID, err := uuid.Parse(claims.Subject)
			if err != nil {
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			acct, err := g.Accounts.AccountByID(r.Context(), accountID)
			if err != nil {
				if !errors.Is(err, ErrAccountNotFound) {
					slog.ErrorContext(r.Context(), "account lookup failed", "err", err)
				}
				httpx.WriteError(w, r, httpx.ErrUnauthorized)
				return
			}
			// The token's role is a claim; the account row is the fact.
			if acct.Disabled() || acct.Role != want {
				httpx.WriteError(w, r, httpx.ErrForbidden)
				return
			}

			ctx := WithIdentity(r.Context(), Identity{AccountID: acct.ID, Role: acct.Role})
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func (g Guard) authenticate(r *http.Request) (Claims, error) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return Claims{}, ErrTokenMalformed
	}
	token = strings.TrimSpace(token)
	if token == "" {
		return Claims{}, ErrTokenMalformed
	}
	return g.Tokens.Parse(token)
}
