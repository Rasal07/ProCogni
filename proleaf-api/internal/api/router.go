// Package api assembles the HTTP surface. Wiring only: no business logic
// lives here, and no other package imports it except the entrypoints and the
// end-to-end tests.
package api

import (
	"net/http"

	"github.com/proleaf/proleaf-api/docs"
	"github.com/proleaf/proleaf-api/internal/admin"
	"github.com/proleaf/proleaf-api/internal/audit"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/httpx"
	"github.com/proleaf/proleaf-api/internal/parent"
	"github.com/proleaf/proleaf-api/internal/teacher"
)

// Version is the path prefix every API route is served under. A breaking
// change to the contract gets a new prefix mounted next to this one; packages
// register their routes unprefixed and never see it.
const Version = "/v1"

type Deps struct {
	Guard   auth.Guard
	Auth    auth.API
	Teacher teacher.API
	Parent  parent.API
	Admin   admin.API
	Audit   audit.Sink
}

// New builds the router.
//
// The shape is the authorization story: three sub-muxes, each mounted once,
// each behind exactly one role gate and its audit stream. A route cannot be
// added to the teacher surface without inheriting RequireTeacher, because the
// only way to reach the teacher mux is through it.
func New(d Deps) http.Handler {
	v1 := http.NewServeMux()

	v1.Handle("GET /openapi.yaml", httpx.Handler(func(w http.ResponseWriter, _ *http.Request) error {
		w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
		_, err := w.Write(docs.OpenAPI)
		return err
	}))

	// Public: the only routes without an identity.
	d.Auth.Routes(v1)

	teacherMux := http.NewServeMux()
	d.Teacher.Routes(teacherMux)
	v1.Handle("/teacher/", httpx.Chain(httpx.Enveloped(teacherMux),
		d.Guard.RequireTeacher(),
		audit.Middleware(d.Audit, audit.StreamApp),
	))

	parentMux := http.NewServeMux()
	d.Parent.Routes(parentMux)
	v1.Handle("/parent/", httpx.Chain(httpx.Enveloped(parentMux),
		d.Guard.RequireParent(),
		audit.Middleware(d.Audit, audit.StreamApp),
	))

	// Separate path prefix, separate audit stream.
	adminMux := http.NewServeMux()
	d.Admin.Routes(adminMux)
	v1.Handle("/admin/", httpx.Chain(httpx.Enveloped(adminMux),
		d.Guard.RequireAdmin(),
		audit.Middleware(d.Audit, audit.StreamAdmin),
	))

	root := http.NewServeMux()
	// Unversioned: it is for load balancers, not for clients.
	root.Handle("GET /healthz", httpx.Handler(func(w http.ResponseWriter, _ *http.Request) error {
		return httpx.JSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}))
	root.Handle(Version+"/", http.StripPrefix(Version, httpx.Enveloped(v1)))

	return httpx.Chain(httpx.Enveloped(root),
		httpx.RequestIDMiddleware,
		httpx.Recover,
		httpx.AccessLog,
	)
}
