package admin

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/audit"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/httpx"
	"github.com/proleaf/proleaf-api/internal/privacy"
	"github.com/proleaf/proleaf-api/internal/student"
)

type API struct {
	Service Service
	Audit   audit.Reader
	Privacy privacy.Service
}

func (a API) Routes(mux *http.ServeMux) {
	mux.Handle("POST /admin/schools", httpx.Handler(a.createSchool))
	mux.Handle("GET /admin/schools", httpx.Handler(a.listSchools))
	mux.Handle("PATCH /admin/schools/{id}", httpx.Handler(a.renameSchool))

	mux.Handle("POST /admin/classes", httpx.Handler(a.createClass))
	mux.Handle("GET /admin/classes", httpx.Handler(a.listClasses))
	mux.Handle("GET /admin/classes/{id}", httpx.Handler(a.getClass))
	mux.Handle("PATCH /admin/classes/{id}", httpx.Handler(a.updateClass))
	mux.Handle("POST /admin/classes/{id}/teachers", httpx.Handler(a.assignTeacher))
	mux.Handle("DELETE /admin/classes/{id}/teachers/{account_id}", httpx.Handler(a.unassignTeacher))
	mux.Handle("POST /admin/classes/{id}/enrollment-codes", httpx.Handler(a.issueClassCodes))

	mux.Handle("POST /admin/students", httpx.Handler(a.createStudent))
	mux.Handle("GET /admin/students", httpx.Handler(a.listStudents))
	mux.Handle("GET /admin/students/{id}", httpx.Handler(a.getStudent))
	mux.Handle("PATCH /admin/students/{id}", httpx.Handler(a.updateStudent))
	mux.Handle("POST /admin/students/{id}/archive", httpx.Handler(a.archiveStudent(true)))
	mux.Handle("POST /admin/students/{id}/restore", httpx.Handler(a.archiveStudent(false)))
	mux.Handle("DELETE /admin/students/{id}/guardians/{account_id}", httpx.Handler(a.revokeBinding))

	mux.Handle("POST /admin/accounts", httpx.Handler(a.createAccount))
	mux.Handle("GET /admin/accounts", httpx.Handler(a.listAccounts))
	mux.Handle("GET /admin/accounts/{id}", httpx.Handler(a.getAccount))
	mux.Handle("PATCH /admin/accounts/{id}", httpx.Handler(a.renameAccount))
	mux.Handle("POST /admin/accounts/{id}/disable", httpx.Handler(a.disableAccount(true)))
	mux.Handle("POST /admin/accounts/{id}/enable", httpx.Handler(a.disableAccount(false)))

	mux.Handle("POST /admin/enrollment-codes", httpx.Handler(a.issueCode))
	mux.Handle("GET /admin/enrollment-codes", httpx.Handler(a.listCodes))
	mux.Handle("POST /admin/enrollment-codes/{id}/revoke", httpx.Handler(a.revokeCode))

	mux.Handle("GET /admin/audit", httpx.Handler(a.readAudit))
	mux.Handle("GET /admin/subject-requests", httpx.Handler(a.listSubjectRequests))
	mux.Handle("POST /admin/subject-requests/{id}/status", httpx.Handler(a.setSubjectRequestStatus))
}

// ---------- schools ----------

type nameBody struct {
	Name string `json:"name"`
}

func (a API) createSchool(w http.ResponseWriter, r *http.Request) error {
	var body nameBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	school, err := a.Service.CreateSchool(r.Context(), body.Name)
	if err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionManageSchool)
	return httpx.JSON(w, http.StatusCreated, school)
}

func (a API) listSchools(w http.ResponseWriter, r *http.Request) error {
	schools, err := a.Service.Store.Schools(r.Context())
	if err != nil {
		return err
	}
	audit.Record(r.Context(), audit.ActionAdminRead)
	return httpx.JSON(w, http.StatusOK, map[string]any{"schools": schools})
}

func (a API) renameSchool(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var body nameBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	school, err := a.Service.RenameSchool(r.Context(), id, body.Name)
	if err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionManageSchool)
	return httpx.JSON(w, http.StatusOK, school)
}

// ---------- classes ----------

type createClassBody struct {
	Name         string     `json:"name"`
	CATStage     int        `json:"cat_stage"`
	AcademicYear string     `json:"academic_year"`
	SchoolID     *uuid.UUID `json:"school_id"`
}

func (a API) createClass(w http.ResponseWriter, r *http.Request) error {
	var body createClassBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	class, err := a.Service.CreateClass(r.Context(), NewClass(body))
	if err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionManageClass)
	return httpx.JSON(w, http.StatusCreated, class)
}

