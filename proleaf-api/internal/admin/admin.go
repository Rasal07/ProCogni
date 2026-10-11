// Package admin is the school-operations surface: schools, classes, students,
// accounts, enrollment codes and the audit log.
//
// It is mounted on its own path prefix behind RequireAdmin and writes to its
// own audit stream, so admin activity can be retained and alerted on
// separately from ordinary teacher and parent traffic.
package admin

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/enroll"
	"github.com/proleaf/proleaf-api/internal/httpx"
	"github.com/proleaf/proleaf-api/internal/student"
)

var (
	ErrSchoolUnknown  = errors.New("admin: no such school")
	ErrStudentUnknown = errors.New("admin: no such student")
	ErrClassUnknown   = errors.New("admin: no such class")
	ErrAccountUnknown = errors.New("admin: no such account")
	ErrCodeUnknown    = errors.New("admin: no such enrollment code")
	// ErrNoLink is a teacher assignment or guardian binding that is not live.
	ErrNoLink        = errors.New("admin: no such live assignment or binding")
	ErrStageMismatch = errors.New("admin: student CAT stage does not match the class")
	// ErrCodeSpent is a code that was already consumed or revoked. A consumed
	// code produced a binding; undoing that is a binding revocation.
	ErrCodeSpent = errors.New("admin: enrollment code is already consumed or revoked")
)

type School struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

type NewClass struct {
	Name         string
	CATStage     int
	AcademicYear string
	SchoolID     *uuid.UUID
}

// ClassPatch and StudentPatch carry only the fields being changed; nil leaves
// the column alone. cat_stage is absent from both on purpose: a class's stage
// is fixed, and a student's follows their class.
type ClassPatch struct {
	Name         *string    `json:"name"`
	AcademicYear *string    `json:"academic_year"`
	SchoolID     *uuid.UUID `json:"school_id"`
}

type StudentPatch struct {
	DisplayName *string    `json:"display_name"`
	ClassID     *uuid.UUID `json:"class_id"`
}

type NewStudent struct {
	DisplayName string
	CATStage    int
	ClassID     uuid.UUID
	DateOfBirth student.Date
}

// Guardian is one live binding, seen from the student's side.
type Guardian struct {
	AccountID uuid.UUID `json:"account_id"`
	Phone     string    `json:"phone"`
	BoundAt   time.Time `json:"bound_at"`
}

type AccountFilter struct {
	Role  auth.Role // empty for any
	Phone string    // normalized; empty for any
	Limit int
}

// Code is an enrollment code as stored: everything except the plaintext.
type Code struct {
	ID         uuid.UUID  `json:"id"`
	StudentID  uuid.UUID  `json:"student_id"`
	IssuedAt   time.Time  `json:"issued_at"`
	ExpiresAt  time.Time  `json:"expires_at"`
	ConsumedAt *time.Time `json:"consumed_at,omitempty"`
	RevokedAt  *time.Time `json:"revoked_at,omitempty"`
	// Status is derived at read time: active, consumed, revoked or expired.
	Status string `json:"status"`
}

// WithStatus fills in Status as of now.
func (c Code) WithStatus(now time.Time) Code {
	switch {
	case c.ConsumedAt != nil:
		c.Status = "consumed"
	case c.RevokedAt != nil:
		c.Status = "revoked"
	case !c.ExpiresAt.After(now):
		c.Status = "expired"
	default:
		c.Status = "active"
	}
	return c
}

