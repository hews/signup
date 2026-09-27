-- 0002: sheets and the claims made on them (M1).
--
-- A sheet holds sections (a date in by_date, a heading in slots_only); a section holds
-- slots; a slot happens at one or more occurrences, and people claim occurrences. In M1
-- every slot has exactly one occurrence: on its section's date for by_date, undated for
-- slots_only. Keeping occurrences separate now lets by_slot (roles across dates) arrive
-- without reshaping claims.
--
-- There are no accounts yet. The organiser manages a sheet through an admin link and a
-- participant manages a claim through a manage link; both are random tokens, and only
-- their SHA-256 is stored here.
--
-- Times are ISO 8601 text: dates YYYY-MM-DD, times HH:MM in the sheet's time zone,
-- instants UTC with a Z.

CREATE TABLE sheets (
    id               INTEGER PRIMARY KEY,
    slug             TEXT    NOT NULL UNIQUE CHECK (length(slug) >= 10),
    admin_token_hash BLOB    NOT NULL UNIQUE CHECK (length(admin_token_hash) = 32),
    title            TEXT    NOT NULL CHECK (length(trim(title)) > 0),
    description      TEXT    NOT NULL DEFAULT '',
    organizer_name   TEXT    NOT NULL DEFAULT '',
    location         TEXT    NOT NULL DEFAULT '',
    time_zone        TEXT    NOT NULL,
    format           TEXT    NOT NULL CHECK (format IN ('by_date', 'slots_only')),
    status           TEXT    NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'open', 'closed')),
    allow_waitlist   INTEGER NOT NULL DEFAULT 1 CHECK (allow_waitlist IN (0, 1)),
    collect_email    TEXT    NOT NULL DEFAULT 'optional' CHECK (collect_email IN ('off', 'optional', 'required')),
    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    published_at     TEXT
);

CREATE TABLE sections (
    id       INTEGER PRIMARY KEY,
    sheet_id INTEGER NOT NULL REFERENCES sheets (id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    title    TEXT    NOT NULL DEFAULT '',
    date     TEXT    CHECK (date IS NULL OR date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    UNIQUE (sheet_id, position)
);

CREATE TABLE slots (
    id              INTEGER PRIMARY KEY,
    section_id      INTEGER NOT NULL REFERENCES sections (id) ON DELETE CASCADE,
    position        INTEGER NOT NULL,
    title           TEXT    NOT NULL CHECK (length(trim(title)) > 0),
    description     TEXT    NOT NULL DEFAULT '',
    -- NULL means unlimited.
    quantity_wanted INTEGER CHECK (quantity_wanted IS NULL OR quantity_wanted > 0),
    unit            TEXT    NOT NULL DEFAULT 'people' CHECK (unit IN ('people', 'items')),
    max_per_claim   INTEGER NOT NULL DEFAULT 1 CHECK (max_per_claim > 0),
    UNIQUE (section_id, position)
);

CREATE TABLE occurrences (
    id         INTEGER PRIMARY KEY,
    slot_id    INTEGER NOT NULL REFERENCES slots (id) ON DELETE CASCADE,
    date       TEXT    CHECK (date IS NULL OR date GLOB '[0-9][0-9][0-9][0-9]-[0-1][0-9]-[0-3][0-9]'),
    start_time TEXT    CHECK (start_time IS NULL OR start_time GLOB '[0-2][0-9]:[0-5][0-9]'),
    end_time   TEXT    CHECK (end_time IS NULL OR end_time GLOB '[0-2][0-9]:[0-5][0-9]'),
    CHECK (end_time IS NULL OR start_time IS NOT NULL),
    CHECK (end_time IS NULL OR end_time > start_time)
);

-- A person as they signed up. Contacts are not unique: the same number on two sheets is
-- two rows until identity (M2) merges them.
CREATE TABLE people (
    id         INTEGER PRIMARY KEY,
    first_name TEXT NOT NULL CHECK (length(trim(first_name)) > 0),
    last_name  TEXT NOT NULL DEFAULT '',
    -- E.164, e.g. +12165550142.
    phone      TEXT CHECK (phone IS NULL OR phone GLOB '+[1-9][0-9]*'),
    email      TEXT CHECK (email IS NULL OR email LIKE '%_@_%'),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (phone IS NOT NULL OR email IS NOT NULL)
);

CREATE TABLE claims (
    id                INTEGER PRIMARY KEY,
    occurrence_id     INTEGER NOT NULL REFERENCES occurrences (id) ON DELETE CASCADE,
    person_id         INTEGER NOT NULL REFERENCES people (id),
    quantity          INTEGER NOT NULL DEFAULT 1 CHECK (quantity > 0),
    comment           TEXT    NOT NULL DEFAULT '',
    status            TEXT    NOT NULL CHECK (status IN ('confirmed', 'waitlisted', 'cancelled')),
    created_by        TEXT    NOT NULL DEFAULT 'participant' CHECK (created_by IN ('participant', 'organizer')),
    manage_token_hash BLOB    NOT NULL UNIQUE CHECK (length(manage_token_hash) = 32),
    manage_expires_at TEXT    NOT NULL,
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    cancelled_at      TEXT,
    CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL))
);

CREATE INDEX sections_by_sheet ON sections (sheet_id, position);
CREATE INDEX slots_by_section ON slots (section_id, position);
CREATE INDEX occurrences_by_slot ON occurrences (slot_id);
-- Capacity is summed over live claims per occurrence on every claim.
CREATE INDEX claims_by_occurrence ON claims (occurrence_id, status);
CREATE INDEX claims_by_person ON claims (person_id);

UPDATE meta SET value = '2' WHERE key = 'schema';
