// Package testsupport provides in-memory implementations of the domain store
// interfaces, so the authorization and consent boundaries can be exercised
// end-to-end over real HTTP without a database.
//
// These fakes deliberately do NOT reimplement the binding check in SQL. They
// enforce it the same way the service layer expects the database to, which is
// what makes a test that passes here a test of the service's logic rather than
// of a query. The Postgres queries carry the same scoping and are checked
// separately by the source-level tests in each package.
package testsupport

import (
	"context"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/proleaf/proleaf-api/internal/admin"
	"github.com/proleaf/proleaf-api/internal/audit"
	"github.com/proleaf/proleaf-api/internal/auth"
	"github.com/proleaf/proleaf-api/internal/consent"
	"github.com/proleaf/proleaf-api/internal/parent"
	"github.com/proleaf/proleaf-api/internal/stats"
	"github.com/proleaf/proleaf-api/internal/student"
	"github.com/proleaf/proleaf-api/internal/teacher"
)

// World is the shared fixture: one in-memory database behind every store.
type World struct {
	mu sync.Mutex

	Accounts   map[uuid.UUID]auth.Account
	Challenges map[uuid.UUID]auth.Challenge
	Classes    map[uuid.UUID]student.Class
	Students   map[uuid.UUID]student.Student
	// Assignments is class -> teacher account -> assigned.
	Assignments map[uuid.UUID]map[uuid.UUID]bool
	// Bindings is account -> student -> live.
	Bindings map[uuid.UUID]map[uuid.UUID]bool
	// BoundAt is when each binding was last made, keyed account then student.
	BoundAt     map[[2]uuid.UUID]time.Time
	Schools     map[uuid.UUID]admin.School
	Assessments []student.Assessment
	Consents    []consent.Event
	Stats       map[string]stats.ClassStat
	Codes       map[string]*Code
	AuditLog    []audit.Entry
}

// Code is one enrollment code, keyed in World.Codes by its hash.
type Code struct {
	ID         uuid.UUID
	StudentID  uuid.UUID
	IssuedAt   time.Time
	ExpiresAt  time.Time
	ConsumedAt *time.Time
	RevokedAt  *time.Time
}

func NewWorld() *World {
	return &World{
		Accounts:    map[uuid.UUID]auth.Account{},
		Challenges:  map[uuid.UUID]auth.Challenge{},
		Classes:     map[uuid.UUID]student.Class{},
		Students:    map[uuid.UUID]student.Student{},
		Assignments: map[uuid.UUID]map[uuid.UUID]bool{},
		Bindings:    map[uuid.UUID]map[uuid.UUID]bool{},
		BoundAt:     map[[2]uuid.UUID]time.Time{},
		Schools:     map[uuid.UUID]admin.School{},
		Stats:       map[string]stats.ClassStat{},
		Codes:       map[string]*Code{},
	}
}

// ---------- fixture builders ----------

func (w *World) AddAccount(role auth.Role, phone string) auth.Account {
	w.mu.Lock()
	defer w.mu.Unlock()
	a := auth.Account{ID: uuid.New(), Phone: phone, Role: role}
	w.Accounts[a.ID] = a
	return a
}

func (w *World) AddClass(name string, stage int) student.Class {
	w.mu.Lock()
	defer w.mu.Unlock()
	c := student.Class{ID: uuid.New(), Name: name, CATStage: stage, AcademicYear: "2026"}
	w.Classes[c.ID] = c
	return c
}

func (w *World) AddStudent(classID uuid.UUID, name string, stage int, dob time.Time) student.Student {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := student.Student{
		ID: uuid.New(), DisplayName: name, CATStage: stage,
		ClassID: classID, DateOfBirth: dob,
	}
	w.Students[s.ID] = s
	return s
}

func (w *World) Assign(classID, teacherID uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Assignments[classID] == nil {
		w.Assignments[classID] = map[uuid.UUID]bool{}
	}
	w.Assignments[classID][teacherID] = true
}

func (w *World) Bind(accountID, studentID uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.bind(accountID, studentID, time.Now().UTC())
}

func (w *World) bind(accountID, studentID uuid.UUID, at time.Time) {
	if w.Bindings[accountID] == nil {
		w.Bindings[accountID] = map[uuid.UUID]bool{}
	}
	w.Bindings[accountID][studentID] = true
	w.BoundAt[[2]uuid.UUID{accountID, studentID}] = at
}