func (a API) listClasses(w http.ResponseWriter, r *http.Request) error {
	schoolID, err := optionalUUID(r.URL.Query().Get("school_id"))
	if err != nil {
		return err
	}
	classes, err := a.Service.Store.Classes(r.Context(), schoolID)
	if err != nil {
		return err
	}
	audit.Record(r.Context(), audit.ActionAdminRead)
	return httpx.JSON(w, http.StatusOK, map[string]any{"classes": classes})
}

func (a API) getClass(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	class, err := a.Service.Class(r.Context(), id)
	if err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionAdminRead)
	return httpx.JSON(w, http.StatusOK, class)
}

func (a API) updateClass(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var patch ClassPatch
	if err := httpx.Decode(r, &patch); err != nil {
		return err
	}
	class, err := a.Service.UpdateClass(r.Context(), id, patch)
	if err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionManageClass)
	return httpx.JSON(w, http.StatusOK, class)
}

type assignTeacherBody struct {
	AccountID uuid.UUID `json:"account_id"`
}

func (a API) assignTeacher(w http.ResponseWriter, r *http.Request) error {
	classID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var body assignTeacherBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	if err := a.Service.AssignTeacher(r.Context(), classID, body.AccountID); err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionManageClass)
	return httpx.JSON(w, http.StatusCreated, map[string]any{"status": "assigned"})
}

func (a API) unassignTeacher(w http.ResponseWriter, r *http.Request) error {
	classID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	accountID, err := pathUUID(r, "account_id")
	if err != nil {
		return err
	}
	if err := a.Service.Store.UnassignTeacher(r.Context(), classID, accountID, time.Now().UTC()); err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionManageClass)
	return httpx.NoContent(w)
}

// ---------- students ----------

type createStudentBody struct {
	DisplayName string       `json:"display_name"`
	CATStage    int          `json:"cat_stage"`
	ClassID     uuid.UUID    `json:"class_id"`
	DateOfBirth student.Date `json:"date_of_birth"`
}

func (a API) createStudent(w http.ResponseWriter, r *http.Request) error {
	var body createStudentBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	st, err := a.Service.CreateStudent(r.Context(), NewStudent(body))
	if err != nil {
		return translate(err)
	}
	audit.RecordStudent(r.Context(), audit.ActionCreateStudent, st.ID)
	return httpx.JSON(w, http.StatusCreated, st)
}

func (a API) listStudents(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	// A roster, not a directory: there is no route that pages through every
	// child in the system.
	classID, err := uuid.Parse(q.Get("class_id"))
	if err != nil {
		return httpx.Invalid("class_id is required")
	}
	students, err := a.Service.Store.Students(r.Context(), classID, q.Get("include_archived") == "true")
	if err != nil {
		return err
	}
	for _, st := range students {
		audit.RecordStudent(r.Context(), audit.ActionViewStudent, st.ID)
	}
	audit.Record(r.Context(), audit.ActionAdminRead)
	return httpx.JSON(w, http.StatusOK, map[string]any{"students": students})
}

func (a API) getStudent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	detail, err := a.Service.Student(r.Context(), id)
	if err != nil {
		return translate(err)
	}
	audit.RecordStudent(r.Context(), audit.ActionViewStudent, id)
	return httpx.JSON(w, http.StatusOK, detail)
}

func (a API) updateStudent(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var patch StudentPatch
	if err := httpx.Decode(r, &patch); err != nil {
		return err
	}
	st, err := a.Service.UpdateStudent(r.Context(), id, patch)
	if err != nil {
		return translate(err)
	}
	audit.RecordStudent(r.Context(), audit.ActionUpdateStudent, id)
	return httpx.JSON(w, http.StatusOK, st)
}

func (a API) archiveStudent(archive bool) httpx.Handler {
	action := audit.ActionRestoreStudent
	if archive {
		action = audit.ActionArchiveStudent
	}
	return func(w http.ResponseWriter, r *http.Request) error {
		id, err := pathUUID(r, "id")
		if err != nil {
			return err
		}
		st, err := a.Service.SetStudentArchived(r.Context(), id, archive)
		if err != nil {
			return translate(err)
		}
		audit.RecordStudent(r.Context(), action, id)
		return httpx.JSON(w, http.StatusOK, st)
	}
}

// revokeBinding ends one guardian's access to one student. The parent routes
// answer 404 for that student from the next request on.
func (a API) revokeBinding(w http.ResponseWriter, r *http.Request) error {
	studentID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	accountID, err := pathUUID(r, "account_id")
	if err != nil {
		return err
	}
	if err := a.Service.Store.RevokeBinding(r.Context(), studentID, accountID, time.Now().UTC()); err != nil {
		return translate(err)
	}
	audit.RecordStudent(r.Context(), audit.ActionRevokeBinding, studentID)
	return httpx.NoContent(w)
}

