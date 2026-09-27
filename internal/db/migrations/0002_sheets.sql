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
-- instants UTC with a Z. Date and time columns must round-trip through SQLite's own date()
-- and strftime(), so impossible values such as 2026-02-30 or 29:59 are refused. An
-- occurrence whose end is earlier than its start runs past midnight into the next day.
--
-- Instants must be exactly YYYY-MM-DDTHH:MM:SS.SSSZ, because expiry is compared as text.
--
-- Deleting a sheet deletes everything under it, including the people who signed up to it:
-- in M1 a person belongs to exactly one sheet, so nothing personal outlives the sheet, and
-- triggers keep every claim inside one sheet, from the claim's side and from its parents'.
--
-- Left to the application (checked in Go, not here): the time zone is a valid IANA name; a
-- by_date section has a date and a slots_only section does not; an occurrence's date matches
-- its section's; a claim's quantity is at most its slot's max_per_claim.

CREATE TABLE sheets (
    id               INTEGER PRIMARY KEY,
    slug             TEXT    NOT NULL UNIQUE CHECK (length(slug) >= 10),
    admin_token_hash BLOB    NOT NULL UNIQUE CHECK (length(admin_token_hash) = 32),
    title            TEXT    NOT NULL CHECK (length(trim(title)) > 0),
    description      TEXT    NOT NULL DEFAULT '',
    organizer_name   TEXT    NOT NULL DEFAULT '',
    location         TEXT    NOT NULL DEFAULT '',
    time_zone        TEXT    NOT NULL CHECK (length(time_zone) > 0),
    format           TEXT    NOT NULL CHECK (format IN ('by_date', 'slots_only')),
    status           TEXT    NOT NULL DEFAULT 'draft' CHECK (status IN ('draft', 'open', 'closed')),
    allow_waitlist   INTEGER NOT NULL DEFAULT 1 CHECK (allow_waitlist IN (0, 1)),
    collect_email    TEXT    NOT NULL DEFAULT 'optional' CHECK (collect_email IN ('off', 'optional', 'required')),
    created_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at       TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    published_at     TEXT    CHECK (published_at IS NULL OR strftime('%Y-%m-%dT%H:%M:%fZ', published_at) IS published_at)
);

CREATE TABLE sections (
    id       INTEGER PRIMARY KEY,
    sheet_id INTEGER NOT NULL REFERENCES sheets (id) ON DELETE CASCADE,
    position INTEGER NOT NULL,
    title    TEXT    NOT NULL DEFAULT '',
    date     TEXT    CHECK (date IS NULL OR date(date) IS date),
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
    date       TEXT    CHECK (date IS NULL OR date(date) IS date),
    start_time TEXT    CHECK (start_time IS NULL OR strftime('%H:%M', start_time) IS start_time),
    end_time   TEXT    CHECK (end_time IS NULL OR strftime('%H:%M', end_time) IS end_time),
    CHECK (end_time IS NULL OR start_time IS NOT NULL),
    CHECK (end_time IS NULL OR end_time <> start_time)
);

-- A person as they signed up to one sheet. Contacts are not unique: the same number on two
-- sheets is two rows. Identity (M2) adds its own, separately scoped table rather than
-- merging these.
CREATE TABLE people (
    id         INTEGER PRIMARY KEY,
    sheet_id   INTEGER NOT NULL REFERENCES sheets (id) ON DELETE CASCADE,
    first_name TEXT NOT NULL CHECK (length(trim(first_name)) > 0),
    last_name  TEXT NOT NULL DEFAULT '',
    -- E.164, e.g. +12165550142.
    phone      TEXT CHECK (phone IS NULL OR (phone GLOB '+[1-9]*' AND phone NOT GLOB '+*[^0-9]*'
                                         AND length(phone) BETWEEN 8 AND 16)),
    email      TEXT CHECK (email IS NULL OR email LIKE '%_@_%'),
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    CHECK (phone IS NOT NULL OR email IS NOT NULL)
);

