# MMR API - Project State Snapshot

## Overview
Car/LPR (License Plate Recognition) event receiver API built in Go. Receives JSON + images from LPR cameras, stores in SQLite, displays on live-updating dashboard with manual comparison/verification features.

**GitHub:** https://github.com/ffgroup-usa/mmrapi  
**Live URL:** https://alpha-mmr.exe.xyz:8000/  
**Location:** `/home/exedev/carapi`

## Tech Stack
- Go 1.21+ with `net/http` (no framework)
- SQLite with sqlc for type-safe queries (WAL mode)
- HTML templates with vanilla JS
- excelize library for XLSX export with embedded images
- systemd service (`carapi.service`)

## Key Files
```
/home/exedev/carapi/
├── cmd/srv/main.go          # Entry point, flags
├── srv/server.go            # All HTTP handlers (~1200 lines)
├── srv/templates/
│   ├── dashboard.html       # Main page with live updates
│   ├── archive.html         # Archived events view
│   ├── compare.html         # Manual verification page
│   └── event.html           # Single event detail
├── db/
│   ├── db.go                # DB open, migrations, pragmas
│   ├── migrations/          # 001-006 SQL migrations
│   ├── queries/events.sql   # sqlc query definitions
│   └── dbgen/               # Generated Go code
├── data/
│   ├── json/                # Stored JSON files
│   └── images/              # Stored image files
├── db.sqlite3               # SQLite database
└── srv.service              # systemd unit file
```

## Database Schema (SQLite)

### events
- id, car_id, plate_utf8, car_state, sensor_provider_id
- event_datetime, capture_timestamp, plate_country, plate_region, plate_region_code
- plate_confidence, vehicle_make, vehicle_model, vehicle_color, vehicle_type
- confidence_mmr, confidence_color, geotag_lat, geotag_lon
- camera_serial, camera_ip, raw_json, json_filename
- archive_id (NULL=current, non-NULL=archived), created_at

### images
- id, event_id, image_type ('plate', 'vehicle', 'uploaded'), filename, disk_filename, image_data (BLOB), created_at

### archives
- id, name, event_count, created_at

### compare_results (NEW)
- id, archive_id, event_id, field ('plate'|'maker'|'model'|'color'), is_incorrect, created_at, updated_at
- UNIQUE(archive_id, event_id, field)

## API Endpoints

### Event Ingestion
- `POST /api` - Receives car events (JSON, multipart with images, base64 ImageArray)

### Dashboard
- `GET /` - Live dashboard, auto-refreshes every 2 seconds
- `GET /api/events` - Returns current events as JSON
- `POST /clean` - Archives current events, clears dashboard

### Archives
- `GET /archive/{id}` - View archived events
- `POST /archive/{id}/delete` - Delete archive + files

### Compare (Manual Verification)
- `GET /archive/{id}/compare` - Compare page with checkboxes
- `POST /archive/{id}/compare/toggle` - AJAX save checkbox state
- `GET /archive/{id}/compare/export` - Export XLSX with embedded images

### Files
- `GET /json/{id}` - View event JSON
- `GET /json/{id}/download` - Download JSON with original filename
- `GET /image/{id}` - Serve image
- `GET /image/{id}/download` - Download image with original filename

## Dashboard Columns
TIMESTAMP | CAR_ID | STATE | LPR_UTF8 | COUNTRY | REGION | CAR_MAKER | CAR_MODEL | CAR_M_TYPE | CAR_COLOR | LP_CROP

### State Colors
- new: green, update: blue, lost: red, reliable: yellow

### Tooltips (hover)
- LPR_UTF8 → plateConfidence
- CAR_MODEL → confidenceMMR  
- CAR_COLOR → confidenceColor

## Compare Page Features
- Columns: TIMESTAMP | CAR_ID | LPR_UTF8 | ✗ | LP_CROP | VEHICLE | CAR_MAKER | ✗ | CAR_MODEL | ✗ | CAR_COLOR | ✗
- Checkboxes mark fields as "incorrect" (red background)
- **Persistent checkboxes** - saved to DB via AJAX, survives page reload
- Vehicle image popup: click = immediate, hover 1sec = delayed
- Statistics section: correct/incorrect counts + percentages per field
- **XLSX Export**: embedded images, red backgrounds for incorrect, Statistics sheet

## Image Type Detection
- Filename contains `lpup` → type = 'plate' (license plate crop)
- Filename contains `roi` → type = 'vehicle' (full vehicle)
- Queries select by type for correct LP_CROP vs VEHICLE display

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
  "imageFile": "/path/to/roi_image.jpg",
  "imageFile2": "/path/to/lpup_image.jpg",
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

## Regenerate sqlc
```bash
cd /home/exedev/carapi/db
~/go/bin/sqlc generate
```

## Recent Commits
1. `64f6b16` - Database persistence for compare checkbox state
2. `1e53bb0` - Click to open vehicle image popup (+ hover)
3. `131c301` - Server-side XLSX export with embedded images
4. `edda65e` - Compare page: LPR checkbox, hover preview, statistics
5. `03a30c9` - Add Compare page for manual verification
6. `5791020` - Fix image type detection (plate vs vehicle)

## Known Issues
- Clicking multiple checkboxes very quickly (<1 sec apart) may cause one to fail due to SQLite busy lock (increased timeout to 5 sec helps)

## Notes
- Files stored on disk: `data/json/{id}_{plate}.json`, `data/images/{id}_{plate}_{type}.jpg`
- Live updates use polling `/api/events` every 2 seconds
- SSH key for GitHub: `~/.ssh/id_ed25519`
- SQLite uses WAL mode + 5 second busy_timeout
- Image decoding requires `_ "image/jpeg"` and `_ "image/png"` imports
