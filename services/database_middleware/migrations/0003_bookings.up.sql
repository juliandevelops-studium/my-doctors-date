-- btree_gist is required for the booking exclusion constraint (uuid/equality + range overlap).
-- gen_random_uuid() is built into PostgreSQL 13+, no pgcrypto needed.
CREATE EXTENSION IF NOT EXISTS btree_gist;

CREATE TABLE bookings (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    patient_id uuid REFERENCES patients (user_id) ON DELETE SET NULL,
    doctor_id  uuid REFERENCES doctors (user_id) ON DELETE RESTRICT,
    service_id uuid NOT NULL REFERENCES services (id) ON DELETE RESTRICT,
    starts_at  timestamptz NOT NULL,
    ends_at    timestamptz NOT NULL,
    reason     text CHECK (reason IS NULL OR char_length(reason) <= 200),
    info       text CHECK (info IS NULL OR char_length(info) <= 2000),
    status     text NOT NULL DEFAULT 'bestaetigt'
                    CHECK (status IN ('angefragt', 'bestaetigt', 'storniert', 'abgeschlossen')),
    deleted_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    CHECK (ends_at > starts_at),
    CONSTRAINT bookings_no_doctor_overlap EXCLUDE USING gist (
        doctor_id WITH =,
        tstzrange(starts_at, ends_at) WITH &&
    ) WHERE (doctor_id IS NOT NULL AND deleted_at IS NULL AND status IN ('angefragt', 'bestaetigt'))
);

CREATE INDEX bookings_patient_idx          ON bookings (patient_id);
CREATE INDEX bookings_doctor_idx           ON bookings (doctor_id);
CREATE INDEX bookings_service_idx          ON bookings (service_id);
-- Calendar queries and the soft-delete filter.
CREATE INDEX bookings_starts_at_active_idx ON bookings (starts_at) WHERE deleted_at IS NULL;
CREATE INDEX bookings_deleted_at_idx       ON bookings (deleted_at) WHERE deleted_at IS NOT NULL;
