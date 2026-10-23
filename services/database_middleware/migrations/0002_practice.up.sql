CREATE TABLE specialties (
    id   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name text NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 100)
);

CREATE TABLE services (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name             text    NOT NULL UNIQUE CHECK (char_length(name) BETWEEN 1 AND 100),
    duration_minutes integer NOT NULL DEFAULT 60 CHECK (duration_minutes BETWEEN 5 AND 480),
    specialty_id     uuid    REFERENCES specialties (id) ON DELETE RESTRICT,
    is_other         boolean NOT NULL DEFAULT false,
    CHECK (is_other OR specialty_id IS NOT NULL)
);
CREATE UNIQUE INDEX services_single_other_key ON services (is_other) WHERE is_other;
CREATE INDEX services_specialty_idx ON services (specialty_id);

INSERT INTO services (name, duration_minutes, specialty_id, is_other) VALUES ('Sonstiges', 60, NULL, true);

CREATE TABLE doctors (
    user_id uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE
);

CREATE TABLE doctor_specialties (
    doctor_id    uuid NOT NULL REFERENCES doctors (user_id) ON DELETE CASCADE,
    specialty_id uuid NOT NULL REFERENCES specialties (id) ON DELETE RESTRICT,
    PRIMARY KEY (doctor_id, specialty_id)
);
CREATE INDEX doctor_specialties_spec_idx ON doctor_specialties (specialty_id);

CREATE TABLE working_days (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    doctor_id  uuid     NOT NULL REFERENCES doctors (user_id) ON DELETE CASCADE,
    weekday    smallint NOT NULL CHECK (weekday BETWEEN 1 AND 7), -- ISO: 1 = Monday
    start_time time     NOT NULL,
    end_time   time     NOT NULL,
    CHECK (end_time > start_time)
);
CREATE INDEX working_days_doctor_idx ON working_days (doctor_id, weekday);

CREATE TABLE patients (
    user_id        uuid PRIMARY KEY REFERENCES users (id) ON DELETE CASCADE,
    street         text CHECK (street IS NULL OR char_length(street) <= 200),
    postal_code    text CHECK (postal_code IS NULL OR char_length(postal_code) <= 20),
    city           text CHECK (city IS NULL OR char_length(city) <= 100),
    -- Hauptarzt; NULL means the union of all matching doctors (UF-05 case B).
    main_doctor_id uuid REFERENCES doctors (user_id) ON DELETE SET NULL
);
CREATE INDEX patients_main_doctor_idx ON patients (main_doctor_id);
