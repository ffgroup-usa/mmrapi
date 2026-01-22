-- Archives table for pagination/cleanup
CREATE TABLE IF NOT EXISTS archives (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    name TEXT,
    event_count INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Add archive_id to events (NULL = current/active, non-NULL = archived)
ALTER TABLE events ADD COLUMN archive_id INTEGER REFERENCES archives(id);

-- Add file paths for stored JSON and images
ALTER TABLE events ADD COLUMN json_filename TEXT;
ALTER TABLE images ADD COLUMN disk_filename TEXT;

CREATE INDEX IF NOT EXISTS idx_events_archive ON events(archive_id);

-- Record execution of this migration
INSERT OR IGNORE INTO migrations (migration_number, migration_name)
VALUES (003, '003-archives');
