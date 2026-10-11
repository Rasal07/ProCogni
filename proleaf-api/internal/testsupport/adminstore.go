package testsupport

import (
	"context"
	"sort"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/admin"
	"github.com/proleaf/proleaf-api/internal/audit"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/student"
)

// ---------- admin.Store ----------

type AdminStore struct{ W *World }

func (s AdminStore) CreateSchool(_ context.Context, name string) (admin.School, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	sc := admin.School{ID: uuid.New(), Name: name, CreatedAt: time.Now().UTC()}
	s.W.Schools[sc.ID] = sc
	return sc, nil
}

func (s AdminStore) Schools(context.Context) ([]admin.School, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	out := []admin.School{}
	for _, sc := range s.W.Schools {
		out = append(out, sc)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s AdminStore) RenameSchool(_ context.Context, id uuid.UUID, name string) (admin.School, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	sc, ok := s.W.Schools[id]
	if !ok {
		return admin.School{}, admin.ErrSchoolUnknown
	}
	sc.Name = name
	s.W.Schools[id] = sc
	return sc, nil
}

func (s AdminStore) knownSchool(id *uuid.UUID) bool {
	if id == nil {
		return true
	}
	_, ok := s.W.Schools[*id]
	return ok
}

func (s AdminStore) CreateClass(_ context.Context, in admin.NewClass) (student.Class, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	if !s.knownSchool(in.SchoolID) {
		return student.Class{}, admin.ErrSchoolUnknown
	}
	c := student.Class{ID: uuid.New(), Name: in.Name, CATStage: in.CATStage, AcademicYear: in.AcademicYear, SchoolID: in.SchoolID}
	s.W.Classes[c.ID] = c
	return c, nil
}

func (s AdminStore) Classes(_ context.Context, schoolID *uuid.UUID) ([]student.Class, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	out := []student.Class{}
	for id, c := range s.W.Classes {
		if schoolID == nil || (c.SchoolID != nil && *c.SchoolID == *schoolID) {
			out = append(out, s.W.class(id))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s AdminStore) Class(_ context.Context, id uuid.UUID) (student.Class, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	if _, ok := s.W.Classes[id]; !ok {
		return student.Class{}, admin.ErrClassUnknown
	}
	return s.W.class(id), nil
}

func (s AdminStore) UpdateClass(_ context.Context, id uuid.UUID, p admin.ClassPatch) (student.Class, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	c, ok := s.W.Classes[id]
	if !ok {
		return student.Class{}, admin.ErrClassUnknown
	}
	if !s.knownSchool(p.SchoolID) {
		return student.Class{}, admin.ErrSchoolUnknown
	}
	if p.Name != nil {
		c.Name = *p.Name
	}
	if p.AcademicYear != nil {
		c.AcademicYear = *p.AcademicYear
	}
	if p.SchoolID != nil {
		c.SchoolID = p.SchoolID
	}
	s.W.Classes[id] = c
	return s.W.class(id), nil
}

func (s AdminStore) ClassTeachers(_ context.Context, classID uuid.UUID) ([]auth.Account, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	out := []auth.Account{}
	for accountID, live := range s.W.Assignments[classID] {
		if live {
			out = append(out, s.W.Accounts[accountID])
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Phone < out[j].Phone })
	return out, nil
}

func (s AdminStore) AssignTeacher(_ context.Context, classID, accountID uuid.UUID) error {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	if _, ok := s.W.Classes[classID]; !ok {
		return admin.ErrClassUnknown
	}
	if s.W.Assignments[classID] == nil {
		s.W.Assignments[classID] = map[uuid.UUID]bool{}
	}
	s.W.Assignments[classID][accountID] = true
	return nil
}

func (s AdminStore) UnassignTeacher(_ context.Context, classID, accountID uuid.UUID, _ time.Time) error {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	if !s.W.teaches(accountID, classID) {
		return admin.ErrNoLink
	}
	s.W.Assignments[classID][accountID] = false
	return nil
}

func (s AdminStore) CreateStudent(_ context.Context, in admin.NewStudent) (student.Student, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	st := student.Student{
		ID: uuid.New(), DisplayName: in.DisplayName, CATStage: in.CATStage,
		ClassID: in.ClassID, DateOfBirth: in.DateOfBirth.Time(),
	}
	s.W.Students[st.ID] = st
	return st, nil
}

func (s AdminStore) Students(_ context.Context, classID uuid.UUID, includeArchived bool) ([]student.Student, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	out := []student.Student{}
	for _, st := range s.W.Students {
		if st.ClassID == classID && (includeArchived || !st.Archived()) {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out, nil
}

func (s AdminStore) Student(_ context.Context, id uuid.UUID) (student.Student, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	st, ok := s.W.Students[id]
	if !ok {
		return student.Student{}, admin.ErrStudentUnknown
	}
	return st, nil
}

func (s AdminStore) UpdateStudent(_ context.Context, id uuid.UUID, p admin.StudentPatch, stage *int) (student.Student, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	st, ok := s.W.Students[id]
	if !ok {
		return student.Student{}, admin.ErrStudentUnknown
	}
	if p.DisplayName != nil {
		st.DisplayName = *p.DisplayName
	}
	if p.ClassID != nil {
		st.ClassID = *p.ClassID
	}
	if stage != nil {
		st.CATStage = *stage
	}
	s.W.Students[id] = st
	return st, nil
}

func (s AdminStore) SetStudentArchived(_ context.Context, id uuid.UUID, archivedAt *time.Time) (student.Student, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	st, ok := s.W.Students[id]
	if !ok {
		return student.Student{}, admin.ErrStudentUnknown
	}
	st.ArchivedAt = archivedAt
	s.W.Students[id] = st
	return st, nil
}

func (s AdminStore) Guardians(_ context.Context, studentID uuid.UUID) ([]admin.Guardian, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	out := []admin.Guardian{}
	for accountID := range s.W.Bindings {
		if s.W.bound(accountID, studentID) {
			out = append(out, admin.Guardian{
				AccountID: accountID,
				Phone:     s.W.Accounts[accountID].Phone,
				BoundAt:   s.W.BoundAt[[2]uuid.UUID{accountID, studentID}],
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].BoundAt.Before(out[j].BoundAt) })
	return out, nil
}

func (s AdminStore) RevokeBinding(_ context.Context, studentID, accountID uuid.UUID, _ time.Time) error {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	if !s.W.bound(accountID, studentID) {
		return admin.ErrNoLink
	}
	s.W.Bindings[accountID][studentID] = false
	return nil
}

func (s AdminStore) Accounts(_ context.Context, f admin.AccountFilter) ([]auth.Account, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	out := []auth.Account{}
	for _, a := range s.W.Accounts {
		if (f.Role == "" || a.Role == f.Role) && (f.Phone == "" || a.Phone == f.Phone) {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Phone < out[j].Phone })
	if len(out) > f.Limit {
		out = out[:f.Limit]
	}
	return out, nil
}

func (s AdminStore) RenameAccount(_ context.Context, id uuid.UUID, displayName string) (auth.Account, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	a, ok := s.W.Accounts[id]
	if !ok {
		return auth.Account{}, admin.ErrAccountUnknown
	}
	a.DisplayName = displayName
	s.W.Accounts[id] = a
	return a, nil
}

func (s AdminStore) SetAccountDisabled(_ context.Context, id uuid.UUID, disabledAt *time.Time) (auth.Account, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	a, ok := s.W.Accounts[id]
	if !ok {
		return auth.Account{}, admin.ErrAccountUnknown
	}
	a.DisabledAt = disabledAt
	s.W.Accounts[id] = a
	return a, nil
}

func (s AdminStore) CreateEnrollmentCode(_ context.Context, id, studentID, _ uuid.UUID, codeHash string, expiresAt time.Time) error {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	s.W.Codes[codeHash] = &Code{ID: id, StudentID: studentID, IssuedAt: time.Now().UTC(), ExpiresAt: expiresAt}
	return nil
}

func (s AdminStore) EnrollmentCodes(_ context.Context, studentID, classID *uuid.UUID) ([]admin.Code, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	out := []admin.Code{}
	for _, c := range s.W.Codes {
		if studentID != nil && c.StudentID != *studentID {
			continue
		}
		if classID != nil && s.W.Students[c.StudentID].ClassID != *classID {
			continue
		}
		out = append(out, admin.Code{
			ID: c.ID, StudentID: c.StudentID, IssuedAt: c.IssuedAt,
			ExpiresAt: c.ExpiresAt, ConsumedAt: c.ConsumedAt, RevokedAt: c.RevokedAt,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IssuedAt.After(out[j].IssuedAt) })
	return out, nil
}

func (s AdminStore) RevokeEnrollmentCode(_ context.Context, id, _ uuid.UUID, now time.Time) error {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	for _, c := range s.W.Codes {
		if c.ID != id {
			continue
		}
		if c.ConsumedAt != nil || c.RevokedAt != nil {
			return admin.ErrCodeSpent
		}
		c.RevokedAt = &now
		return nil
	}
	return admin.ErrCodeUnknown
}

// ---------- audit.Reader ----------

type AuditReader struct{ W *World }

func (s AuditReader) Query(_ context.Context, f audit.Filter) ([]audit.Entry, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	out := []audit.Entry{}
	for _, e := range s.W.AuditLog {
		if f.Stream != "" && e.Stream != f.Stream {
			continue
		}
		if f.SubjectStudent != nil && (e.SubjectStudent == nil || *e.SubjectStudent != *f.SubjectStudent) {
			continue
		}
		if f.ActorAccount != nil && e.ActorAccount != *f.ActorAccount {
			continue
		}
		out = append(out, e)
	}
	return out, nil
}
