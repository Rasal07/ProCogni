# Skeleton-phase decisions

`BACKEND-SKELETON.md` left several things open. This records what was chosen,
why, and what it would cost to change. It also lists the places where the code
departs from the guide's DDL, so a reader comparing the two is not surprised.

---

## 1. Stack

**Go + PostgreSQL, as written in §0.** Not switched to Java/Spring: nothing in
the requirements pulls towards the JVM, and the guide's rationale (single
binary, straightforward middleware and audit story) holds.

Within Go, the choices were all towards fewer moving parts:

| | Chosen | Why |
|---|---|---|
| Router | `net/http.ServeMux` | Go 1.22+ patterns cover `GET /teacher/classes/{id}/students`. A framework earns nothing here. |
| DB | `jackc/pgx/v5` | The de facto Postgres driver. Used through a narrow `db.Conn` interface so handlers can run inside a caller's transaction. |
| Tokens | Hand-rolled HS256 | ~80 lines of standard library. One algorithm, no negotiation, and `alg: none` cannot happen. A test asserts it. |
| Migrations | Embedded SQL + a small runner | Numbered, forward-only, advisory-locked, one transaction each. No tool to install. |
| IDs | `google/uuid` | Matches the schema. |

Total non-stdlib dependencies: two.

## 2. Storage behind interfaces

Each domain package declares a narrow `Store` interface and keeps its SQL in
`store_pg.go`. This is not an abstraction for its own sake — it buys two things
the skeleton phase specifically needs:

- The authorization and consent boundaries are testable end-to-end over real
  HTTP without a database, so §4's negative tests run in CI from day one.
- **Every method takes the acting account id.** That is the structural part: a
  query that forgets the binding cannot be written, because there is no method
  that does not receive the account to scope by.

The SQL still carries the join itself. Service-level check and query-level
scope are both present, deliberately.

## 3. Deviations from the guide's DDL

Three changes, each one flagged in the migration:

**`enrollment_codes.code` → `code_hash`.** The primary key holds a keyed hash;
the plaintext is shown to the issuing admin once and never stored. A dump of
that table otherwise hands an attacker the ability to bind to any student, and
the codes are the one credential in the system issued out of band and printed
on paper. Cost of the change: a lost code is reissued rather than looked up.

**`class_teachers` added.** `GET /teacher/classes` has no answer without a link
between teacher accounts and classes, and neither does any scoping of
assessment entry. `(class_id, account_id)` with an `unassigned_at` tombstone,
so removing a teacher is reversible and auditable.

**`audit_log.stream` added.** §3 asks for a separate admin audit stream. One
table with a `stream` discriminator keeps a single append-only guarantee to
defend and a single trigger to show a reviewer, rather than two tables that can
drift.

Also added, all supporting tables the guide implies rather than specifies:
`otp_challenges`, `subject_requests`, and a `classes.created_at`.

## 4. Decisions the guide left open

**Role assignment at sign-in.** An unknown phone verifying an OTP becomes a
**parent**. Teacher and admin accounts are provisioned via `POST
/admin/accounts` and must exist before their holder can sign in. Self-service
sign-in can therefore never mint elevated access. The first admin comes from
`api -bootstrap-admin <phone>`, which runs from an operator's shell and exits
without serving traffic.

**Small-cohort suppression.** `class_stats` rows are computed for every class
but published only when the cohort is at least `PROLEAF_STATS_MIN_COHORT`
(default 5). A mean over three students is close enough to an individual score
to be one, and the comparison endpoint is the one with real leak potential. A
suppressed row is still written with its `student_count`, so the parent
endpoint can distinguish "too small to show" from "not computed yet" — two
different things to tell a parent.

**The comparison response shape.** No student id, no class id, no name, no
teacher. Not even the id of the child the request was *about*: the client
already knows which child it asked for, and echoing it back is the seam through
which a `top_student_id` would eventually arrive. The test asserts no
UUID-shaped value appears anywhere in the body and that the key set is closed.

**Consent scopes.** `progress` and `comparison`, granted and withdrawn
independently. Some guardians accept progress reporting while objecting to
ranking, and one boolean cannot express that. `/parent/bind` requires the
client to name the policy version it displayed; a mismatch is a 409.

**404 vs 403.** Object-level misses inside `parent/` and `teacher/` are 404,
including malformed ids, so neither existence nor id shape is an oracle. Role
mismatches at the route gate are 403 — the caller is known and the route is
simply not theirs.