CREATE TABLE claims (
    id                INTEGER PRIMARY KEY,
    occurrence_id     INTEGER NOT NULL REFERENCES occurrences (id) ON DELETE CASCADE,
    person_id         INTEGER NOT NULL REFERENCES people (id) ON DELETE CASCADE,
    quantity          INTEGER NOT NULL DEFAULT 1 CHECK (quantity > 0),
    comment           TEXT    NOT NULL DEFAULT '',
    status            TEXT    NOT NULL CHECK (status IN ('confirmed', 'waitlisted', 'cancelled')),
    created_by        TEXT    NOT NULL DEFAULT 'participant' CHECK (created_by IN ('participant', 'organizer')),
    manage_token_hash BLOB    NOT NULL UNIQUE CHECK (length(manage_token_hash) = 32),
    manage_expires_at TEXT    NOT NULL CHECK (strftime('%Y-%m-%dT%H:%M:%fZ', manage_expires_at) IS manage_expires_at),
    created_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    updated_at        TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
    cancelled_at      TEXT    CHECK (cancelled_at IS NULL OR strftime('%Y-%m-%dT%H:%M:%fZ', cancelled_at) IS cancelled_at),
    CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL))
);

CREATE INDEX sections_by_sheet ON sections (sheet_id, position);
CREATE INDEX slots_by_section ON slots (section_id, position);
CREATE INDEX occurrences_by_slot ON occurrences (slot_id);
-- Capacity is summed over live claims per occurrence on every claim.
CREATE INDEX claims_by_occurrence ON claims (occurrence_id, status);
CREATE INDEX claims_by_person ON claims (person_id);
CREATE INDEX people_by_sheet ON people (sheet_id);

-- A claim's person and occurrence must belong to the same sheet: the sheet is the isolation
-- boundary, so a claim may never join one sheet's person to another sheet's slot. (If either
-- side is missing the comparison is NULL and the foreign keys report it instead.)
CREATE TRIGGER claims_same_sheet_insert BEFORE INSERT ON claims
WHEN (SELECT sheet_id FROM people WHERE id = NEW.person_id) <>
     (SELECT se.sheet_id FROM occurrences o
        JOIN slots sl ON sl.id = o.slot_id
        JOIN sections se ON se.id = sl.section_id
       WHERE o.id = NEW.occurrence_id)
BEGIN
    SELECT RAISE(ABORT, 'claim crosses sheets');
END;

CREATE TRIGGER claims_same_sheet_update BEFORE UPDATE OF occurrence_id, person_id ON claims
WHEN (SELECT sheet_id FROM people WHERE id = NEW.person_id) <>
     (SELECT se.sheet_id FROM occurrences o
        JOIN slots sl ON sl.id = o.slot_id
        JOIN sections se ON se.id = sl.section_id
       WHERE o.id = NEW.occurrence_id)
BEGIN
    SELECT RAISE(ABORT, 'claim crosses sheets');
END;

-- The same rule from the other side: a row never moves to another parent, so nothing a claim
-- points at can be carried into a different sheet. (A claim itself may move to another
-- occurrence on its own sheet; the triggers above check that.) Nothing in M1 re-parents rows.
CREATE TRIGGER people_sheet_id_fixed BEFORE UPDATE OF sheet_id ON people
WHEN NEW.sheet_id IS NOT OLD.sheet_id
BEGIN
    SELECT RAISE(ABORT, 'people.sheet_id cannot change');
END;

CREATE TRIGGER sections_sheet_id_fixed BEFORE UPDATE OF sheet_id ON sections
WHEN NEW.sheet_id IS NOT OLD.sheet_id
BEGIN
    SELECT RAISE(ABORT, 'sections.sheet_id cannot change');
END;

CREATE TRIGGER slots_section_id_fixed BEFORE UPDATE OF section_id ON slots
WHEN NEW.section_id IS NOT OLD.section_id
BEGIN
    SELECT RAISE(ABORT, 'slots.section_id cannot change');
END;

CREATE TRIGGER occurrences_slot_id_fixed BEFORE UPDATE OF slot_id ON occurrences
WHEN NEW.slot_id IS NOT OLD.slot_id
BEGIN
    SELECT RAISE(ABORT, 'occurrences.slot_id cannot change');
END;

UPDATE meta SET value = '2' WHERE key = 'schema';
