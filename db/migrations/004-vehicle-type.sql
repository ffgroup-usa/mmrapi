-- Add vehicle type and confidence fields
ALTER TABLE events ADD COLUMN vehicle_type TEXT;
ALTER TABLE events ADD COLUMN confidence_mmr TEXT;
ALTER TABLE events ADD COLUMN confidence_color TEXT;

-- Record execution of this migration
INSERT OR IGNORE INTO migrations (migration_number, migration_name)
VALUES (004, '004-vehicle-type');
