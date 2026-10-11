-- 0003: schools, and the class -> school link.
--
-- school_id is nullable because classes created before this migration have no
-- school to point at. Tighten it to NOT NULL once every class has been filed.

CREATE TABLE schools (
    id              UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    name            TEXT NOT NULL,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now()
);

ALTER TABLE classes ADD COLUMN school_id UUID REFERENCES schools(id);
CREATE INDEX classes_school_idx ON classes (school_id);
