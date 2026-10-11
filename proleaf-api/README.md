# proleaf-api

Backend skeleton for ProLeaf, built to `BACKEND-SKELETON.md`. Go 1.25 +
PostgreSQL 17, standard library HTTP, two dependencies.

The goal of this phase is a running service with **real auth, real schema, real
authorization boundaries and stubbed business logic**. No ProLeaf Intelligence,
no school-system integration, no client.

---

## Quickstart

```bash
cp .env.example .env
docker compose up -d db          # or point PROLEAF_DATABASE_URL at your own
go run ./cmd/api -migrate-only   # apply migrations
go run ./cmd/api -bootstrap-admin +8613800138000
go run ./cmd/api                 # listens on :8080
```

`go test ./...` needs nothing running: the authorization, consent and audit
tests drive the real router over in-memory stores.

The dev SMS sender writes the OTP to the application log instead of sending it.
It refuses to build outside `PROLEAF_ENV=dev`.

```bash
curl -XPOST localhost:8080/v1/auth/otp/request -d '{"phone":"13800138000"}'
# read the code out of the server log, then
curl -XPOST localhost:8080/v1/auth/otp/verify -d '{"phone":"13800138000","code":"123456"}'
```

No Go installed? `scripts/go.sh test ./...` runs the toolchain in Docker.

### Mock server (for the frontend)

The same router, handlers and authorization rules over in-memory stores with
seeded fake data. No database, no SMS, and the OTP is always `123456`.

```bash
go run ./cmd/mock            # http://localhost:8080/v1
docker compose up mock       # http://localhost:8090/v1, no Go needed
```

| Sign in as | Phone | What it has |
|---|---|---|
| admin | `+8613800000001` | one school |
| teacher | `+8613800000002` | two classes, eight weeks of assessments each |
| parent | `+8613800000003` | two children; `comparison` consent declined for one |
| new parent | any other number | nothing yet; bind with `MOCK-CODE-0001` or `MOCK-CODE-0002` |

Policy version is `2026-01-01`. Data resets on restart. CORS is open on the
mock only. Class aggregates are computed once at startup, so an assessment
posted to the mock does not move the comparison.

Class aggregates are computed by a job, never on the request path:

```bash
go run ./cmd/statsjob                  # current month
go run ./cmd/statsjob -period 2026-08
go run ./cmd/statsjob -retention-dry-run
```

---

## Layout

```
cmd/api/           entrypoint, wiring only (+ -migrate-only, -bootstrap-admin)
cmd/statsjob/      class_stats computation and the retention dry run
cmd/mock/          the API over in-memory fake data, for frontend work
docs/openapi.yaml  the API contract; served at /v1/openapi.yaml
internal/
  config/          env loading; secrets are required outside dev
  httpx/           JSON, error envelope, request id, recovery, access log
  db/              pool, tx helper, embedded forward-only migrations
  auth/            OTP issue/verify, HS256 tokens, RequireTeacher/Parent/Admin
  audit/           append-only access log + the middleware that flushes it
  teacher/         /teacher/*  — scoped by class_teachers
  parent/          /parent/*   — scoped by guardian_bindings
  admin/           /admin/*    — separate prefix, separate audit stream
  student/         shared domain types and pure logic; no SQL
  stats/           class aggregate computation
  consent/         consent as events, and the check every read runs
  privacy/         retention + data-subject requests (scaffold, see below)
  enroll/          enrollment code format, normalization and hashing
  intelligence/    INTERFACE ONLY; no provider, called from nowhere
  api/             router assembly + the cross-cutting tests
  testsupport/     in-memory stores and the HTTP harness
pkg/sms/           gateway adapter; log sender for dev, provider stubbed
```

`teacher/` and `parent/` do not import each other, and `admin/` imports
neither. A test enforces it (`internal/api/architecture_test.go`) — shared
logic goes in `student/` or `stats/`.

---

## The authorization boundary

This is the part that is not stubbed.

A parent reaches student data only through a live row in `guardian_bindings`,
resolved server-side on every request. The student id from the client is never
trusted.

Two things make that structural rather than remembered:

1. **Every method on `parent.Store` takes the account id** and scopes its query
   by the binding. It is not possible to write a query in that package that
   forgets the check, because there is no method that does not receive the
   account.
