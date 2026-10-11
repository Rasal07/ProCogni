package auth

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/proleaf/proleaf-api/internal/httpx"
)

// API serves the two public endpoints. They are the only routes in the service
// with no identity in context.
type API struct {
	OTP    OTP
	Tokens Tokens
}

func (a API) Routes(mux *http.ServeMux) {
	mux.Handle("POST /auth/otp/request", httpx.Handler(a.requestOTP))
	mux.Handle("POST /auth/otp/verify", httpx.Handler(a.verifyOTP))
}

type otpRequestBody struct {
	Phone string `json:"phone"`
}

func (a API) requestOTP(w http.ResponseWriter, r *http.Request) error {
	var body otpRequestBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	phone, err := a.OTP.Request(r.Context(), body.Phone)
	switch {
	case err == nil:
	case errors.Is(err, ErrInvalidPhone):
		return httpx.Invalid("phone is not a valid number")
	case errors.Is(err, ErrResendTooSoon):
		return httpx.ErrRateLimited
	default:
		return err
	}
	slog.InfoContext(r.Context(), "otp requested", "phone", redact(phone),
		"request_id", httpx.RequestID(r.Context()))

	// 202: the code has been handed to the gateway, not necessarily delivered.
	return httpx.JSON(w, http.StatusAccepted, map[string]any{"status": "sent"})
}

type otpVerifyBody struct {
	Phone string `json:"phone"`
	Code  string `json:"code"`
}

type tokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
	Role      Role      `json:"role"`
	AccountID string    `json:"account_id"`
}

func (a API) verifyOTP(w http.ResponseWriter, r *http.Request) error {
	var body otpVerifyBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	acct, err := a.OTP.Verify(r.Context(), body.Phone, body.Code)
	switch {
	case err == nil:
	case errors.Is(err, ErrInvalidPhone):
		return httpx.Invalid("phone is not a valid number")
	case errors.Is(err, ErrTooManyAttempts):
		return httpx.ErrRateLimited
	case errors.Is(err, ErrNoChallenge), errors.Is(err, ErrChallengeExpired),
		errors.Is(err, ErrCodeMismatch), errors.Is(err, ErrAccountNotFound):
		// One response for every failure mode. Distinguishing "expired" from
		// "wrong" from "no such account" is a free oracle for an attacker.
		return httpx.Errorf(http.StatusUnauthorized, "otp_invalid", "code is not valid")
	default:
		return err
	}

	token, expires, err := a.Tokens.Issue(acct.ID, acct.Role)
	if err != nil {
		return err
	}
	slog.InfoContext(r.Context(), "otp verified", "account_id", acct.ID, "role", acct.Role,
		"request_id", httpx.RequestID(r.Context()))

	return httpx.JSON(w, http.StatusOK, tokenResponse{
		Token:     token,
		ExpiresAt: expires,
		Role:      acct.Role,
		AccountID: acct.ID.String(),
	})
}

// redact keeps a phone number identifiable in logs without writing it out.
func redact(phone string) string {
	if len(phone) <= 4 {
		return "****"
	}
	return "****" + phone[len(phone)-4:]
}
