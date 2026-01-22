# MMR API - Project State Snapshot

## Overview
Car/LPR (License Plate Recognition) event receiver API built in Go. Receives JSON + images from LPR cameras, stores in SQLite, displays on live-updating dashboard.

**GitHub:** https://github.com/ffgroup-usa/mmrapi  
**Live URL:** https://alpha-mmr.exe.xyz:8000/  
**Location:** `/home/exedev/carapi`

## Tech Stack
- Go 1.21+ with `net/http` (no framework)
- SQLite with sqlc for type-safe queries
- HTML templates with vanilla JS for live updates
- systemd service (`carapi.service`)

## Key Files
```
/home/exedev/carapi/
├── cmd/srv/main.go          # Entry point, flags
├── srv/server.go            # All HTTP handlers (~780 lines)
├── srv/templates/
│   ├── dashboard.html       # Main page with live updates
│   ├── archive.html         # Archived events view
│   └── event.html           # Single event detail
├── db/
│   ├── migrations/          # 001-005 SQL migrations
│   ├── queries/events.sql   # sqlc query definitions
│   └── dbgen/               # Generated Go code
├── data/
│   ├── json/                # Stored JSON files
│   └── images/              # Stored image files
├── db.sqlite3               # SQLite database
└── srv.service              # systemd unit file
```

## API Endpoints

### POST /api
Receives car events. Supports:
- Plain JSON (`Content-Type: application/json`)
- Multipart with JSON file + images
- Multipart with `json` form field + images
- Base64 images in `ImageArray` field

### GET /
Live dashboard - auto-refreshes every 2 seconds

### GET /api/events
Returns current events as JSON (for live updates)

### GET /archive/{id}
View archived events

### POST /clean
Archives current events, clears dashboard

### POST /archive/{id}/delete
Deletes archive + associated files from disk

### GET /json/{id}, GET /json/{id}/download
View/download event JSON

### GET /image/{id}
Serve image data

## Database Schema (SQLite)

### events
- id, car_id, plate_utf8, car_state, sensor_provider_id
- event_datetime, capture_timestamp, plate_country, plate_region, plate_region_code
- plate_confidence, vehicle_make, vehicle_model, vehicle_color, vehicle_type
- confidence_mmr, confidence_color, geotag_lat, geotag_lon
- camera_serial, camera_ip, raw_json, json_filename
- archive_id (NULL=current, non-NULL=archived), created_at

### images
- id, event_id, image_type, filename, disk_filename, image_data (BLOB), created_at

### archives
- id, name, event_count, created_at

## Dashboard Columns
TIMESTAMP | CAR_ID | STATE | LPR_UTF8 | COUNTRY | REGION | CAR_MAKER | CAR_MODEL | CAR_M_TYPE | CAR_COLOR | LP_CROP

### Tooltips (hover)
- LPR_UTF8 → plateConfidence
- CAR_MODEL → confidenceMMR
- CAR_COLOR → confidenceColor

### State Colors
- new: green
- update: blue
- lost: red
- reliable: yellow

## JSON Input Fields (from LPR cameras)
```json
{
  "carID": "907",
  "plateUTF8": "ABC123",
  "carState": "lost",
  "datetime": "20260121 163817135",
  "plateCountry": "USA",
  "plateRegionCode": "WA",
  "plateConfidence": "0.839598",
  "sensorProviderID": "defaultID",
  "vehicle_info": {
    "make": "Chevrolet",
    "model": "Avalanche/Silverado",
    "type": "PICKUP",
    "color": "BROWN",
    "confidenceMMR": "0.733200",
    "confidenceColor": "0.999349"
  },
  "ImageArray": [
    {"ImageType": "vehicle", "ImageFormat": "jpg", "BinaryImage": "base64..."}
  ]
}
```

## Service Management
```bash
sudo systemctl status carapi
sudo systemctl restart carapi
journalctl -u carapi -f
```

## Build & Deploy
```bash
cd /home/exedev/carapi
go build -o carapi ./cmd/srv
sudo systemctl restart carapi
```

## Recent Commits
1. `c75af4d` - Dynamic updates, STATE/REGION columns, archive delete
2. `da19dcd` - Vehicle type, confidence tooltips, real-size LP_CROP
3. `ac8d56d` - Clean button, archives, JSON/image modals, file storage
4. `f49d256` - Spreadsheet-style dashboard with LP_CROP hover
5. `1a7322b` - Initial: JSON/Image receiver for LPR cameras

## Notes
- Files stored on disk: `data/json/{id}_{plate}.json`, `data/images/{id}_{plate}_{type}.jpg`
- Live updates use polling `/api/events` every 2 seconds
- SSH key for GitHub: `~/.ssh/id_ed25519`
- To make public: `ssh exe.dev share set-public alpha-mmr`