func (w *World) Revoke(accountID, studentID uuid.UUID) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.Bindings[accountID] != nil {
		w.Bindings[accountID][studentID] = false
	}
}

func (w *World) AddCode(code string, studentID uuid.UUID, expires time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Codes[code] = &Code{ID: uuid.New(), StudentID: studentID, IssuedAt: time.Now().UTC(), ExpiresAt: expires}
}

func (w *World) AddAssessment(studentID, by uuid.UUID, date time.Time, score float64) student.Assessment {
	w.mu.Lock()
	defer w.mu.Unlock()
	a := student.Assessment{
		ID: uuid.New(), StudentID: studentID, RecordedBy: by,
		SessionDate: date, Score: &score, CreatedAt: time.Now().UTC(),
		Note: "teacher-only note",
	}
	w.Assessments = append(w.Assessments, a)
	return a
}

// GrantConsent records a current grant for every bind scope, as though the
// parent had consented at bind time.
func (w *World) GrantConsent(accountID, studentID uuid.UUID, policyVersion string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	for _, scope := range consent.BindScopes {
		w.Consents = append(w.Consents, consent.Event{
			ID: uuid.New(), AccountID: accountID, StudentID: studentID,
			PolicyVersion: policyVersion, Scope: scope, Granted: true,
			OccurredAt: time.Now().UTC(),
		})
	}
}

// WithdrawConsent records a refusal for one scope, later than any grant.
func (w *World) WithdrawConsent(accountID, studentID uuid.UUID, scope, policyVersion string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Consents = append(w.Consents, consent.Event{
		ID: uuid.New(), AccountID: accountID, StudentID: studentID,
		PolicyVersion: policyVersion, Scope: scope, Granted: false,
		OccurredAt: time.Now().UTC().Add(time.Second),
	})
}

func (w *World) SetStats(s stats.ClassStat) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.Stats[statKey(s.ClassID, s.Period)] = s
}

// ComputeStats runs the real stats computation over the world's assessments
// for one class and month and stores the row the parent endpoint reads: the
// stats job, in-process.
func (w *World) ComputeStats(classID uuid.UUID, period time.Time, minCohort int) {
	w.mu.Lock()
	defer w.mu.Unlock()

	byStudent := map[uuid.UUID][]float64{}
	for _, a := range w.Assessments {
		st, ok := w.Students[a.StudentID]
		if !ok || st.ClassID != classID || a.Score == nil {
			continue
		}
		if !stats.Month(a.SessionDate).Equal(stats.Month(period)) {
			continue
		}
		byStudent[a.StudentID] = append(byStudent[a.StudentID], *a.Score)
	}

	scores := make([]float64, 0, len(byStudent))
	for _, vals := range byStudent {
		var sum float64
		for _, v := range vals {
			sum += v
		}
		scores = append(scores, sum/float64(len(vals)))
	}

	stat := stats.ClassStat{
		ClassID:      classID,
		Period:       stats.Month(period),
		StudentCount: len(scores),
		ComputedAt:   time.Now().UTC(),
	}
	if agg, ok := stats.Compute(scores); ok && len(scores) >= minCohort {
		stat.Mean, stat.Median, stat.Top = &agg.Mean, &agg.Median, &agg.Top
	}
	w.Stats[statKey(stat.ClassID, stat.Period)] = stat
}

// AuditEntries returns everything the audit sink has been handed.
func (w *World) AuditEntries() []audit.Entry {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]audit.Entry(nil), w.AuditLog...)
}

func statKey(classID uuid.UUID, period time.Time) string {
	return classID.String() + "|" + stats.Month(period).Format("2006-01")
}

// class returns a class with its student count derived, as the SQL does.
func (w *World) class(id uuid.UUID) student.Class {
	c := w.Classes[id]
	c.StudentCount = 0
	for _, st := range w.Students {
		if st.ClassID == id && !st.Archived() {
			c.StudentCount++
		}
	}
	return c
}

func (w *World) bound(accountID, studentID uuid.UUID) bool {
	return w.Bindings[accountID] != nil && w.Bindings[accountID][studentID]
}