type Store interface {
	CreateSchool(ctx context.Context, name string) (School, error)
	Schools(ctx context.Context) ([]School, error)
	RenameSchool(ctx context.Context, id uuid.UUID, name string) (School, error)

	CreateClass(ctx context.Context, in NewClass) (student.Class, error)
	Classes(ctx context.Context, schoolID *uuid.UUID) ([]student.Class, error)
	Class(ctx context.Context, id uuid.UUID) (student.Class, error)
	UpdateClass(ctx context.Context, id uuid.UUID, p ClassPatch) (student.Class, error)
	ClassTeachers(ctx context.Context, classID uuid.UUID) ([]auth.Account, error)
	AssignTeacher(ctx context.Context, classID, accountID uuid.UUID) error
	UnassignTeacher(ctx context.Context, classID, accountID uuid.UUID, now time.Time) error

	CreateStudent(ctx context.Context, in NewStudent) (student.Student, error)
	// Students lists a class roster, archived students included on request.
	Students(ctx context.Context, classID uuid.UUID, includeArchived bool) ([]student.Student, error)
	// Student returns archived students too: an admin has to be able to find
	// one in order to restore it.
	Student(ctx context.Context, id uuid.UUID) (student.Student, error)
	// UpdateStudent sets cat_stage alongside class_id when stage is non-nil.
	UpdateStudent(ctx context.Context, id uuid.UUID, p StudentPatch, stage *int) (student.Student, error)
	SetStudentArchived(ctx context.Context, id uuid.UUID, archivedAt *time.Time) (student.Student, error)
	Guardians(ctx context.Context, studentID uuid.UUID) ([]Guardian, error)
	RevokeBinding(ctx context.Context, studentID, accountID uuid.UUID, now time.Time) error

	Accounts(ctx context.Context, f AccountFilter) ([]auth.Account, error)
	RenameAccount(ctx context.Context, id uuid.UUID, displayName string) (auth.Account, error)
	SetAccountDisabled(ctx context.Context, id uuid.UUID, disabledAt *time.Time) (auth.Account, error)

	CreateEnrollmentCode(ctx context.Context, id, studentID, issuedBy uuid.UUID, codeHash string, expiresAt time.Time) error
	// EnrollmentCodes lists by student or by class; exactly one is non-nil.
	EnrollmentCodes(ctx context.Context, studentID, classID *uuid.UUID) ([]Code, error)
	RevokeEnrollmentCode(ctx context.Context, id, revokedBy uuid.UUID, now time.Time) error
}

type Service struct {
	Store      Store
	Accounts   auth.Store
	CodePepper []byte
	CodeTTL    time.Duration
	Now        func() time.Time
}

