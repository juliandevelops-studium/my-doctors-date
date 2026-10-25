package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/juliandevelops-studium/my-doctors-date/services/database_middleware/internal/api"
)

func toEmail(s string) api.Email { return api.Email(s) }

// TokenCheck reports whether a token is still valid: the account is active, the token was issued after
// sessions_valid_after (second granularity, JWT iat has no fraction) and its jti is not revoked.
func (s *Store) TokenCheck(ctx context.Context, p api.TokenCheckParams) (api.TokenCheckResult, error) {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM users u
		WHERE u.id = $1 AND u.deleted_at IS NULL AND u.status = 'active'
		  AND $2::timestamptz >= date_trunc('second', u.sessions_valid_after)
		  AND NOT EXISTS (SELECT 1 FROM token_revocations t WHERE t.jti = $3))`, p.Sub, p.Iat, p.Jti).Scan(&ok)
	return api.TokenCheckResult{Valid: ok}, mapErr(err)
}

// TokenRevoke records a revoked jti until it would expire anyway.
func (s *Store) TokenRevoke(ctx context.Context, p api.TokenRevokeParams) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO token_revocations (jti, user_id, expires_at) VALUES ($1, $2, $3) ON CONFLICT (jti) DO NOTHING`,
		p.Jti, p.UserId, p.ExpiresAt)
	return refAsNotFound(err)
}

// EmailTokenCreate stores a token hash. Older unused tokens of the same purpose become unusable.
func (s *Store) EmailTokenCreate(ctx context.Context, p api.EmailTokenCreateParams) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `UPDATE email_tokens SET used_at = now() WHERE user_id = $1 AND purpose = $2 AND used_at IS NULL`,
			p.UserId, string(p.Purpose)); err != nil {
			return mapErr(err)
		}
		var newEmail *string
		if p.NewEmail != nil {
			e := string(*p.NewEmail)
			newEmail = &e
		}
		_, err := tx.Exec(ctx, `INSERT INTO email_tokens (user_id, purpose, token_hash, new_email, expires_at) VALUES ($1, $2, $3, $4, $5)`,
			p.UserId, string(p.Purpose), p.TokenHash, newEmail, p.ExpiresAt)
		return refAsNotFound(err)
	})
}

// EmailTokenConsume atomically marks an unused, unexpired token as used.
func (s *Store) EmailTokenConsume(ctx context.Context, p api.EmailTokenConsumeParams) (api.EmailTokenResult, error) {
	var res api.EmailTokenResult
	var newEmail *string
	err := s.pool.QueryRow(ctx, `UPDATE email_tokens SET used_at = now()
		WHERE token_hash = $1 AND purpose = $2 AND used_at IS NULL AND expires_at > now()
		RETURNING user_id, new_email`, p.TokenHash, string(p.Purpose)).Scan(&res.UserId, &newEmail)
	if err != nil {
		return res, mapErr(err)
	}
	if newEmail != nil {
		e := toEmail(*newEmail)
		res.NewEmail = &e
	}
	return res, nil
}

// MfaSecretUpsert stores the secret of an unfinished setup. A confirmed secret is never overwritten: MFA has to be
// removed first (decision 22), otherwise the answer is 409 mfa_active.
func (s *Store) MfaSecretUpsert(ctx context.Context, p api.MfaSecretParams) error {
	tag, err := s.pool.Exec(ctx, `INSERT INTO mfa_secrets (user_id, secret_enc) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET secret_enc = EXCLUDED.secret_enc WHERE mfa_secrets.confirmed_at IS NULL`, p.UserId, p.SecretEnc)
	if err != nil {
		return refAsNotFound(err)
	}
	if tag.RowsAffected() == 0 {
		return conflict("mfa_active", "MFA is already active, remove it first")
	}
	return nil
}

// MfaSecretGet returns the encrypted secret and whether it is confirmed.
func (s *Store) MfaSecretGet(ctx context.Context, id api.Uuid) (api.MfaSecretRecord, error) {
	var rec api.MfaSecretRecord
	err := s.pool.QueryRow(ctx, `SELECT secret_enc, confirmed_at IS NOT NULL FROM mfa_secrets WHERE user_id = $1`, id).
		Scan(&rec.SecretEnc, &rec.Confirmed)
	return rec, mapErr(err)
}

// MfaSecretConfirm marks an unfinished setup as confirmed. Nothing to confirm (or already confirmed) is ErrNotFound.
func (s *Store) MfaSecretConfirm(ctx context.Context, id api.Uuid) error {
	return s.execOne(ctx, `UPDATE mfa_secrets SET confirmed_at = now() WHERE user_id = $1 AND confirmed_at IS NULL`, id)
}

// MfaUseStep accepts a TOTP time step exactly once: it succeeds only if the step is newer than every step used before.
func (s *Store) MfaUseStep(ctx context.Context, p api.MfaStepParams) (api.MfaStepResult, error) {
	var exists bool
	if err := s.pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM mfa_secrets WHERE user_id = $1)`, p.UserId).Scan(&exists); err != nil {
		return api.MfaStepResult{}, mapErr(err)
	}
	if !exists {
		return api.MfaStepResult{}, ErrNotFound
	}
	tag, err := s.pool.Exec(ctx, `UPDATE mfa_secrets SET last_step = $2 WHERE user_id = $1 AND last_step < $2`, p.UserId, p.Step)
	if err != nil {
		return api.MfaStepResult{}, mapErr(err)
	}
	return api.MfaStepResult{Accepted: tag.RowsAffected() == 1}, nil
}

// MfaDelete hard-deletes the TOTP secret and all recovery codes.
func (s *Store) MfaDelete(ctx context.Context, id api.Uuid) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM mfa_secrets WHERE user_id = $1`, id); err != nil {
			return mapErr(err)
		}
		_, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, id)
		return mapErr(err)
	})
}

// RecoveryCodesReplace swaps all recovery codes for new hashes.
func (s *Store) RecoveryCodesReplace(ctx context.Context, p api.RecoveryCodesParams) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `DELETE FROM recovery_codes WHERE user_id = $1`, p.UserId); err != nil {
			return mapErr(err)
		}
		_, err := tx.Exec(ctx, `INSERT INTO recovery_codes (user_id, code_hash) SELECT $1, unnest($2::text[])`, p.UserId, p.CodeHashes)
		return refAsNotFound(err)
	})
}

// RecoveryCodeConsume uses up one recovery code atomically.
func (s *Store) RecoveryCodeConsume(ctx context.Context, p api.RecoveryCodeConsumeParams) (api.ConsumeResult, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE recovery_codes SET used_at = now() WHERE user_id = $1 AND code_hash = $2 AND used_at IS NULL`, p.UserId, p.CodeHash)
	if err != nil {
		return api.ConsumeResult{}, mapErr(err)
	}
	return api.ConsumeResult{Consumed: tag.RowsAffected() == 1}, nil
}
