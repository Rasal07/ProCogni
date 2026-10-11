// Package audit is the append-only access log.
//
// Handlers do not write rows directly. They call Record, which appends to a
// per-request collector; the middleware flushes the collector once the handler
// has returned, stamping actor and request id from context. That keeps the
// call site to one line and makes "did this handler audit?" checkable.
package audit

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Streams. The admin surface writes its own so it can be read, retained and
// alerted on separately from ordinary teacher/parent traffic.
const (
	StreamApp   = "app"
	StreamAdmin = "admin"
)

// Actions. Keep this list closed: an audit log you have to grep with a regex
// is one nobody reads.
const (
	ActionListClasses       = "list_classes"
	ActionListClassStudents = "list_class_students"
	ActionViewStudent       = "view_student"
	ActionRecordAssessment  = "record_assessment"
	ActionBindGuardian      = "bind_guardian"
	ActionListChildren      = "list_children"
	ActionViewProgress      = "view_progress"
	ActionViewComparison    = "view_comparison"
	ActionGrantConsent      = "grant_consent"
	ActionWithdrawConsent   = "withdraw_consent"
	ActionSubjectRequest    = "subject_request"

	ActionIssueEnrollmentCode  = "issue_enrollment_code"
	ActionRevokeEnrollmentCode = "revoke_enrollment_code"
	ActionCreateStudent        = "create_student"
	ActionUpdateStudent        = "update_student"
	ActionArchiveStudent       = "archive_student"
	ActionRestoreStudent       = "restore_student"
	ActionRevokeBinding        = "revoke_binding"
	ActionCreateAccount        = "create_account"
	ActionUpdateAccount        = "update_account"
	ActionDisableAccount       = "disable_account"
	ActionEnableAccount        = "enable_account"
	ActionManageSchool         = "manage_school"
	ActionManageClass          = "manage_class"
	ActionAdminRead            = "admin_read"
	ActionReadAudit            = "read_audit"
)

type Entry struct {
	ActorAccount   uuid.UUID  `json:"actor_account"`
	ActorRole      string     `json:"actor_role"`
	Action         string     `json:"action"`
	SubjectStudent *uuid.UUID `json:"subject_student"`
	RequestID      string     `json:"request_id"`
	Stream         string     `json:"stream"`
	OccurredAt     time.Time  `json:"occurred_at"`
}

// Sink persists entries. Append is called once per request with everything the
// handler recorded.
type Sink interface {
	Append(ctx context.Context, entries []Entry) error
}

type ctxKey int

const collectorKey ctxKey = iota

type collector struct {
	mu      sync.Mutex
	entries []Entry
}

func (c *collector) add(action string, subject *uuid.UUID) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, Entry{
		Action:         action,
		SubjectStudent: subject,
		OccurredAt:     time.Now().UTC(),
	})
}

func (c *collector) drain() []Entry {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := c.entries
	c.entries = nil
	return out
}

// Record notes an action against the current request. It is deliberately
// infallible: an audit write must never be the reason a handler returns an
// error to a teacher mid-entry. Flush failures are logged loudly instead.
func Record(ctx context.Context, action string) {
	if c, ok := ctx.Value(collectorKey).(*collector); ok {
		c.add(action, nil)
	}
}

// RecordStudent notes an action against a specific student.
func RecordStudent(ctx context.Context, action string, studentID uuid.UUID) {
	if c, ok := ctx.Value(collectorKey).(*collector); ok {
		id := studentID
		c.add(action, &id)
	}
}

// Recorded reports how many entries the current request has accumulated. Tests
// use it; so does the middleware, to catch a handler that forgot.
func Recorded(ctx context.Context) int {
	c, ok := ctx.Value(collectorKey).(*collector)
	if !ok {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
