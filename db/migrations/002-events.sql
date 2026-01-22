-- Car events table
CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    car_id TEXT NOT NULL,
    plate_utf8 TEXT,
    car_state TEXT,
    sensor_provider_id TEXT,
    event_datetime TEXT,
    capture_timestamp TEXT,
    plate_country TEXT,
    plate_region TEXT,
    plate_confidence REAL,
    geotag_lat REAL,
    geotag_lon REAL,
    vehicle_make TEXT,
    vehicle_model TEXT,
    vehicle_color TEXT,
    camera_serial TEXT,
    camera_ip TEXT,
    raw_json TEXT,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

-- Images table (for both uploaded files and base64 decoded images)
CREATE TABLE IF NOT EXISTS images (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_id INTEGER NOT NULL REFERENCES events(id) ON DELETE CASCADE,
    image_type TEXT,  -- 'vehicle', 'plate', 'uploaded', etc.
    filename TEXT,
    image_data BLOB NOT NULL,
    created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_events_car_id ON events(car_id);
CREATE INDEX IF NOT EXISTS idx_events_plate ON events(plate_utf8);
CREATE INDEX IF NOT EXISTS idx_events_created ON events(created_at);
CREATE INDEX IF NOT EXISTS idx_images_event_id ON images(event_id);

-- Record execution of this migration
INSERT OR IGNORE INTO migrations (migration_number, migration_name)
VALUES (002, '002-events');
