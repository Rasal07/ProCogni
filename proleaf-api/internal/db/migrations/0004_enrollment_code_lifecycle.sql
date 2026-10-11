-- 0004: enrollment codes can be listed and revoked.
--
-- The primary key stays the keyed hash. The id is what an admin names when
-- revoking: the hash is not something to put in a URL, and the plaintext is
-- not stored at all.

ALTER TABLE enrollment_codes
    ADD COLUMN id          UUID NOT NULL DEFAULT gen_random_uuid() UNIQUE,
    ADD COLUMN revoked_at  TIMESTAMPTZ,
    ADD COLUMN revoked_by  UUID REFERENCES accounts(id);
