-- Add plate region code
ALTER TABLE events ADD COLUMN plate_region_code TEXT;

-- Backfill from raw_json
UPDATE events SET plate_region_code = json_extract(raw_json, '$.plateRegionCode')
WHERE raw_json IS NOT NULL AND plate_region_code IS NULL;

-- Record execution of this migration
INSERT OR IGNORE INTO migrations (migration_number, migration_name)
VALUES (005, '005-region-code');
