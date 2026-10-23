-- Schema checks. Run after migrations; everything happens in a transaction that is rolled back.
\set ON_ERROR_STOP on
BEGIN;

CREATE FUNCTION pg_temp.expect_error(sql text, label text) RETURNS void LANGUAGE plpgsql AS $$
BEGIN
    BEGIN
        EXECUTE sql;
    EXCEPTION WHEN OTHERS THEN
        RAISE NOTICE 'PASS (rejected): %', label;
        RETURN;
    END;
    RAISE EXCEPTION 'FAIL (accepted): %', label;
END $$;

INSERT INTO users (id, first_name, last_name, email, password_hash, status) VALUES
  ('00000000-0000-0000-0000-000000000001', 'Doc', 'Tor', 'doc@example.org', '$argon2id$v=19$m=65536,t=3,p=2$x$y', 'active'),
  ('00000000-0000-0000-0000-000000000002', 'Pat', 'Ient', 'pat@example.org', '$argon2id$v=19$m=65536,t=3,p=2$x$y', 'active');
INSERT INTO doctors (user_id) VALUES ('00000000-0000-0000-0000-000000000001');
INSERT INTO patients (user_id) VALUES ('00000000-0000-0000-0000-000000000002');
INSERT INTO specialties (id, name) VALUES ('00000000-0000-0000-0000-0000000000a1', 'Allgemeinmedizin');
INSERT INTO services (id, name, specialty_id) VALUES ('00000000-0000-0000-0000-0000000000b1', 'Kontrolle', '00000000-0000-0000-0000-0000000000a1');

-- Overlapping booking for the same doctor is rejected.
INSERT INTO bookings (id, patient_id, doctor_id, service_id, starts_at, ends_at) VALUES
  ('00000000-0000-0000-0000-0000000000c1', '00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-0000000000b1', '2030-01-01 09:00+00', '2030-01-01 09:30+00');
SELECT pg_temp.expect_error($q$
  INSERT INTO bookings (patient_id, doctor_id, service_id, starts_at, ends_at) VALUES
  ('00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-0000000000b1', '2030-01-01 09:15+00', '2030-01-01 09:45+00')$q$,
  'overlapping booking for the same doctor');

-- Adjacent booking is fine.
INSERT INTO bookings (patient_id, doctor_id, service_id, starts_at, ends_at) VALUES
  ('00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-0000000000b1', '2030-01-01 09:30+00', '2030-01-01 10:00+00');

-- Cancelling frees the slot.
UPDATE bookings SET status = 'storniert', deleted_at = now(), reason = NULL, info = NULL
 WHERE id = '00000000-0000-0000-0000-0000000000c1';
INSERT INTO bookings (patient_id, doctor_id, service_id, starts_at, ends_at) VALUES
  ('00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-000000000001',
   '00000000-0000-0000-0000-0000000000b1', '2030-01-01 09:00+00', '2030-01-01 09:30+00');

-- Bookings without a doctor ("Sonstiges") do not block each other.
INSERT INTO bookings (patient_id, service_id, starts_at, ends_at) VALUES
  ('00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-0000000000b1', '2030-02-01 09:00+00', '2030-02-01 10:00+00'),
  ('00000000-0000-0000-0000-000000000002', '00000000-0000-0000-0000-0000000000b1', '2030-02-01 09:00+00', '2030-02-01 10:00+00');

SELECT pg_temp.expect_error($q$INSERT INTO users (first_name, last_name, email, password_hash) VALUES ('A','B','DOC@example.org','$argon2id$x')$q$, 'duplicate email, different case');
SELECT pg_temp.expect_error($q$INSERT INTO users (first_name, last_name, email, password_hash) VALUES ('A','B','x@example.org','plaintext')$q$, 'non-argon2id password hash');
SELECT pg_temp.expect_error($q$UPDATE users SET status = 'bogus'$q$, 'invalid user status');
SELECT pg_temp.expect_error($q$INSERT INTO user_roles VALUES ('00000000-0000-0000-0000-000000000001', 'root')$q$, 'invalid role');
SELECT pg_temp.expect_error($q$UPDATE bookings SET status = 'bogus'$q$, 'invalid booking status');
SELECT pg_temp.expect_error($q$UPDATE bookings SET ends_at = starts_at$q$, 'booking with zero length');
SELECT pg_temp.expect_error($q$INSERT INTO services (name, is_other) VALUES ('Sonstiges 2', true), ('Sonstiges 3', true)$q$, 'second is_other service');
SELECT pg_temp.expect_error($q$INSERT INTO working_days (doctor_id, weekday, start_time, end_time) VALUES ('00000000-0000-0000-0000-000000000001', 1, '12:00', '08:00')$q$, 'working day ending before start');

DO $$ BEGIN
    IF (SELECT count(*) FROM services WHERE is_other) <> 1 THEN RAISE EXCEPTION 'FAIL: service Sonstiges missing'; END IF;
    RAISE NOTICE 'PASS: service Sonstiges seeded';
END $$;

DO $$ BEGIN
    IF (SELECT count(*) FROM config) <> 5 THEN RAISE EXCEPTION 'FAIL: config defaults missing'; END IF;
    RAISE NOTICE 'PASS: config defaults A to E present';
END $$;

DO $$ BEGIN
    IF (SELECT count(*) FROM users WHERE sessions_valid_after IS NULL) <> 0 THEN
        RAISE EXCEPTION 'FAIL: sessions_valid_after must never be NULL';
    END IF;
    RAISE NOTICE 'PASS: sessions_valid_after set for all users';
END $$;

ROLLBACK;
