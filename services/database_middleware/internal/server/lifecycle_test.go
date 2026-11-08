package server_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestAccountDelete(t *testing.T) {
	e := newEnv(t)
	f := newFixture(e)
	id, email := e.newUser("active", "patient")
	e.ok("user_service", "mfaSecretUpsert", obj{"user_id": id, "secret_enc": "c2VjcmV0"}, 204)
	future := e.book("calendar_service", id, f.doc, f.svc, slot(), 201)
	past := e.book("calendar_service", id, f.doc, f.svc, time.Now().Add(-72*time.Hour).Truncate(time.Hour), 201)

	res := e.ok("user_service", "accountDelete", obj{"user_id": id}, 200)
	if res["cancelled_bookings"].(float64) != 1 || res["anonymised_bookings"].(float64) != 1 {
		t.Fatalf("result: %v", res)
	}
	e.expect("user_service", "userGetByEmail", obj{"email": email}, 404, "not_found")
	e.expect("calendar_service", "bookingGet", obj{"booking_id": future["id"]}, 404, "not_found") // cancelled
	kept := e.ok("calendar_service", "bookingGet", obj{"booking_id": past["id"]}, 200)
	if kept["patient_id"] != nil || kept["patient_email"] != nil {
		t.Fatalf("a past booking must lose the patient link: %v", kept)
	}
	e.expect("calendar_service", "tokenCheck", obj{"jti": uuid.NewString(), "sub": id, "iat": ts(time.Now())}, 200, "")
	if e.ok("calendar_service", "tokenCheck", obj{"jti": uuid.NewString(), "sub": id, "iat": ts(time.Now())}, 200)["valid"] == true {
		t.Fatal("a deleted account must not validate")
	}
	var first, mail string
	var n int
	if err := e.pool.QueryRow(context.Background(), `SELECT first_name, email, (SELECT count(*) FROM mfa_secrets WHERE user_id = $1) FROM users WHERE id = $1`, id).Scan(&first, &mail, &n); err != nil {
		t.Fatal(err)
	}
	if first != "Gelöscht" || mail == email || n != 0 {
		t.Fatalf("account not pseudonymised: %s %s %d", first, mail, n)
	}
}

func TestRetentionSettings(t *testing.T) {
	e := newEnv(t)
	admin, _ := e.newUser("active", "systemadmin")
	cur := e.ok("user_service", "retentionGet", nil, 200)
	e.ok("purge_job", "retentionGet", nil, 200)
	e.expect("calendar_service", "retentionGet", nil, 403, "forbidden")

	set := func(b, c float64) (int, obj) {
		return e.call("user_service", "retentionSet", obj{
			"retention_a_days": 30, "retention_b_days": b, "retention_c_days": c, "retention_d_days": 30, "retention_e_days": 90, "updated_by": admin,
		})
	}
	if got, out := set(400, 300); got != 422 || out["code"] != "c_before_b" {
		t.Fatalf("C < B must be rejected: %d %v", got, out)
	}
	if got, out := set(365, 730); got != 200 || out["retention_c_days"].(float64) != 730 {
		t.Fatalf("valid settings: %d %v", got, out)
	}
	_ = cur
}

