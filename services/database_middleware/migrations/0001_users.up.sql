CREATE TABLE users (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    first_name           text        NOT NULL CHECK (char_length(first_name) BETWEEN 1 AND 100),
    last_name            text        NOT NULL CHECK (char_length(last_name) BETWEEN 1 AND 100),
    email                text        NOT NULL CHECK (char_length(email) BETWEEN 3 AND 254),
    phone                text        CHECK (phone IS NULL OR char_length(phone) <= 30),
    password_hash        text        NOT NULL CHECK (password_hash LIKE '$argon2id$%'),
    email_verified_at    timestamptz,
    status               text        NOT NULL DEFAULT 'pending'
                                     CHECK (status IN ('pending', 'active', 'locked', 'disabled')),
    failed_attempts      integer     NOT NULL DEFAULT 0 CHECK (failed_attempts >= 0),
    locked_until         timestamptz,
    permanently_locked   boolean     NOT NULL DEFAULT false,
    must_change_password boolean     NOT NULL DEFAULT false,
    sessions_valid_after timestamptz NOT NULL DEFAULT now(),
    deleted_at           timestamptz,
    created_at           timestamptz NOT NULL DEFAULT now(),
    updated_at           timestamptz NOT NULL DEFAULT now()
);

-- Case-insensitive uniqueness. Pseudonymised accounts get a unique placeholder email.
CREATE UNIQUE INDEX users_email_lower_key ON users (lower(email));
CREATE INDEX users_deleted_at_idx ON users (deleted_at) WHERE deleted_at IS NOT NULL;

CREATE TABLE user_roles (
    user_id uuid NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    role    text NOT NULL CHECK (role IN ('patient', 'doctor', 'desk', 'organisator', 'systemadmin')),
    PRIMARY KEY (user_id, role)
);
CREATE INDEX user_roles_role_idx ON user_roles (role);

CREATE TABLE mfa_secrets (
    user_id       uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    secret_enc    bytea       NOT NULL, -- TOTP secret, encrypted by user_service (AES-GCM, nonce prepended)
    confirmed_at  timestamptz,          -- NULL while setup is not completed (UF-04)
    -- RFC 6238: a TOTP code must not be accepted twice. last_step is the highest time step that was used successfully.
    last_step     bigint      NOT NULL DEFAULT 0 CHECK (last_step >= 0),
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE recovery_codes (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    code_hash  text        NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX recovery_codes_user_idx ON recovery_codes (user_id);

-- Revoked JWTs (logout, password reset, account deletion). Rows are purged after expires_at.
CREATE TABLE token_revocations (
    jti        uuid PRIMARY KEY,
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    revoked_at timestamptz NOT NULL DEFAULT now(),
    expires_at timestamptz NOT NULL
);
CREATE INDEX token_revocations_user_idx    ON token_revocations (user_id);
CREATE INDEX token_revocations_expires_idx ON token_revocations (expires_at); -- daily purge job

-- Only hashes are stored; the raw token exists only in the e-mail link.
CREATE TABLE email_tokens (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id    uuid        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    purpose    text        NOT NULL CHECK (purpose IN ('verify', 'reset', 'email_change')),
    token_hash text        NOT NULL UNIQUE,
    new_email  text,
    expires_at timestamptz NOT NULL,
    used_at    timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    CHECK (purpose = 'email_change' OR new_email IS NULL)
);
CREATE INDEX email_tokens_user_idx    ON email_tokens (user_id);
CREATE INDEX email_tokens_expires_idx ON email_tokens (expires_at); -- daily purge job