**Bulk entry semantics.** All-or-nothing in one transaction, capped at 500
entries, with the index of the failing entry reported back. A per-entry
`session_date` overrides a batch default, so a teacher catching up on two days
sends two batches or one with overrides. The single-entry endpoint is a batch
of one: one write path to reason about.

**Consent tie-break.** Where two consent events share a timestamp, the refusal
wins. The tie has to break somewhere and the safe side is the one that
withholds data.

## 5. Still blocked, per the guide

- **SIS / grade-data integration** — waiting on the company. The skeleton
  assumes manual teacher entry only. `assessments.recorded_by` already points
  at an account, so an import would need a service account and a provenance
  column, not a reshape.
- **Flutter vs. native** — waiting on the push-plugin spike. Backend only; no
  push machinery is present.
- **Retention period.** Not agreed, so the retention job reports and deletes
  nothing. `statsjob -retention-dry-run` prints what a run would touch.
- **Data-subject request fulfilment.** Intake is real and timestamped;
  fulfilment returns `ErrNotImplemented`. Erasure has a different answer for
  assessments, consent events and the append-only audit log, and choosing them
  is a policy question, not a coding one.

## 6. Operational notes for the reviewer

- `audit_log` is append-only by trigger (migration 0002). The application role
  should additionally hold only `INSERT` and `SELECT`; the `GRANT` lives in
  provisioning because the role name is environment-specific, and the exact
  statements are in the migration's comment.
- Secrets (`PROLEAF_TOKEN_SECRET`, `PROLEAF_OTP_PEPPER`,
  `PROLEAF_ENROLLMENT_CODE_PEPPER`) are required outside `PROLEAF_ENV=dev` and
  must be at least 32 bytes. In dev they are generated per process, so every
  token dies at restart — unusable anywhere else by construction.
- The `log` SMS sender refuses to build outside dev.
- Phone numbers are redacted to the last four digits in logs. OTP codes are
  never logged outside the dev sender.
- `students.date_of_birth` is never serialised to any client. It exists to
  drive the under-14 sensitive-information classification.

## 7. October 2026: contract, mock, admin, enrollment codes

**`/v1` only.** Every route moved under `/v1`; the unversioned paths were
removed rather than kept as aliases, since no client had shipped against them.
Packages still register `/teacher/...` and never see the prefix, so `/v2` is a
second mount in `internal/api/router.go`, not a rename.

**One error shape, including the router's.** The 404 and 405 that `net/http`
produces on its own were `text/plain`. `httpx.Enveloped` replaces them with the
envelope, so a client has one decoder for every non-2xx.

**The spec is hand-written and checked.** `docs/openapi.yaml` is not generated.
A test reads the route patterns out of the source and fails if either side has
a route the other lacks. It does not check schemas; those are kept honest by
review and by the frontend using them.

**The mock is the real service.** `cmd/mock` wires the production router over
the in-memory stores the tests already use, rather than a second, hand-faked
server that would drift. The cost is that a new store method needs its
in-memory twin, which the tests required anyway.

**Schools.** `classes.school_id` is nullable, because classes existed before
schools did. Admin accounts are global, not per school: nothing yet says a
school has its own administrators. Both are the things to revisit if a second
school is onboarded with its own office staff.

**A student's stage follows their class.** `PATCH /admin/students/{id}` with a
`class_id` sets `cat_stage` to the new class's, so the "student stage equals
class stage" invariant survives a mid-year move without the caller restating
it. A class's own stage cannot be changed.

**Admin validation errors.** Only `httpx.Invalid` becomes a 400. Previously any
plain error from the admin service did, which echoed database error text to
the client as a validation message.

**Enrollment code lifecycle.** Codes gained an `id` (to revoke by; the hash is
not URL material) and `revoked_at`. "Distribute" is the class sheet: one call
returns a named code per student for the school to print. Sending codes by SMS
waits for a real SMS provider. Issuing a code does not revoke a student's
earlier ones, because two guardians need two codes.

**Score precision (migration 0005).** `NUMERIC(4,2)` could not hold 100.00
although the API and the CHECK constraint allow it; a full-marks entry failed
at the database. Widened to `NUMERIC(5,2)`.

**Known wart, left alone.** `session_date` goes in as `YYYY-MM-DD` and comes
out as a midnight-UTC timestamp. The spec documents it as it is. Changing the
output to a plain date is a contract change to make before the frontend's
Week 7, or not at all.
