-- 0001_init: core schema.
--
-- Ordering note: classes is created before students, because students
-- references it.

CREATE EXTENSION IF NOT EXISTS pgcrypto;

CREATE TABLE classes (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    cat_stage       SMALLINT NOT NULL CHECK (cat_stage BETWEEN 3 AND 5),
    academic_year   TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TABLE students (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    display_name    TEXT NOT NULL,
    cat_stage       SMALLINT NOT NULL CHECK (cat_stage BETWEEN 3 AND 5),
    class_id        UUID NOT NULL REFERENCES classes(id),
    date_of_birth   DATE NOT NULL,           -- drives under-14 sensitive classification
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    archived_at     TIMESTAMPTZ
);
CREATE INDEX students_class_idx ON students (class_id) WHERE archived_at IS NULL;

-- accounts: one row per real person, role-scoped
CREATE TABLE accounts (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    phone           TEXT NOT NULL UNIQUE,     -- real-name verified via OTP
    role            TEXT NOT NULL CHECK (role IN ('teacher','parent','admin')),
    display_name    TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    disabled_at     TIMESTAMPTZ
);

-- which teacher may act on which class. Not in the skeleton doc DDL, but
-- GET /teacher/classes has no answer without it.
CREATE TABLE class_teachers (
    class_id        UUID NOT NULL REFERENCES classes(id),
    account_id      UUID NOT NULL REFERENCES accounts(id),
    assigned_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    unassigned_at   TIMESTAMPTZ,
    PRIMARY KEY (class_id, account_id)
);
CREATE INDEX class_teachers_account_idx ON class_teachers (account_id) WHERE unassigned_at IS NULL;

-- the binding: which parent account may see which student
CREATE TABLE guardian_bindings (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES accounts(id),
    student_id      UUID NOT NULL REFERENCES students(id),
    bound_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    revoked_at      TIMESTAMPTZ,
    UNIQUE (account_id, student_id)
);
CREATE INDEX guardian_bindings_account_idx ON guardian_bindings (account_id) WHERE revoked_at IS NULL;

-- single-use codes issued by the school, out of band.
--
-- Deviation from the skeleton doc: the primary key holds a keyed hash of the
-- code, not the code itself. The plaintext is shown to the issuing admin once
-- and never stored, so a dump of this table does not hand an attacker the
-- ability to bind to arbitrary students.
CREATE TABLE enrollment_codes (
    code_hash       TEXT PRIMARY KEY,
    student_id      UUID NOT NULL REFERENCES students(id),
    issued_by       UUID REFERENCES accounts(id),
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    consumed_at     TIMESTAMPTZ,
    consumed_by     UUID REFERENCES accounts(id)
);
CREATE INDEX enrollment_codes_student_idx ON enrollment_codes (student_id);

-- teacher-entered assessment records
CREATE TABLE assessments (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    student_id      UUID NOT NULL REFERENCES students(id),
    recorded_by     UUID NOT NULL REFERENCES accounts(id),
    session_date    DATE NOT NULL,
    score           NUMERIC(4,2) CHECK (score >= 0 AND score <= 100),
    participation   SMALLINT CHECK (participation BETWEEN 0 AND 5),
    note            TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX assessments_student_date_idx ON assessments (student_id, session_date DESC);

-- pre-computed class aggregates. Parents read THIS, never students/assessments.
-- Scores only: no student ids, no foreign key to a student. That absence is the
-- safety property. Do not add a top_student_id column.
CREATE TABLE class_stats (
    class_id        UUID NOT NULL REFERENCES classes(id),
    period          DATE NOT NULL,            -- month bucket, always day 01
    mean_score      NUMERIC(4,2),
    median_score    NUMERIC(4,2),
    top_score       NUMERIC(4,2),
    student_count   SMALLINT NOT NULL,
    computed_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (class_id, period)
);

CREATE TABLE audit_log (
    id              BIGSERIAL PRIMARY KEY,
    actor_account   UUID NOT NULL,
    actor_role      TEXT NOT NULL,
    action          TEXT NOT NULL,         -- 'view_student','record_assessment',...
    subject_student UUID,
    request_id      TEXT NOT NULL,
    -- 'app' for teacher/parent traffic, 'admin' for the admin surface. The
    -- skeleton doc asks for a separate admin audit stream; one table with a
    -- stream discriminator keeps a single append-only guarantee to defend.
    stream          TEXT NOT NULL DEFAULT 'app' CHECK (stream IN ('app','admin')),
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX audit_log_subject_idx ON audit_log (subject_student, occurred_at DESC);
CREATE INDEX audit_log_actor_idx ON audit_log (actor_account, occurred_at DESC);
CREATE INDEX audit_log_stream_idx ON audit_log (stream, occurred_at DESC);

-- consent is a sequence of events, not a boolean. A refusal is recorded too.
CREATE TABLE consent_events (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES accounts(id),
    student_id      UUID NOT NULL REFERENCES students(id),
    policy_version  TEXT NOT NULL,
    scope           TEXT NOT NULL,
    granted         BOOLEAN NOT NULL,
    occurred_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX consent_events_lookup_idx
    ON consent_events (account_id, student_id, scope, occurred_at DESC);

-- OTP challenges. The code is stored as a keyed hash; the plaintext only ever
-- exists in the SMS payload.
CREATE TABLE otp_challenges (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    phone           TEXT NOT NULL,
    code_hash       TEXT NOT NULL,
    issued_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
    expires_at      TIMESTAMPTZ NOT NULL,
    attempts        SMALLINT NOT NULL DEFAULT 0,
    consumed_at     TIMESTAMPTZ
);
CREATE INDEX otp_challenges_phone_idx ON otp_challenges (phone, issued_at DESC);

-- Data-subject requests (access / correction / deletion) and the retention job
-- both need somewhere to land. Handlers are scaffolded; the workflow is not.
CREATE TABLE subject_requests (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id      UUID NOT NULL REFERENCES accounts(id),
    student_id      UUID REFERENCES students(id),
    kind            TEXT NOT NULL CHECK (kind IN ('access','correction','deletion')),
    status          TEXT NOT NULL DEFAULT 'received'
                        CHECK (status IN ('received','in_progress','fulfilled','refused')),
    detail          TEXT,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    resolved_at     TIMESTAMPTZ
);