func (s Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// name trims a required display string. Validation failures in this package
// are httpx.Invalid, so that only they — and never a database error — reach
// the client as a 400.
func name(field, raw string) (string, error) {
	v := strings.TrimSpace(raw)
	if v == "" || len([]rune(v)) > 200 {
		return "", httpx.Invalid(field + " is required and must be at most 200 characters")
	}
	return v, nil
}

func (s Service) CreateSchool(ctx context.Context, rawName string) (School, error) {
	n, err := name("name", rawName)
	if err != nil {
		return School{}, err
	}
	return s.Store.CreateSchool(ctx, n)
}

func (s Service) RenameSchool(ctx context.Context, id uuid.UUID, rawName string) (School, error) {
	n, err := name("name", rawName)
	if err != nil {
		return School{}, err
	}
	return s.Store.RenameSchool(ctx, id, n)
}

func (s Service) CreateClass(ctx context.Context, in NewClass) (student.Class, error) {
	var err error
	if in.Name, err = name("name", in.Name); err != nil {
		return student.Class{}, err
	}
	if in.AcademicYear, err = name("academic_year", in.AcademicYear); err != nil {
		return student.Class{}, err
	}
	if !student.ValidCATStage(in.CATStage) {
		return student.Class{}, httpx.Invalid("cat_stage must be between 3 and 5")
	}
	return s.Store.CreateClass(ctx, in)
}

func (s Service) UpdateClass(ctx context.Context, id uuid.UUID, p ClassPatch) (student.Class, error) {
	for field, v := range map[string]*string{"name": p.Name, "academic_year": p.AcademicYear} {
		if v == nil {
			continue
		}
		n, err := name(field, *v)
		if err != nil {
			return student.Class{}, err
		}
		*v = n
	}
	return s.Store.UpdateClass(ctx, id, p)
}

// ClassDetail is a class with the teachers currently assigned to it.
type ClassDetail struct {
	student.Class
	Teachers []auth.Account `json:"teachers"`
}

func (s Service) Class(ctx context.Context, id uuid.UUID) (ClassDetail, error) {
	class, err := s.Store.Class(ctx, id)
	if err != nil {
		return ClassDetail{}, err
	}
	teachers, err := s.Store.ClassTeachers(ctx, id)
	if err != nil {
		return ClassDetail{}, err
	}
	return ClassDetail{Class: class, Teachers: teachers}, nil
}

func (s Service) CreateStudent(ctx context.Context, in NewStudent) (student.Student, error) {
	var err error
	if in.DisplayName, err = name("display_name", in.DisplayName); err != nil {
		return student.Student{}, err
	}
	if !student.ValidCATStage(in.CATStage) {
		return student.Student{}, httpx.Invalid("cat_stage must be between 3 and 5")
	}
	if in.DateOfBirth.IsZero() || in.DateOfBirth.After(student.NewDate(s.now())) {
		return student.Student{}, httpx.Invalid("date_of_birth is required and must be in the past")
	}

	class, err := s.Store.Class(ctx, in.ClassID)
	if err != nil {
		return student.Student{}, err
	}
	// A student filed under a class of a different stage would show up in the
	// wrong cohort's aggregates, which is a privacy bug as much as a data one.
	if class.CATStage != in.CATStage {
		return student.Student{}, ErrStageMismatch
	}
	return s.Store.CreateStudent(ctx, in)
}

// UpdateStudent renames a student and/or moves them to another class. A move
// takes the new class's stage with it, which keeps the invariant CreateStudent
// enforces without asking the caller to restate the stage.
func (s Service) UpdateStudent(ctx context.Context, id uuid.UUID, p StudentPatch) (student.Student, error) {
	if p.DisplayName != nil {
		n, err := name("display_name", *p.DisplayName)
		if err != nil {
			return student.Student{}, err
		}
		p.DisplayName = &n
	}
	var stage *int
	if p.ClassID != nil {
		class, err := s.Store.Class(ctx, *p.ClassID)
		if err != nil {
			return student.Student{}, err
		}
		stage = &class.CATStage
	}
	return s.Store.UpdateStudent(ctx, id, p, stage)
}

func (s Service) SetStudentArchived(ctx context.Context, id uuid.UUID, archived bool) (student.Student, error) {
	var at *time.Time
	if archived {
		now := s.now().UTC()
		at = &now
	}
	return s.Store.SetStudentArchived(ctx, id, at)
}

// StudentDetail is a student with their live guardian bindings.
type StudentDetail struct {
	student.Student
	Guardians []Guardian `json:"guardians"`
}

func (s Service) Student(ctx context.Context, id uuid.UUID) (StudentDetail, error) {
	st, err := s.Store.Student(ctx, id)
	if err != nil {
		return StudentDetail{}, err
	}
	guardians, err := s.Store.Guardians(ctx, id)
	if err != nil {
		return StudentDetail{}, err
	}
	return StudentDetail{Student: st, Guardians: guardians}, nil
}

func (s Service) AssignTeacher(ctx context.Context, classID, accountID uuid.UUID) error {
	acct, err := s.Accounts.AccountByID(ctx, accountID)
	if errors.Is(err, auth.ErrAccountNotFound) {
		return ErrAccountUnknown
	}
	if err != nil {
		return err
	}
	if acct.Role != auth.RoleTeacher {
		return httpx.Invalid("only a teacher account can be assigned to a class")
	}
	return s.Store.AssignTeacher(ctx, classID, accountID)
}

// IssuedCode is returned once, at issue time. The plaintext is not stored and
// cannot be recovered: a lost code is revoked and reissued, not looked up.
type IssuedCode struct {
	ID        uuid.UUID `json:"id"`
	Code      string    `json:"code"`
	StudentID uuid.UUID `json:"student_id"`
	// DisplayName is filled in on the class sheet, where the code has to be
	// matched to a child by whoever hands the slips out.
	DisplayName string    `json:"display_name,omitempty"`
	ExpiresAt   time.Time `json:"expires_at"`
}

func (s Service) IssueEnrollmentCode(ctx context.Context, studentID, issuedBy uuid.UUID) (IssuedCode, error) {
	st, err := s.Store.Student(ctx, studentID)
	if err != nil {
		return IssuedCode{}, err
	}
	if st.Archived() {
		return IssuedCode{}, ErrStudentUnknown
	}
	return s.issue(ctx, studentID, issuedBy)
}

func (s Service) issue(ctx context.Context, studentID, issuedBy uuid.UUID) (IssuedCode, error) {
	code, err := enroll.Generate()
	if err != nil {
		return IssuedCode{}, err
	}
	normalized, err := enroll.Normalize(code)
	if err != nil {
		return IssuedCode{}, err
	}
	id := uuid.New()
	expires := s.now().UTC().Add(s.CodeTTL)
	if err := s.Store.CreateEnrollmentCode(ctx, id, studentID, issuedBy, enroll.Hash(s.CodePepper, normalized), expires); err != nil {
		return IssuedCode{}, err
	}
	return IssuedCode{ID: id, Code: code, StudentID: studentID, ExpiresAt: expires}, nil
}

// IssueClassCodes issues one code per active student in a class: the sheet a
// school prints and sends home. Students who already hold a live code get
// another; a second guardian needs one too, and unused codes expire.
func (s Service) IssueClassCodes(ctx context.Context, classID, issuedBy uuid.UUID) ([]IssuedCode, error) {
	if _, err := s.Store.Class(ctx, classID); err != nil {
		return nil, err
	}
	roster, err := s.Store.Students(ctx, classID, false)
	if err != nil {
		return nil, err
	}
	out := make([]IssuedCode, 0, len(roster))
	for _, st := range roster {
		issued, err := s.issue(ctx, st.ID, issuedBy)
		if err != nil {
			return nil, err
		}
		issued.DisplayName = st.DisplayName
		out = append(out, issued)
	}
	return out, nil
}

func (s Service) EnrollmentCodes(ctx context.Context, studentID, classID *uuid.UUID) ([]Code, error) {
	codes, err := s.Store.EnrollmentCodes(ctx, studentID, classID)
	if err != nil {
		return nil, err
	}
	now := s.now()
	for i := range codes {
		codes[i] = codes[i].WithStatus(now)
	}
	return codes, nil
}

// CreateAccount provisions a staff account. Parent accounts are created by the
// OTP flow instead; provisioning one here would create an account nobody has
// verified control of.
func (s Service) CreateAccount(ctx context.Context, rawPhone string, role auth.Role, displayName string) (auth.Account, error) {
	phone, err := auth.NormalizePhone(rawPhone)
	if err != nil {
		return auth.Account{}, err
	}
	if role != auth.RoleTeacher && role != auth.RoleAdmin {
		return auth.Account{}, httpx.Invalid("role must be teacher or admin")
	}
	if existing, err := s.Accounts.AccountByPhone(ctx, phone); err == nil {
		return existing, nil
	} else if !errors.Is(err, auth.ErrAccountNotFound) {
		return auth.Account{}, err
	}
	return s.Accounts.CreateAccount(ctx, phone, role, displayName)
}

func (s Service) Account(ctx context.Context, id uuid.UUID) (auth.Account, error) {
	acct, err := s.Accounts.AccountByID(ctx, id)
	if errors.Is(err, auth.ErrAccountNotFound) {
		return auth.Account{}, ErrAccountUnknown
	}
	return acct, err
}

// SetAccountDisabled disables or re-enables an account. The Guard re-reads the
// account on every request, so a disable takes effect on the next one.
func (s Service) SetAccountDisabled(ctx context.Context, actor, id uuid.UUID, disabled bool) (auth.Account, error) {
	var at *time.Time
	if disabled {
		// The one mistake this endpoint makes easy: locking yourself out, and
		// with a single admin, locking everyone out.
		if actor == id {
			return auth.Account{}, httpx.Invalid("an admin cannot disable their own account")
		}
		now := s.now().UTC()
		at = &now
	}
	return s.Store.SetAccountDisabled(ctx, id, at)
}