2. **A test parses the package** and fails if any SQL literal touching
   `students` or `assessments` does not also mention `guardian_bindings`. The
   same test covers `teacher/` against `class_teachers`. That is §4's "grep the
   parent package", as a build failure rather than a habit.

A miss is **404, never 403** — a 403 confirms that the id names a real student,
which is a disclosure on its own. A malformed id gets the same 404, so the
shape of the id is not an oracle either. A role mismatch *is* 403: there the
caller is known and the route is simply not theirs.

The negative tests §4 asks for, all in `internal/api/authorization_test.go`:

| Test | Expectation |
|---|---|
| `TestParentTokenOnTeacherRouteIsForbidden` | parent token on a teacher route → 403 |
| `TestUnboundStudentIsNotFound` | unbound student id → 404 |
| `TestRevokedBindingIsNotFound` | revoked binding → 404, immediately |
| `TestComparisonLeaksNoIdentifiers` | comparison carries no identifiers at all |

The comparison test asserts three things: none of the known ids or names
appear, **no UUID-shaped value appears at all**, and the response's keys are a
closed set. Adding a field to that response means editing the test on purpose.

---

## Endpoints

Everything is under **`/v1`** (`GET /healthz` is the one exception). The full
contract, with request and response schemas and every error code, is
`docs/openapi.yaml`, served by the running service at `/v1/openapi.yaml`.
`TestOpenAPIMatchesRegisteredRoutes` fails the build if a route and the
document disagree.

Every error, including an unknown route or a wrong method, has one shape:

```json
{ "error": { "code": "invalid", "message": "...", "details": {} }, "request_id": "..." }
```

**Public**

```
POST /auth/otp/request           { phone }                     -> 202
POST /auth/otp/verify            { phone, code }               -> token
```

Every failure mode of verify returns the same `otp_invalid` 401. Distinguishing
"expired" from "wrong" from "no such account" is a free oracle.

**Teacher** (`RequireTeacher`)

```
GET  /teacher/classes
GET  /teacher/classes/{id}/students
POST /teacher/assessments            single entry (a batch of one)
POST /teacher/assessments/bulk       { session_date, entries: [...] }
GET  /teacher/students/{id}/history
```

`bulk` is all-or-nothing and reports the index of the entry that failed. It is
built now, not later: daily entry is the input every downstream feature depends
on, and if it is tedious the dataset degrades.

**Parent** (`RequireParent`)

```
POST /parent/bind                    { enrollment_code, policy_version, accepted_scopes }
GET  /parent/children
GET  /parent/children/{id}/progress
GET  /parent/children/{id}/comparison[?period=YYYY-MM]
GET  /parent/children/{id}/consent
PUT  /parent/children/{id}/consent   { scope, granted, policy_version }
POST /parent/requests                { kind, student_id?, detail? }
```

**Admin** (`RequireAdmin`, separate path prefix and audit stream)

```
POST   /admin/schools
GET    /admin/schools
PATCH  /admin/schools/{id}

POST   /admin/classes
GET    /admin/classes[?school_id=]
GET    /admin/classes/{id}                          with its teachers
PATCH  /admin/classes/{id}                          name, academic_year, school_id
POST   /admin/classes/{id}/teachers
DELETE /admin/classes/{id}/teachers/{account_id}

POST   /admin/students
GET    /admin/students?class_id=[&include_archived=true]
GET    /admin/students/{id}                         with their guardians
PATCH  /admin/students/{id}                         rename, or move class
POST   /admin/students/{id}/archive
POST   /admin/students/{id}/restore
DELETE /admin/students/{id}/guardians/{account_id}  revoke a binding

POST   /admin/accounts                              staff only; parents come from OTP
GET    /admin/accounts[?role=&phone=&limit=]
GET    /admin/accounts/{id}
PATCH  /admin/accounts/{id}                         display_name
POST   /admin/accounts/{id}/disable
POST   /admin/accounts/{id}/enable

POST   /admin/enrollment-codes                      returns the plaintext code exactly once
POST   /admin/classes/{id}/enrollment-codes         one per student: the sheet to print
GET    /admin/enrollment-codes?student_id=|class_id=   status only, never the code
POST   /admin/enrollment-codes/{id}/revoke

GET    /admin/audit
GET    /admin/subject-requests
POST   /admin/subject-requests/{id}/status
```

