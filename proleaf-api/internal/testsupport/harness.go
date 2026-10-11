package testsupport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/admin"
	"github.com/proleaf/proleaf-api/internal/api"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/consent"
	"github.com/proleaf/proleaf-api/internal/parent"
	"github.com/proleaf/proleaf-api/internal/privacy"
	"github.com/proleaf/proleaf-api/internal/teacher"
	"github.com/proleaf/proleaf-api/pkg/sms"
)

// PolicyVersion is the policy the harness runs under.
const PolicyVersion = "test-policy-1"

// CodePepper keys enrollment-code hashing in tests.
var CodePepper = []byte("test-enrollment-code-pepper-0000000000")

// Harness is the whole service, wired to in-memory stores, served over real
// HTTP. Tests go through the router so that middleware, status codes and JSON
// shapes are all exercised — which is the only way to check that an unbound
// request really comes back 404 rather than 403.
type Harness struct {
	W       *World
	Handler http.Handler
	Tokens  auth.Tokens
	SMS     *sms.Capture
}

func NewHarness() *Harness { return NewHarnessWith(Options{PolicyVersion: PolicyVersion}) }

// Options are the knobs the mock server turns; tests use the defaults.
type Options struct {
	PolicyVersion string
	// FixedOTP, when set, is the code every OTP request issues, so a person
	// can sign in without reading a log. Never reachable from cmd/api: this
	// package is wired only by tests and cmd/mock.
	FixedOTP string
}

func NewHarnessWith(opt Options) *Harness {
	w := NewWorld()
	capture := &sms.Capture{}

	accounts := AuthStore{W: w}
	tokens := auth.Tokens{
		Secret: []byte("test-token-secret-at-least-32-bytes!!"),
		TTL:    time.Hour,
		Issuer: "proleaf-test",
	}
	consentService := consent.Service{Store: ConsentStore{W: w}, PolicyVersion: opt.PolicyVersion}
	privacyService := privacy.Service{Store: &PrivacyStore{W: w}}

	otp := auth.OTP{
		Store:          accounts,
		Sender:         capture,
		Pepper:         []byte("test-otp-pepper"),
		TTL:            5 * time.Minute,
		MaxAttempts:    5,
		ResendInterval: time.Minute,
		Length:         6,
	}
	if opt.FixedOTP != "" {
		otp.ResendInterval = 0
		otp.Store = fixedOTPStore{Store: accounts, hash: func(phone string) string { return otp.Hash(phone, opt.FixedOTP) }}
	}

	handler := api.New(api.Deps{
		Guard:   auth.Guard{Tokens: tokens, Accounts: accounts},
		Auth:    auth.API{Tokens: tokens, OTP: otp},
		Teacher: teacher.API{Service: teacher.Service{Store: TeacherStore{W: w}}},
		Parent: parent.API{
			PolicyVersion: opt.PolicyVersion,
			Privacy:       privacyService,
			Service: parent.Service{
				Store:      ParentStore{W: w},
				Stats:      StatsReader{W: w},
				Consent:    consentService,
				CodePepper: CodePepper,
			},
		},
		Admin: admin.API{
			Privacy: privacyService,
			Audit:   AuditReader{W: w},
			Service: admin.Service{
				Store:      AdminStore{W: w},
				Accounts:   accounts,
				CodePepper: CodePepper,
				CodeTTL:    14 * 24 * time.Hour,
			},
		},
		Audit: AuditSink{W: w},
	})

	return &Harness{W: w, Handler: handler, Tokens: tokens, SMS: capture}
}

// fixedOTPStore swaps the hash of whatever code was generated for the hash of
// the fixed one, as the challenge is stored.
type fixedOTPStore struct {
	auth.Store
	hash func(phone string) string
}

func (s fixedOTPStore) InsertChallenge(ctx context.Context, c auth.Challenge) error {
	c.CodeHash = s.hash(c.Phone)
	return s.Store.InsertChallenge(ctx, c)
}

// Token mints a bearer token for an account.
func (h *Harness) Token(a auth.Account) string {
	tok, _, err := h.Tokens.Issue(a.ID, a.Role)
	if err != nil {
		panic(err)
	}
	return tok
}

// Response is a decoded reply.
type Response struct {
	Status int
	Body   []byte
}

// JSON decodes the body into dst.
func (r Response) JSON(dst any) error { return json.Unmarshal(r.Body, dst) }

// String returns the raw body, for assertions about what is *not* in it.
func (r Response) String() string { return string(r.Body) }

// Do issues a request against the versioned API: path is given without the
// /v1 prefix. A nil body sends none; token may be empty.
func (h *Harness) Do(method, path, token string, body any) Response {
	return h.DoRaw(method, api.Version+path, token, body)
}

// DoRaw issues a request to an exact path.
func (h *Harness) DoRaw(method, path, token string, body any) Response {
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			panic(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.Handler.ServeHTTP(rec, req)
	return Response{Status: rec.Code, Body: rec.Body.Bytes()}
}

// ---------- privacy.Store ----------

type PrivacyStore struct {
	W        *World
	Requests []privacy.Request
}

func (s *PrivacyStore) CreateRequest(_ context.Context, r privacy.Request) error {
	s.Requests = append(s.Requests, r)
	return nil
}

func (s *PrivacyStore) ListRequests(_ context.Context, status string, limit int) ([]privacy.Request, error) {
	out := []privacy.Request{}
	for _, r := range s.Requests {
		if status == "" || r.Status == status {
			out = append(out, r)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *PrivacyStore) SetRequestStatus(_ context.Context, id uuid.UUID, status string, at time.Time) error {
	for i := range s.Requests {
		if s.Requests[i].ID == id {
			s.Requests[i].Status = status
			s.Requests[i].ResolvedAt = &at
			return nil
		}
	}
	return privacy.ErrRequestUnknown
}

func (s *PrivacyStore) RetentionCandidates(context.Context, time.Time) (privacy.Candidates, error) {
	return privacy.Candidates{}, nil
}
