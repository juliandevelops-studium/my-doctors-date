CREATE TABLE config (
    key        text PRIMARY KEY,
    value_int  integer     NOT NULL CHECK (value_int > 0),
    updated_by uuid,
    updated_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO config (key, value_int) VALUES
    ('retention_a_days', 30),   -- A: cancelled/changed booking -> hard delete
    ('retention_b_days', 365),  -- B: completed booking -> reason/info emptied
    ('retention_c_days', 730),  -- C: completed booking -> record deleted
    ('retention_d_days', 30),   -- D: deactivated account -> final deletion
    ('retention_e_days', 90);   -- E: audit log

-- No appointment contents and no personal data in the payload.
-- actor_user_id has no foreign key so entries survive the final deletion of a user.
CREATE TABLE audit_log (
    id             bigserial PRIMARY KEY,
    occurred_at    timestamptz NOT NULL DEFAULT now(),
    actor_user_id  uuid,
    action         text        NOT NULL CHECK (char_length(action) BETWEEN 1 AND 100),
    data_category  text,
    target_user_id uuid,
    affected_count integer CHECK (affected_count IS NULL OR affected_count >= 0)
);
CREATE INDEX audit_log_occurred_at_idx ON audit_log (occurred_at); -- daily purge job