An enrollment code is `active`, `consumed`, `revoked` or `expired`. It is
generated by an admin, distributed on the class sheet, expires after
`PROLEAF_ENROLLMENT_CODE_TTL`, and can be revoked until it is used. A student
may hold several live codes, one per guardian.

---

## Consent

Consent is a sequence of events, never a boolean. A refusal is a recorded
event, and so is a withdrawal.

- `/parent/bind` requires the client to name the `policy_version` it displayed.
  A mismatch is a 409: the parent was shown text that is no longer in force.
- Scopes are separate (`progress`, `comparison`), so a guardian can accept
  progress reporting and decline ranking.
- Every parent-facing student-data handler checks for a **current** grant
  before returning anything. A grant under a superseded policy version does not
  count.
- Where two events share a timestamp, the refusal wins. The tie has to break
  somewhere; the safe side is the one that withholds data.

## Audit

Append-only, enforced in the database by a trigger (migration 0002) as well as
by there being no UPDATE or DELETE path in the code.

Handlers call `audit.Record` / `audit.RecordStudent`, which append to a
per-request collector; the middleware flushes it afterwards, stamping actor,
role, request id and stream. If a handler behind a role gate returns 2xx having
recorded nothing, that is logged as an error — a coverage gap is what a
reviewer finds first. `TestEveryStudentDataRouteIsAudited` covers every route.

---

## What is deliberately not built

Per §8, and left obviously absent rather than half-done:

- Any LLM provider integration. `internal/intelligence` is an interface, a
  no-op, and a compile-time assertion in `cmd/api`. A test asserts no domain
  package imports it.
- SIS / school-system import — blocked on the company.
- Push notifications — blocked on the Flutter spike.
- Parent-facing personalised content, lesson planning, the mobile client.

Scaffolded but not finished, on purpose:

- **Retention/deletion** (`internal/privacy`). The job reports what it *would*
  touch and deletes nothing. The retention period is not agreed and deleting
  assessment history is not reversible.
- **Data-subject requests.** Intake is real — a request that arrives before the
  workflow exists still has to be recorded with its timestamp — and shows up at
  `/admin/subject-requests`. `Fulfil` returns `ErrNotImplemented`, because
  erasure has a different answer for assessments, consent events and the
  append-only audit log, and that decision is not made.

See `docs/DECISIONS.md` for the choices made where the skeleton guide left room,
and the deviations from its DDL.

---

## Definition of done

| §9 item | Status |
|---|---|
| Service runs locally against Postgres, migrations apply clean | Done — verified against PostgreSQL 17.5 |
| OTP request/verify end-to-end with a stubbed SMS adapter | Done — `TestSignInFlow` |
| Teacher authenticates, lists a class, records a bulk assessment | Done — `TestTeacherBulkEntryToParentComparison` |
| Parent binds with an enrollment code and reads a comparison | Done — same test |
| All four negative authorization tests pass | Done — table above |
| Every student-data handler writes an audit row | Done — `TestEveryStudentDataRouteIsAudited` |
| Consent check blocks data access when absent | Done — `TestAbsentConsentBlocksData`, `TestWithdrawnConsentBlocksData`, `TestStaleConsentBlocksData` |
| `intelligence.Provider` compiles with a no-op and is called nowhere | Done — `TestIntelligenceIsCalledNowhere` |

`go test ./...` covers all of this over in-memory stores and needs nothing
running. `scripts/smoke.py` covers the half those tests cannot — the actual
SQL, the transaction in bulk entry, the migrations, and the stats job writing a
row the parent endpoint then reads:

```bash
docker compose up -d db
go run ./cmd/api -migrate-only
go run ./cmd/api -bootstrap-admin +8613800000001
go run ./cmd/api > server.log 2>&1 &
python scripts/smoke.py --log server.log
```

It also checks the append-only trigger by what it refuses: `UPDATE`, `DELETE`
and `TRUNCATE` on `audit_log` all raise, while `INSERT` continues to work.
