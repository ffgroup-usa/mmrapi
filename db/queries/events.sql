-- name: InsertEvent :one
INSERT INTO events (
    car_id, plate_utf8, car_state, sensor_provider_id,
    event_datetime, capture_timestamp, plate_country, plate_region, plate_confidence,
    geotag_lat, geotag_lon, vehicle_make, vehicle_model, vehicle_color,
    camera_serial, camera_ip, raw_json, created_at
) VALUES (
    ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?
) RETURNING id;

-- name: InsertImage :exec
INSERT INTO images (event_id, image_type, filename, image_data, created_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetRecentEvents :many
SELECT id, car_id, plate_utf8, car_state, sensor_provider_id, event_datetime, created_at
FROM events
ORDER BY created_at DESC
LIMIT ?;

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
