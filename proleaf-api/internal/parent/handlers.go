package parent

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/audit"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/consent"
	"github.com/proleaf/proleaf-api/internal/httpx"
	"github.com/proleaf/proleaf-api/internal/privacy"
	"github.com/proleaf/proleaf-api/internal/stats"
)

type API struct {
	Service       Service
	Privacy       privacy.Service
	PolicyVersion string
}

// Routes mounts the parent surface. The caller wraps these in RequireParent
// and the audit middleware; nothing here re-checks the role.
func (a API) Routes(mux *http.ServeMux) {
	mux.Handle("POST /parent/bind", httpx.Handler(a.bind))
	mux.Handle("GET /parent/children", httpx.Handler(a.children))
	mux.Handle("GET /parent/children/{id}/progress", httpx.Handler(a.progress))
	mux.Handle("GET /parent/children/{id}/comparison", httpx.Handler(a.comparison))
	mux.Handle("GET /parent/children/{id}/consent", httpx.Handler(a.consentStatus))
	mux.Handle("PUT /parent/children/{id}/consent", httpx.Handler(a.setConsent))
	mux.Handle("POST /parent/requests", httpx.Handler(a.submitSubjectRequest))
}

type bindBody struct {
	EnrollmentCode string   `json:"enrollment_code"`
	PolicyVersion  string   `json:"policy_version"`
	AcceptedScopes []string `json:"accepted_scopes"`
}

func (a API) bind(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())

	var body bindBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	// The client has to name the policy version it displayed. A mismatch means
	// the parent was shown text that is no longer the text in force, and their
	// agreement is not to the current terms.
	if body.PolicyVersion != a.PolicyVersion {
		return a.policyMismatch()
	}
	if !contains(body.AcceptedScopes, consent.ScopeProgress) {
		return httpx.Invalid("binding requires consent for the " + consent.ScopeProgress + " scope")
	}

	result, err := a.Service.Bind(r.Context(), id.AccountID, body.EnrollmentCode, body.AcceptedScopes)
	switch {
	case err == nil:
	case errors.Is(err, ErrCodeNotUsable):
		// Expired, already consumed, or never existed: one answer for all.
		return httpx.Errorf(http.StatusNotFound, "code_not_usable", "enrollment code is not usable")
	case errors.Is(err, ErrAlreadyBound):
		return httpx.Errorf(http.StatusConflict, "already_bound", "this account is already bound to that student")
	default:
		return err
	}

	audit.RecordStudent(r.Context(), audit.ActionBindGuardian, result.Student.ID)
	for _, s := range result.Consent {
		action := audit.ActionWithdrawConsent
		if s.Granted {
			action = audit.ActionGrantConsent
		}
		audit.RecordStudent(r.Context(), action, result.Student.ID)
	}
	return httpx.JSON(w, http.StatusCreated, result)
}

func (a API) children(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())

	children, err := a.Service.Children(r.Context(), id.AccountID)
	if err != nil {
		return err
	}
	audit.Record(r.Context(), audit.ActionListChildren)
	return httpx.JSON(w, http.StatusOK, map[string]any{"children": children})
}

func (a API) progress(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())
	studentID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	records, err := a.Service.Progress(r.Context(), id.AccountID, studentID, limit)
	if err != nil {
		return translate(err)
	}
	audit.RecordStudent(r.Context(), audit.ActionViewProgress, studentID)
	return httpx.JSON(w, http.StatusOK, map[string]any{"sessions": records})
}

func (a API) comparison(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())
	studentID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	period, err := parsePeriod(r.URL.Query().Get("period"))
	if err != nil {
		return err
	}

	result, err := a.Service.Comparison(r.Context(), id.AccountID, studentID, period)
	if err != nil {
		return translate(err)
	}
	audit.RecordStudent(r.Context(), audit.ActionViewComparison, studentID)
	return httpx.JSON(w, http.StatusOK, result)
}

func (a API) consentStatus(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())
	studentID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	status, err := a.Service.ConsentStatus(r.Context(), id.AccountID, studentID)
	if err != nil {
		return translate(err)
	}
	audit.RecordStudent(r.Context(), audit.ActionViewStudent, studentID)
	return httpx.JSON(w, http.StatusOK, map[string]any{
		"policy_version": a.PolicyVersion,
		"consent":        status,
	})
}

type setConsentBody struct {
	Scope         string `json:"scope"`
	Granted       bool   `json:"granted"`
	PolicyVersion string `json:"policy_version"`
}

func (a API) setConsent(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())
	studentID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var body setConsentBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	if !contains(consent.BindScopes, body.Scope) {
		return httpx.Invalid("unknown consent scope")
	}
	// A withdrawal is always against the current policy; a grant has to name it.
	if body.Granted && body.PolicyVersion != a.PolicyVersion {
		return a.policyMismatch()
	}

	status, err := a.Service.SetConsent(r.Context(), id.AccountID, studentID, body.Scope, body.Granted)
	if err != nil {
		return translate(err)
	}
	action := audit.ActionWithdrawConsent
	if body.Granted {
		action = audit.ActionGrantConsent
	}
	audit.RecordStudent(r.Context(), action, studentID)
	return httpx.JSON(w, http.StatusOK, map[string]any{
		"policy_version": a.PolicyVersion,
		"consent":        status,
	})
}

// policyMismatch carries the version in force in details, so a client that
// has not bound yet (and so cannot read it from the consent route) can learn it.
func (a API) policyMismatch() error {
	return httpx.Errorf(http.StatusConflict, "policy_version_mismatch",
		"consent must be given against policy version "+a.PolicyVersion).
		WithDetails(map[string]any{"policy_version": a.PolicyVersion})
}

// translate maps the two boundary failures onto the responses §4 specifies.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrNotBound):
		// 404, not 403. A 403 would confirm that the student id is real.
		return httpx.ErrNotFound
	case errors.Is(err, consent.ErrNoConsent),
		errors.Is(err, consent.ErrNotGranted),
		errors.Is(err, consent.ErrPolicyOutdated):
		return httpx.ErrConsentRequired
	default:
		return err
	}
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		// An unparseable id is treated exactly like an unbound one, so the
		// shape of the id is not an oracle either.
		return uuid.Nil, httpx.ErrNotFound
	}
	return id, nil
}

// parsePeriod accepts "2026-09"; empty means the current month.
func parsePeriod(raw string) (time.Time, error) {
	if raw == "" {
		return stats.Month(time.Now()), nil
	}
	t, err := time.Parse("2006-01", raw)
	if err != nil {
		return time.Time{}, httpx.Invalid("period must be formatted YYYY-MM")
	}
	return stats.Month(t), nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
