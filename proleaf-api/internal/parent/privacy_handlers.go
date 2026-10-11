package parent

import (
	"errors"
	"net/http"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/audit"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/httpx"
	"github.com/proleaf/proleaf-api/internal/privacy"
)

// The data-subject request intake. Recording the request is in scope for the
// skeleton; fulfilling it is not (see internal/privacy). A guardian who asks
// for access, correction or erasure gets a receipt with a timestamp, and the
// obligation is then visible to staff at /admin/subject-requests.

type subjectRequestBody struct {
	Kind      string     `json:"kind"`
	StudentID *uuid.UUID `json:"student_id,omitempty"`
	Detail    string     `json:"detail,omitempty"`
}

func (a API) submitSubjectRequest(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())

	var body subjectRequestBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	if !privacy.ValidKind(body.Kind) {
		return httpx.Invalid("kind must be access, correction or deletion")
	}

	// A request about a specific child still has to pass the binding check,
	// or the intake form becomes a way to probe for student ids.
	if body.StudentID != nil {
		st, err := a.Service.ResolveBoundStudent(r.Context(), id.AccountID, *body.StudentID)
		if err != nil {
			return translate(err)
		}
		body.StudentID = &st.ID
	}

	req, err := a.Privacy.Submit(r.Context(), id.AccountID, body.StudentID, body.Kind, body.Detail)
	if err != nil {
		if errors.Is(err, privacy.ErrUnknownKind) {
			return httpx.Invalid("kind must be access, correction or deletion")
		}
		return err
	}
	if req.StudentID != nil {
		audit.RecordStudent(r.Context(), audit.ActionSubjectRequest, *req.StudentID)
	} else {
		audit.Record(r.Context(), audit.ActionSubjectRequest)
	}
	return httpx.JSON(w, http.StatusAccepted, req)
}