func (w *World) teaches(accountID, classID uuid.UUID) bool {
	return w.Assignments[classID] != nil && w.Assignments[classID][accountID]
}

// ---------- auth.Store ----------

type AuthStore struct{ W *World }

func (s AuthStore) InsertChallenge(_ context.Context, c auth.Challenge) error {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	s.W.Challenges[c.ID] = c
	return nil
}

func (s AuthStore) LatestChallenge(_ context.Context, phone string) (auth.Challenge, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	var best auth.Challenge
	found := false
	for _, c := range s.W.Challenges {
		if c.Phone == phone && (!found || c.IssuedAt.After(best.IssuedAt)) {
			best, found = c, true
		}
	}
	if !found {
		return auth.Challenge{}, auth.ErrNoChallenge
	}
	return best, nil
}

func (s AuthStore) RecordAttempt(_ context.Context, id uuid.UUID) (int, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	c, ok := s.W.Challenges[id]
	if !ok {
		return 0, auth.ErrNoChallenge
	}
	c.Attempts++
	s.W.Challenges[id] = c
	return c.Attempts, nil
}

func (s AuthStore) ConsumeChallenge(_ context.Context, id uuid.UUID) error {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	c, ok := s.W.Challenges[id]
	if !ok || c.ConsumedAt != nil {
		return auth.ErrNoChallenge
	}
	now := time.Now().UTC()
	c.ConsumedAt = &now
	s.W.Challenges[id] = c
	return nil
}

func (s AuthStore) AccountByPhone(_ context.Context, phone string) (auth.Account, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	for _, a := range s.W.Accounts {
		if a.Phone == phone {
			return a, nil
		}
	}
	return auth.Account{}, auth.ErrAccountNotFound
}

func (s AuthStore) AccountByID(_ context.Context, id uuid.UUID) (auth.Account, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	a, ok := s.W.Accounts[id]
	if !ok {
		return auth.Account{}, auth.ErrAccountNotFound
	}
	return a, nil
}

func (s AuthStore) CreateAccount(_ context.Context, phone string, role auth.Role, name string) (auth.Account, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	a := auth.Account{ID: uuid.New(), Phone: phone, Role: role, DisplayName: name}
	s.W.Accounts[a.ID] = a
	return a, nil
}

// ---------- consent.Store ----------

type ConsentStore struct{ W *World }

func (s ConsentStore) Append(_ context.Context, e consent.Event) error {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	s.W.Consents = append(s.W.Consents, e)
	return nil
}

func (s ConsentStore) Latest(_ context.Context, accountID, studentID uuid.UUID, scope string) (consent.Event, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	var best consent.Event
	found := false
	for _, e := range s.W.Consents {
		if e.AccountID != accountID || e.StudentID != studentID || e.Scope != scope {
			continue
		}
		// Later wins; at the same instant a refusal beats a grant.
		if !found || e.OccurredAt.After(best.OccurredAt) ||
			(e.OccurredAt.Equal(best.OccurredAt) && !e.Granted) {
			best, found = e, true
		}
	}
	if !found {
		return consent.Event{}, consent.ErrNoConsent
	}
	return best, nil
}

// ---------- parent.Store ----------

type ParentStore struct{ W *World }

func (s ParentStore) BoundStudent(_ context.Context, accountID, studentID uuid.UUID) (student.Student, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	if !s.W.bound(accountID, studentID) {
		return student.Student{}, parent.ErrNotBound
	}
	st, ok := s.W.Students[studentID]
	if !ok {
		return student.Student{}, parent.ErrNotBound
	}
	return st, nil
}

