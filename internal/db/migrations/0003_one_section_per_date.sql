-- 0003: a by_date sheet has at most one section per date. The application checks first; this
-- makes two near-simultaneous submissions of the same date unable to both succeed.
CREATE UNIQUE INDEX sections_one_per_date ON sections (sheet_id, date) WHERE date IS NOT NULL;
UPDATE meta SET value = '3' WHERE key = 'schema';