// ---------- accounts ----------

type createAccountBody struct {
	Phone       string `json:"phone"`
	Role        string `json:"role"`
	DisplayName string `json:"display_name"`
}

func (a API) createAccount(w http.ResponseWriter, r *http.Request) error {
	var body createAccountBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	acct, err := a.Service.CreateAccount(r.Context(), body.Phone, auth.Role(body.Role), body.DisplayName)
	if err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionCreateAccount)
	return httpx.JSON(w, http.StatusCreated, acct)
}

func (a API) listAccounts(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f := AccountFilter{Role: auth.Role(q.Get("role"))}
	if f.Role != "" && !f.Role.Valid() {
		return httpx.Invalid("role must be teacher, parent or admin")
	}
	if raw := q.Get("phone"); raw != "" {
		phone, err := auth.NormalizePhone(raw)
		if err != nil {
			return translate(err)
		}
		f.Phone = phone
	}
	if f.Limit, _ = strconv.Atoi(q.Get("limit")); f.Limit <= 0 || f.Limit > 500 {
		f.Limit = 100
	}
	accounts, err := a.Service.Store.Accounts(r.Context(), f)
	if err != nil {
		return err
	}
	audit.Record(r.Context(), audit.ActionAdminRead)
	return httpx.JSON(w, http.StatusOK, map[string]any{"accounts": accounts})
}

func (a API) getAccount(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	acct, err := a.Service.Account(r.Context(), id)
	if err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionAdminRead)
	return httpx.JSON(w, http.StatusOK, acct)
}

type renameAccountBody struct {
	DisplayName string `json:"display_name"`
}

func (a API) renameAccount(w http.ResponseWriter, r *http.Request) error {
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var body renameAccountBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	displayName, err := name("display_name", body.DisplayName)
	if err != nil {
		return err
	}
	acct, err := a.Service.Store.RenameAccount(r.Context(), id, displayName)
	if err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionUpdateAccount)
	return httpx.JSON(w, http.StatusOK, acct)
}

func (a API) disableAccount(disable bool) httpx.Handler {
	action := audit.ActionEnableAccount
	if disable {
		action = audit.ActionDisableAccount
	}
	return func(w http.ResponseWriter, r *http.Request) error {
		actor := auth.MustFromContext(r.Context())
		id, err := pathUUID(r, "id")
		if err != nil {
			return err
		}
		acct, err := a.Service.SetAccountDisabled(r.Context(), actor.AccountID, id, disable)
		if err != nil {
			return translate(err)
		}
		audit.Record(r.Context(), action)
		return httpx.JSON(w, http.StatusOK, acct)
	}
}

// ---------- enrollment codes ----------

type issueCodeBody struct {
	StudentID uuid.UUID `json:"student_id"`
}

func (a API) issueCode(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())

	var body issueCodeBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	issued, err := a.Service.IssueEnrollmentCode(r.Context(), body.StudentID, id.AccountID)
	if err != nil {
		return translate(err)
	}
	audit.RecordStudent(r.Context(), audit.ActionIssueEnrollmentCode, issued.StudentID)

	// The only time the plaintext exists outside the SMS/print channel.
	return httpx.JSON(w, http.StatusCreated, issued)
}

// issueClassCodes is the distribution step: one response holding a code per
// student, for the school to print and send home.
func (a API) issueClassCodes(w http.ResponseWriter, r *http.Request) error {
	id := auth.MustFromContext(r.Context())
	classID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	issued, err := a.Service.IssueClassCodes(r.Context(), classID, id.AccountID)
	if err != nil {
		return translate(err)
	}
	for _, c := range issued {
		audit.RecordStudent(r.Context(), audit.ActionIssueEnrollmentCode, c.StudentID)
	}
	// An empty class still did something worth a row.
	audit.Record(r.Context(), audit.ActionManageClass)
	return httpx.JSON(w, http.StatusCreated, map[string]any{"codes": issued})
}

func (a API) listCodes(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	studentID, err := optionalUUID(q.Get("student_id"))
	if err != nil {
		return err
	}
	classID, err := optionalUUID(q.Get("class_id"))
	if err != nil {
		return err
	}
	if (studentID == nil) == (classID == nil) {
		return httpx.Invalid("exactly one of student_id or class_id is required")
	}
	codes, err := a.Service.EnrollmentCodes(r.Context(), studentID, classID)
	if err != nil {
		return err
	}
	audit.Record(r.Context(), audit.ActionAdminRead)
	return httpx.JSON(w, http.StatusOK, map[string]any{"codes": codes})
}