func (s ParentStore) BoundStudents(_ context.Context, accountID uuid.UUID) ([]student.Student, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	out := []student.Student{}
	for id, live := range s.W.Bindings[accountID] {
		if !live {
			continue
		}
		if st, ok := s.W.Students[id]; ok && !st.Archived() {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out, nil
}

func (s ParentStore) BoundAssessments(_ context.Context, accountID, studentID uuid.UUID, limit int) ([]student.Assessment, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	if !s.W.bound(accountID, studentID) {
		return nil, parent.ErrNotBound
	}
	out := []student.Assessment{}
	for _, a := range s.W.Assessments {
		if a.StudentID == studentID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].SessionDate.After(out[j].SessionDate) })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s ParentStore) BoundPeriodScore(_ context.Context, accountID, studentID uuid.UUID, period time.Time) (*float64, int, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	if !s.W.bound(accountID, studentID) {
		return nil, 0, parent.ErrNotBound
	}
	month := stats.Month(period)
	var sum float64
	var n int
	for _, a := range s.W.Assessments {
		if a.StudentID != studentID || a.Score == nil {
			continue
		}
		if stats.Month(a.SessionDate).Equal(month) {
			sum += *a.Score
			n++
		}
	}
	if n == 0 {
		return nil, 0, nil
	}
	mean := sum / float64(n)
	return &mean, n, nil
}

func (s ParentStore) Bind(_ context.Context, accountID uuid.UUID, codeHash string, now time.Time) (student.Student, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	code, ok := s.W.Codes[codeHash]
	if !ok || code.ConsumedAt != nil || code.RevokedAt != nil || !code.ExpiresAt.After(now) {
		return student.Student{}, parent.ErrCodeNotUsable
	}
	if s.W.bound(accountID, code.StudentID) {
		return student.Student{}, parent.ErrAlreadyBound
	}
	code.ConsumedAt = &now
	s.W.bind(accountID, code.StudentID, now)
	return s.W.Students[code.StudentID], nil
}

// ---------- teacher.Store ----------

type TeacherStore struct{ W *World }

func (s TeacherStore) Classes(_ context.Context, accountID uuid.UUID) ([]student.Class, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	out := []student.Class{}
	for id := range s.W.Classes {
		if s.W.teaches(accountID, id) {
			c := s.W.class(id)
			c.SchoolID = nil // the teacher query does not select it
			out = append(out, c)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (s TeacherStore) ClassStudents(_ context.Context, accountID, classID uuid.UUID) ([]student.Student, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	if !s.W.teaches(accountID, classID) {
		return nil, teacher.ErrNotTaught
	}
	out := []student.Student{}
	for _, st := range s.W.Students {
		if st.ClassID == classID && !st.Archived() {
			out = append(out, st)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].DisplayName < out[j].DisplayName })
	return out, nil
}

func (s TeacherStore) StudentHistory(_ context.Context, accountID, studentID uuid.UUID, limit int) ([]student.Assessment, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	st, ok := s.W.Students[studentID]
	if !ok || !s.W.teaches(accountID, st.ClassID) {
		return nil, teacher.ErrNotTaught
	}
	out := []student.Assessment{}
	for _, a := range s.W.Assessments {
		if a.StudentID == studentID {
			out = append(out, a)
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s TeacherStore) RecordAssessments(_ context.Context, accountID uuid.UUID, entries []teacher.NewAssessment) ([]uuid.UUID, int, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	// Validate the whole batch before writing any of it, mirroring the
	// transaction in the Postgres store.
	for i, e := range entries {
		st, ok := s.W.Students[e.StudentID]
		if !ok || !s.W.teaches(accountID, st.ClassID) {
			return nil, i, teacher.ErrNotTaught
		}
	}
	ids := make([]uuid.UUID, 0, len(entries))
	for _, e := range entries {
		a := student.Assessment{
			ID: uuid.New(), StudentID: e.StudentID, RecordedBy: accountID,
			SessionDate: e.SessionDate.Time(), Score: e.Score,
			Participation: e.Participation, Note: e.Note, CreatedAt: time.Now().UTC(),
		}
		s.W.Assessments = append(s.W.Assessments, a)
		ids = append(ids, a.ID)
	}
	return ids, -1, nil
}

// ---------- stats.Reader ----------

type StatsReader struct{ W *World }

func (s StatsReader) Get(_ context.Context, classID uuid.UUID, period time.Time) (stats.ClassStat, error) {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	st, ok := s.W.Stats[statKey(classID, period)]
	if !ok {
		return stats.ClassStat{}, stats.ErrNoStats
	}
	return st, nil
}

// ---------- audit.Sink ----------

type AuditSink struct{ W *World }

func (s AuditSink) Append(_ context.Context, entries []audit.Entry) error {
	s.W.mu.Lock()
	defer s.W.mu.Unlock()
	s.W.AuditLog = append(s.W.AuditLog, entries...)
	return nil
}
