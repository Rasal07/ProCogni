package audit

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/httpx"
)

// Middleware installs a collector for the request and flushes it afterwards.
// Mount it inside the role middleware, so the identity is already resolved.
func Middleware(sink Sink, stream string) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			c := &collector{}
			ctx := context.WithValue(r.Context(), collectorKey, c)
			r = r.WithContext(ctx)

			rec := httpx.NewStatusRecorder(w)
			next.ServeHTTP(rec, r)

			entries := c.drain()
			requestID := httpx.RequestID(ctx)

			if len(entries) == 0 {
				// Every route behind this middleware reaches student data or
				// the list of it. A silent 2xx here is a coverage gap, and a
				// gap is what an MLPS reviewer will find first.
				if rec.Status >= 200 && rec.Status < 300 {
					slog.ErrorContext(ctx, "handler returned 2xx without an audit entry",
						"path", r.URL.Path, "method", r.Method, "request_id", requestID)
				}
				return
			}

			identity, _ := auth.FromContext(ctx)
			for i := range entries {
				entries[i].ActorAccount = identity.AccountID
				entries[i].ActorRole = string(identity.Role)
				entries[i].RequestID = requestID
				entries[i].Stream = stream
			}

			// The client may already be gone; the row still has to land.
			if err := sink.Append(context.WithoutCancel(ctx), entries); err != nil {
				slog.ErrorContext(ctx, "audit append failed",
					"err", err, "entries", len(entries), "request_id", requestID)
			}
		})
	}
}
