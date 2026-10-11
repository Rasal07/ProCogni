# ProLeaf Backend — Skeleton Build Guide

Companion to `BACKEND-ARCHITECTURE.md` (which holds the *why*). This doc is the *what to build first* — enough structure to start writing code without locking in decisions that are still open.

**Goal of the skeleton phase:** a running service with real auth, real schema, real authorization boundaries, and stubbed business logic. No ProLeaf Intelligence. No school-system integration. No UI polish.

---

## 0. Stack assumption — flag before you start

This doc is written for **Go + PostgreSQL**. Rationale: small team, single binary deploy, straightforward audit-logging and middleware story, good Alibaba/Tencent Cloud support.

**If the team is stronger in Java/Spring, switch now** — the swap costs almost nothing at skeleton stage and a lot at month six. Everything below (schema, endpoints, boundaries) is framework-agnostic; only the directory layout changes.

Two decisions remain genuinely blocked and are deliberately **not** required for the skeleton:
- SIS/grade-data integration (waiting on the company) — skeleton assumes manual teacher entry only
- Flutter vs. native (waiting on the push-plugin spike) — skeleton is backend-only, client comes after

---

## 1. Directory layout

```
proleaf-api/
├── cmd/
│   └── api/main.go              # entrypoint, wiring only
├── internal/
│   ├── config/                  # env loading, no secrets in code
│   ├── db/
│   │   ├── migrations/          # numbered, forward-only
│   │   └── db.go                # pool, tx helpers
│   ├── auth/
│   │   ├── otp.go               # OTP issue/verify
│   │   ├── token.go             # issue/parse, role claim
│   │   └── middleware.go        # RequireTeacher / RequireParent
│   ├── audit/                   # append-only access log — see §5
│   ├── teacher/                 # handlers + service, /teacher/*
│   ├── parent/                  # handlers + service, /parent/*
│   ├── student/                 # shared student domain
│   ├── stats/                   # class aggregate computation — see §4
│   ├── consent/                 # consent records — see §6
│   └── intelligence/            # INTERFACE ONLY at this stage — see §7
├── pkg/sms/                     # gateway adapter, stubbed locally
└── docs/
```

Rule: `teacher/` and `parent/` never import each other. Shared logic goes in `student/` or `stats/`. If you find yourself wanting a cross-import, that's a signal the logic belongs in a shared package.

---

## 2. Schema — first migration

Enough to be real, not everything. Written as Postgres DDL.

```sql
-- students
CREATE TABLE students (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name    TEXT NOT NULL,
    cat_stage       SMALLINT NOT NULL CHECK (cat_stage BETWEEN 3 AND 5),
    class_id        UUID NOT NULL REFERENCES classes(id),
    date_of_birth   DATE NOT NULL,           -- drives under-14 sensitive classification
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at     TIMESTAMPTZ
);

CREATE TABLE classes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    cat_stage       SMALLINT NOT NULL,
    academic_year   TEXT NOT NULL
);

-- accounts: one row per real person, role-scoped
CREATE TABLE accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    phone           TEXT NOT NULL UNIQUE,     -- real-name verified via OTP
    role            TEXT NOT NULL CHECK (role IN ('teacher','parent','admin')),
    display_name    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled_at     TIMESTAMPTZ
);

-- the binding: which parent account may see which student
CREATE TABLE guardian_bindings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES accounts(id),
    student_id      UUID NOT NULL REFERENCES students(id),
    bound_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at      TIMESTAMPTZ,
    UNIQUE (account_id, student_id)
);

-- single-use codes issued by the school, out of band
CREATE TABLE enrollment_codes (
    code            TEXT PRIMARY KEY,         -- high-entropy, not sequential
    student_id      UUID NOT NULL REFERENCES students(id),
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    consumed_at     TIMESTAMPTZ,
    consumed_by     UUID REFERENCES accounts(id)
);

-- teacher-entered assessment records
CREATE TABLE assessments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    student_id      UUID NOT NULL REFERENCES students(id),
    recorded_by     UUID NOT NULL REFERENCES accounts(id),
    session_date    DATE NOT NULL,
    score           NUMERIC(4,2),
    participation   SMALLINT,
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- pre-computed class aggregates — parents read THIS, never students/assessments
CREATE TABLE class_stats (
    class_id        UUID NOT NULL REFERENCES classes(id),
    period          DATE NOT NULL,            -- month bucket
    mean_score      NUMERIC(4,2),
    median_score    NUMERIC(4,2),
    top_score       NUMERIC(4,2),
    student_count   SMALLINT NOT NULL,
    computed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (class_id, period)
);
```

Note `class_stats` holds **scores only** — no student ids, no foreign key to a student. That absence is the safety property. Don't add a `top_student_id` column, however convenient it seems later.

---

## 3. Endpoints — skeleton set

Stub the handlers; wire the middleware for real.

**Public**
```
POST   /auth/otp/request          { phone }
POST   /auth/otp/verify           { phone, code } -> token
```

