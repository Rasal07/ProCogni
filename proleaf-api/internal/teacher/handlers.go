package teacher

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/audit"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/httpx"
	"github.com/proleaf/proleaf-api/internal/student"
)

type API struct {
	Service Service
}

func (a API) Routes(mux *http.ServeMux) {
	mux.Handle("GET /teacher/classes", httpx.Handler(a.classes))
	mux.Handle("GET /teacher/classes/{id}/students", httpx.Handler(a.classStudents))
	mux.Handle("POST /teacher/assessments", httpx.Handler(a.recordOne))
	mux.Handle("POST /teacher/assessments/bulk", httpx.Handler(a.recordBulk))
	mux.Handle("GET /teacher/students/{id}/history", httpx.Handler(a.history))
}

func (a API) classes(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())

	classes, err := a.Service.Classes(r.Context(), id.AccountID)
	if err != nil {
		return err
	}
	audit.Record(r.Context(), audit.ActionListClasses)
	return httpx.JSON(w, http.StatusOK, map[string]any{"classes": classes})
}

func (a API) classStudents(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())
	classID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}

	students, err := a.Service.ClassStudents(r.Context(), id.AccountID, classID)
	if err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionListClassStudents)
	return httpx.JSON(w, http.StatusOK, map[string]any{"students": students})
}

func (a API) history(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())
	studentID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))

	history, err := a.Service.StudentHistory(r.Context(), id.AccountID, studentID, limit)
	if err != nil {
		return translate(err)
	}
	audit.RecordStudent(r.Context(), audit.ActionViewStudent, studentID)
	return httpx.JSON(w, http.StatusOK, map[string]any{"sessions": history})
}

func (a API) recordOne(w http.ResponseWriter, r *http.Request) error {
	var entry NewAssessment
	if err := httpx.Decode(r, &entry); err != nil {
		return err
	}
	return a.record(w, r, student.Date{}, []NewAssessment{entry})
}

// bulkBody carries a default session date so a teacher submitting a day of
// entries sends it once. Per-entry session_date still wins.
type bulkBody struct {
	SessionDate student.Date    `json:"session_date"`
	Entries     []NewAssessment `json:"entries"`
}

func (a API) recordBulk(w http.ResponseWriter, r *http.Request) error {
	var body bulkBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	return a.record(w, r, body.SessionDate, body.Entries)
}

func (a API) record(w http.ResponseWriter, r *http.Request, defaultDate student.Date, entries []NewAssessment) error {
	id := auth.MustFromContext(r.Context())

	ids, failed, err := a.Service.Record(r.Context(), id.AccountID, defaultDate, entries)
	switch {
	case err == nil:
	case errors.Is(err, ErrEmptyBatch):
		return httpx.Invalid("entries must contain at least one assessment")
	case errors.Is(err, ErrNotTaught):
		// Which row was rejected, but not why it exists or whose it is.
		return httpx.Errorf(http.StatusNotFound, "student_not_in_your_classes",
			"an entry names a student you are not assigned to").
			WithDetails(map[string]any{"index": failed})
	case failed >= 0:
		return httpx.Invalid(err.Error()).WithDetails(map[string]any{"index": failed})
	default:
		return err
	}

	// One audit row per student touched, not one per batch: the log has to
	// answer "who looked at this child's record" for each child.
	for _, e := range entries {
		audit.RecordStudent(r.Context(), audit.ActionRecordAssessment, e.StudentID)
	}
	return httpx.JSON(w, http.StatusCreated, map[string]any{
		"recorded":       len(ids),
		"assessment_ids": ids,
	})
}

func translate(err error) error {
	if errors.Is(err, ErrNotTaught) {
		return httpx.ErrNotFound
	}
	return err
}

func pathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, httpx.ErrNotFound
	}
	return id, nil
}