func (a API) revokeCode(w http.ResponseWriter, r *http.Request) error {
	actor := auth.MustFromContext(r.Context())
	id, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := a.Service.Store.RevokeEnrollmentCode(r.Context(), id, actor.AccountID, time.Now().UTC()); err != nil {
		return translate(err)
	}
	audit.Record(r.Context(), audit.ActionRevokeEnrollmentCode)
	return httpx.JSON(w, http.StatusOK, map[string]any{"status": "revoked"})
}

// ---------- audit and data-subject requests ----------

func (a API) readAudit(w http.ResponseWriter, r *http.Request) error {
	q := r.URL.Query()
	f := audit.Filter{Stream: q.Get("stream")}
	if f.Stream != "" && f.Stream != audit.StreamApp && f.Stream != audit.StreamAdmin {
		return httpx.Invalid("stream must be app or admin")
	}
	var err error
	if f.ActorAccount, err = optionalUUID(q.Get("actor")); err != nil {
		return err
	}
	if f.SubjectStudent, err = optionalUUID(q.Get("subject")); err != nil {
		return err
	}
	if f.Since, err = optionalTime(q.Get("since")); err != nil {
		return err
	}
	if f.Until, err = optionalTime(q.Get("until")); err != nil {
		return err
	}
	f.Limit, _ = strconv.Atoi(q.Get("limit"))

	entries, err := a.Audit.Query(r.Context(), f)
	if err != nil {
		return err
	}
	// Reading the log is itself an audited act, in the admin stream.
	audit.Record(r.Context(), audit.ActionReadAudit)
	return httpx.JSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (a API) listSubjectRequests(w http.ResponseWriter, r *http.Request) error {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	requests, err := a.Privacy.List(r.Context(), r.URL.Query().Get("status"), limit)
	if err != nil {
		return err
	}
	audit.Record(r.Context(), audit.ActionSubjectRequest)
	return httpx.JSON(w, http.StatusOK, map[string]any{"requests": requests})
}

type subjectStatusBody struct {
	Status string `json:"status"`
}

func (a API) setSubjectRequestStatus(w http.ResponseWriter, r *http.Request) error {
	requestID, err := pathUUID(r, "id")
	if err != nil {
		return err
	}
	var body subjectStatusBody
	if err := httpx.Decode(r, &body); err != nil {
		return err
	}
	switch body.Status {
	case privacy.StatusInProgress, privacy.StatusFulfilled, privacy.StatusRefused:
	default:
		return httpx.Invalid("status must be in_progress, fulfilled or refused")
	}
	// Recording the outcome is in scope; carrying it out is not. Marking a
	// deletion fulfilled does not delete anything, and the skeleton does not
	// pretend otherwise.
	switch err := a.Privacy.Store.SetRequestStatus(r.Context(), requestID, body.Status, time.Now().UTC()); {
	case err == nil:
	case errors.Is(err, privacy.ErrRequestUnknown):
		return httpx.ErrNotFound
	default:
		return err
	}
	audit.Record(r.Context(), audit.ActionSubjectRequest)
	return httpx.JSON(w, http.StatusOK, map[string]any{"status": body.Status})
}

// translate maps this package's errors onto responses. Anything it does not
// recognise passes through and becomes a 500: a database error is never
// echoed to the client as a validation message.
func translate(err error) error {
	switch {
	case errors.Is(err, ErrSchoolUnknown), errors.Is(err, ErrClassUnknown),
		errors.Is(err, ErrStudentUnknown), errors.Is(err, ErrAccountUnknown),
		errors.Is(err, ErrCodeUnknown), errors.Is(err, ErrNoLink):
		return httpx.ErrNotFound
	case errors.Is(err, ErrStageMismatch):
		return httpx.Invalid("cat_stage does not match the class")
	case errors.Is(err, ErrCodeSpent):
		return httpx.Errorf(http.StatusConflict, "code_not_revocable",
			"enrollment code is already consumed or revoked")
	case errors.Is(err, auth.ErrInvalidPhone):
		return httpx.Invalid("phone is not a valid number")
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

func optionalUUID(raw string) (*uuid.UUID, error) {
	if raw == "" {
		return nil, nil
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		return nil, httpx.Invalid("not a valid id: " + raw)
	}
	return &id, nil
}

func optionalTime(raw string) (*time.Time, error) {
	if raw == "" {
		return nil, nil
	}
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return nil, httpx.Invalid("timestamps must be RFC3339")
	}
	return &t, nil
}
