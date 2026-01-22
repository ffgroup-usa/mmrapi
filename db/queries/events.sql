-- name: InsertEvent :one
INSERT INTO events (
    car_id, plate_utf8, car_state, sensor_provider_id,
    event_datetime, capture_timestamp, plate_country, plate_region, plate_confidence,
    geotag_lat, geotag_lon, vehicle_make, vehicle_model, vehicle_color,
    vehicle_type, confidence_mmr, confidence_color,
    camera_serial, camera_ip, raw_json, created_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
) RETURNING id;

-- name: InsertImage :exec
INSERT INTO images (event_id, image_type, filename, image_data, created_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetRecentEvents :many
SELECT 
    e.id, e.car_id, e.plate_utf8, e.car_state, e.sensor_provider_id, 
    e.event_datetime, e.created_at, e.plate_country, e.plate_region,
    e.vehicle_make, e.vehicle_model, e.vehicle_color, e.vehicle_type,
    e.plate_confidence, e.confidence_mmr, e.confidence_color,
    e.json_filename,
    COALESCE((SELECT id FROM images WHERE event_id = e.id ORDER BY id LIMIT 1), 0) as first_image_id,
    COALESCE((SELECT id FROM images WHERE event_id = e.id ORDER BY id LIMIT 1 OFFSET 1), 0) as second_image_id
FROM events e
WHERE e.archive_id IS NULL
ORDER BY e.created_at DESC
LIMIT ?;

-- name: GetArchivedEvents :many
SELECT 
    e.id, e.car_id, e.plate_utf8, e.car_state, e.sensor_provider_id, 
    e.event_datetime, e.created_at, e.plate_country, e.plate_region,
    e.vehicle_make, e.vehicle_model, e.vehicle_color, e.vehicle_type,
    e.plate_confidence, e.confidence_mmr, e.confidence_color,
    e.json_filename,
    COALESCE((SELECT id FROM images WHERE event_id = e.id ORDER BY id LIMIT 1), 0) as first_image_id,
    COALESCE((SELECT id FROM images WHERE event_id = e.id ORDER BY id LIMIT 1 OFFSET 1), 0) as second_image_id
FROM events e
WHERE e.archive_id = ?
ORDER BY e.created_at DESC;

-- name: CountCurrentEvents :one
SELECT COUNT(*) FROM events WHERE archive_id IS NULL;

-- name: GetEventByID :one
SELECT * FROM events WHERE id = ?;

-- name: GetImagesByEventID :many
SELECT id, image_type, filename, created_at FROM images WHERE event_id = ?;

-- name: GetImageData :one
SELECT image_data FROM images WHERE id = ?;

-- name: CountEvents :one
SELECT COUNT(*) FROM events;

-- name: SearchByPlate :many
SELECT id, car_id, plate_utf8, car_state, sensor_provider_id, event_datetime, created_at
FROM events
WHERE plate_utf8 LIKE ?
ORDER BY created_at DESC
LIMIT ?;


-- name: CreateArchive :one
INSERT INTO archives (name, event_count, created_at)
VALUES (?, ?, ?)
RETURNING id;

-- name: ArchiveCurrentEvents :exec
UPDATE events SET archive_id = ? WHERE archive_id IS NULL;

-- name: GetArchives :many
SELECT id, name, event_count, created_at FROM archives ORDER BY created_at DESC;

-- name: GetArchiveByID :one
SELECT id, name, event_count, created_at FROM archives WHERE id = ?;

-- name: UpdateEventJsonFilename :exec
UPDATE events SET json_filename = ? WHERE id = ?;

-- name: UpdateImageDiskFilename :exec
UPDATE images SET disk_filename = ? WHERE id = ?;

-- name: GetImageWithFilename :one
SELECT id, event_id, image_type, filename, disk_filename, created_at FROM images WHERE id = ?;