func TestPurge(t *testing.T) {
	e := newEnv(t)
	f := newFixture(e)
	ctx := context.Background()

	// A: cancelled booking older than retention A is deleted, a fresh one stays.
	oldB := e.book("calendar_service", f.patA, f.doc, f.svc, slot(), 201)
	newB := e.book("calendar_service", f.patA, f.doc, f.svc, slot(), 201)
	e.ok("calendar_service", "bookingCancel", obj{"booking_id": oldB["id"]}, 200)
	e.ok("calendar_service", "bookingCancel", obj{"booking_id": newB["id"]}, 200)
	exec(t, e, `UPDATE bookings SET deleted_at = now() - interval '400 days' WHERE id = $1`, oldB["id"])
	res := e.ok("purge_job", "purgeExpired", obj{"category": "bookings_cancelled"}, 200)
	if res["affected_count"].(float64) < 1 {
		t.Fatalf("expected a purge: %v", res)
	}
	if count(t, e, `SELECT count(*) FROM bookings WHERE id = $1`, oldB["id"]) != 0 || count(t, e, `SELECT count(*) FROM bookings WHERE id = $1`, newB["id"]) != 1 {
		t.Fatal("only the expired cancelled booking may be deleted")
	}

	// B and C: completed bookings lose their contents, then the row.
	done := e.book("calendar_service", f.patA, f.doc, f.svc, time.Now().Add(-5*time.Hour).Truncate(time.Hour), 201)
	exec(t, e, `UPDATE bookings SET status = 'abgeschlossen', reason = 'Husten', info = 'x', starts_at = now() - interval '900 days', ends_at = now() - interval '900 days' + interval '30 minutes' WHERE id = $1`, done["id"])
	e.ok("purge_job", "purgeExpired", obj{"category": "bookings_completed_content"}, 200)
	if count(t, e, `SELECT count(*) FROM bookings WHERE id = $1 AND reason IS NULL AND info IS NULL`, done["id"]) != 1 {
		t.Fatal("contents of an old completed booking must be cleared")
	}
	e.ok("purge_job", "purgeExpired", obj{"category": "bookings_completed"}, 200)
	if count(t, e, `SELECT count(*) FROM bookings WHERE id = $1`, done["id"]) != 0 {
		t.Fatal("an old completed booking must be deleted (retention C)")
	}

	// D: a deleted account is removed after retention D.
	uid, _ := e.newUser("active", "patient")
	e.ok("user_service", "accountDelete", obj{"user_id": uid}, 200)
	e.ok("purge_job", "purgeExpired", obj{"category": "users"}, 200)
	if count(t, e, `SELECT count(*) FROM users WHERE id = $1`, uid) != 1 {
		t.Fatal("a fresh deletion must stay until retention D has passed")
	}
	exec(t, e, `UPDATE users SET deleted_at = now() - interval '400 days' WHERE id = $1`, uid)
	e.ok("purge_job", "purgeExpired", obj{"category": "users"}, 200)
	if count(t, e, `SELECT count(*) FROM users WHERE id = $1`, uid) != 0 {
		t.Fatal("the account must be gone after retention D")
	}

	// Tokens and e-mail tokens.
	id, _ := e.newUser("active", "patient")
	e.ok("user_service", "tokenRevoke", obj{"jti": uuid.NewString(), "user_id": id, "expires_at": ts(time.Now().Add(-time.Hour))}, 204)
	if e.ok("purge_job", "purgeExpired", obj{"category": "tokens"}, 200)["affected_count"].(float64) < 1 {
		t.Fatal("expired revocations must be purged")
	}
	e.ok("user_service", "emailTokenCreate", obj{"user_id": id, "purpose": "reset", "token_hash": uuid.NewString() + uuid.NewString(), "expires_at": ts(time.Now().Add(-time.Hour))}, 204)
	if e.ok("purge_job", "purgeExpired", obj{"category": "email_tokens"}, 200)["affected_count"].(float64) < 1 {
		t.Fatal("expired e-mail tokens must be purged")
	}

	// E: audit rows. Each run leaves exactly one audit row without contents.
	exec(t, e, `INSERT INTO audit_log (occurred_at, action) VALUES (now() - interval '400 days', 'old')`)
	e.ok("purge_job", "purgeExpired", obj{"category": "audit_log"}, 200)
	if count(t, e, `SELECT count(*) FROM audit_log WHERE action = 'old'`) != 0 {
		t.Fatal("old audit rows must be purged")
	}
	if count(t, e, `SELECT count(*) FROM audit_log WHERE action = 'purge' AND data_category = 'audit_log' AND affected_count >= 1 AND actor_user_id IS NULL`) < 1 {
		t.Fatal("a purge run must write an audit row with the count")
	}
	_ = ctx
}

func TestAuditLog(t *testing.T) {
	e := newEnv(t)
	actor, _ := e.newUser("active", "systemadmin")
	action := "test-" + uuid.NewString()
	e.ok("user_service", "auditWrite", obj{"actor_user_id": actor, "action": action, "target_user_id": actor}, 204)
	e.ok("calendar_service", "auditWrite", obj{"action": action}, 204)
	res := e.ok("user_service", "auditList", obj{"action": action}, 200)
	if res["total"].(float64) != 2 {
		t.Fatalf("audit: %v", res)
	}
	e.expect("calendar_service", "auditList", obj{}, 403, "forbidden") // only user_service reads the log
}

func exec(t *testing.T, e *env, sql string, args ...any) {
	t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, e *env, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
