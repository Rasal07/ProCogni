-- 0002: make audit_log append-only in the database, not only by convention.
--
-- The application role should additionally hold only INSERT and SELECT:
--
--   REVOKE UPDATE, DELETE, TRUNCATE ON audit_log FROM proleaf_app;
--   GRANT  INSERT, SELECT            ON audit_log TO   proleaf_app;
--
-- Those grants live in provisioning rather than here, because the role name is
-- environment-specific. The trigger below holds even when the grants are
-- missed, and it is the artifact to show a reviewer.

CREATE OR REPLACE FUNCTION audit_log_append_only() RETURNS trigger AS $fn$
BEGIN
    RAISE EXCEPTION 'audit_log is append-only (attempted %)', TG_OP;
END;
$fn$ LANGUAGE plpgsql;

CREATE TRIGGER audit_log_no_mutation
    BEFORE UPDATE OR DELETE OR TRUNCATE ON audit_log
    FOR EACH STATEMENT EXECUTE FUNCTION audit_log_append_only();