**Teacher** (`RequireTeacher`)
```
GET    /teacher/classes
GET    /teacher/classes/:id/students
POST   /teacher/assessments                 # single entry
POST   /teacher/assessments/bulk            # build this early — see note
GET    /teacher/students/:id/history
```

**Parent** (`RequireParent`)
```
POST   /parent/bind               { enrollment_code }
GET    /parent/children           # bound students only
GET    /parent/children/:id/progress
GET    /parent/children/:id/comparison   # own score + class_stats, nothing else
```

**Admin** (`RequireAdmin`, separate path + separate audit stream)
```
POST   /admin/enrollment-codes
POST   /admin/students
GET    /admin/audit
```

Note on `/teacher/assessments/bulk`: build it in the skeleton, not later. Daily teacher entry is the input that every downstream feature depends on; if it's tedious the dataset degrades and Intelligence has nothing to work from.

---

## 4. The authorization boundary — build this properly now

This is the one thing not to stub.

**Parent handlers must resolve student access through `guardian_bindings`, every request, server-side.** Never trust a student id from the client.

```
resolveBoundStudent(accountID, requestedStudentID) (Student, error)
  -> SELECT ... FROM guardian_bindings
     WHERE account_id = $1 AND student_id = $2 AND revoked_at IS NULL
  -> not found => 404 (not 403 — don't confirm the student exists)
```

**The comparison endpoint reads `class_stats` and the bound student's own rows. Nothing else.** There is no code path from a parent context to another student's row — verify this by grepping the `parent/` package for references to `students` and `assessments` tables and confirming every one is scoped by a resolved binding.

Write the negative tests in the skeleton:
- parent token on a teacher route → 403
- parent requesting an unbound student id → 404
- revoked binding → 404
- comparison response contains no student identifiers

---

## 5. Audit logging

Append-only. Write it now, because retrofitting coverage is how gaps happen.

```sql
CREATE TABLE audit_log (
    id              BIGSERIAL PRIMARY KEY,
    actor_account   UUID NOT NULL,
    actor_role      TEXT NOT NULL,
    action          TEXT NOT NULL,         -- 'view_student','record_assessment',...
    subject_student UUID,
    request_id      TEXT NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

No UPDATE or DELETE grants on this table for the app role. Every handler touching student data writes one row. This is the first artifact an MLPS reviewer asks to see.

---

## 6. Consent records

Not a boolean. Model it as events:

```sql
CREATE TABLE consent_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES accounts(id),
    student_id      UUID NOT NULL REFERENCES students(id),
    policy_version  TEXT NOT NULL,
    scope           TEXT NOT NULL,
    granted         BOOLEAN NOT NULL,      -- refusal is a recorded event too
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
```

Skeleton requirement: `/parent/bind` must record a consent event, and student-data endpoints must check for a current granted consent before returning anything. Stub the policy text; don't stub the check.

Also scaffold (empty implementations are fine for now): retention/deletion job, and data-subject request handlers for access/correction/deletion.

---

## 7. Intelligence — interface only

Create `internal/intelligence/` with the interface and a no-op implementation. **No provider integration in the skeleton.**

```go
type Context struct {
    CATStage      int
    SessionCount  int
    RecentTrend   string
    FocusAreas    []string
}   // note: no names, no IDs, no free-text notes

type Provider interface {
    Suggest(ctx context.Context, c Context) (Suggestion, error)
}
```

The point of building this now: it forces the pseudonymization boundary into the architecture before any call sites exist. The mapping from `Context` back to a real student happens inside our own service, never at the provider.

Also encode the MOE constraint in the type names — suggestions go *to a teacher*, they are not verdicts on a student. `Suggestion`, not `Evaluation`.

---

## 8. Explicitly out of scope for the skeleton

Don't build these yet; they'll churn:

- Any LLM provider integration
- SIS/school-system import
- Push notifications (blocked on the Flutter spike)
- Parent-facing "personalised page" content
- Lesson planning
- Anything in the mobile client

---

## 9. Definition of done for this phase

- [ ] Service runs locally against Postgres, migrations apply clean
- [ ] OTP request/verify works end-to-end with a stubbed SMS adapter
- [ ] A teacher can authenticate, list a class, and record a bulk assessment
- [ ] A parent can bind with an enrollment code and read a comparison
- [ ] All four negative authorization tests in §4 pass
- [ ] Every student-data handler writes an audit row
- [ ] Consent check blocks data access when absent
- [ ] `intelligence.Provider` compiles with a no-op impl and is called nowhere

---

## 10. First-week order

1. Migrations + db wiring
2. OTP auth + token + role middleware
3. Teacher endpoints (stubs → real)
4. Binding flow + consent event
5. `class_stats` computation job + parent comparison
6. Audit middleware across all of the above
7. Negative authorization tests
8. `intelligence` interface stub

Do **not** start 5 before 4 is enforcing bindings properly — the comparison endpoint is the one with real leak potential, and it should be written against a working binding check rather than retrofitted onto one.
