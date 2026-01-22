-- Compare results for manual verification
CREATE TABLE IF NOT EXISTS compare_results (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    archive_id INTEGER NOT NULL,
    event_id INTEGER NOT NULL,
    field TEXT NOT NULL,  -- 'plate', 'maker', 'model', 'color'
    is_incorrect BOOLEAN NOT NULL DEFAULT 0,
    created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    updated_at DATETIME DEFAULT CURRENT_TIMESTAMP,
    UNIQUE(archive_id, event_id, field),
    FOREIGN KEY (archive_id) REFERENCES archives(id) ON DELETE CASCADE,
    FOREIGN KEY (event_id) REFERENCES events(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_compare_results_archive ON compare_results(archive_id);
