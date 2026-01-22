package srv

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"srv.exe.dev/db"
	"srv.exe.dev/db/dbgen"
)

type Server struct {
	DB           *sql.DB
	Hostname     string
	TemplatesDir string
	StaticDir    string
	DataDir      string // For storing JSON and images on disk
}

// Event JSON structures (flexible to handle different field naming conventions)
type IncomingEvent struct {
	// Car identifiers (support multiple naming conventions)
	CarID  string `json:"carID"`
	CarId  string `json:"carid"`
	CarId2 string `json:"carId"`

	// Plate info
	PlateUTF8       string `json:"plateUTF8"`
	PlateText       string `json:"plateText"`
	PlateRegion     string `json:"plateRegion"`
	PlateRegionCode string `json:"plateRegionCode"`
	PlateCountry    string `json:"plateCountry"`
	PlateConfidence string `json:"plateConfidence"`

	// State
	CarState  string `json:"carState"`
	CarState2 string `json:"carstate"`

	// Timestamps
	DateTime         string `json:"datetime"`
	CaptureTimestamp string `json:"capture_timestamp"`

	// Sensor/Camera
	SensorProviderID string `json:"sensorProviderID"`
	PacketCounter    string `json:"packetCounter"`

	// Geotag
	Geotag *struct {
		Lat float64 `json:"lat"`
		Lon float64 `json:"lon"`
	} `json:"geotag"`

	// Vehicle info
	VehicleInfo *struct {
		Make            string `json:"make"`
		Model           string `json:"model"`
		Color           string `json:"color"`
		Type            string `json:"type"`
		ConfidenceMMR   string `json:"confidenceMMR"`
		ConfidenceColor string `json:"confidenceColor"`
	} `json:"vehicle_info"`

	// Camera info
	CameraInfo *struct {
		SerialNumber string `json:"SerialNumber"`
		IPAddress    string `json:"IPAddress"`
	} `json:"camera_info"`

	// Images embedded in JSON
	ImageArray []struct {
		ImageType   string `json:"ImageType"`
		ImageFormat string `json:"ImageFormat"`
		BinaryImage string `json:"BinaryImage"`
	} `json:"ImageArray"`
}

// Helper to get first non-empty value
func coalesce(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func ptr[T any](v T) *T {
	return &v
}

func ptrIfNotEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func New(dbPath, hostname string) (*Server, error) {
	_, thisFile, _, _ := runtime.Caller(0)
	baseDir := filepath.Dir(thisFile)
	dataDir := filepath.Join(filepath.Dir(baseDir), "data")
	
	// Create data directories
	os.MkdirAll(filepath.Join(dataDir, "json"), 0755)
	os.MkdirAll(filepath.Join(dataDir, "images"), 0755)
	
	srv := &Server{
		Hostname:     hostname,
		TemplatesDir: filepath.Join(baseDir, "templates"),
		StaticDir:    filepath.Join(baseDir, "static"),
		DataDir:      dataDir,
	}
	if err := srv.setUpDatabase(dbPath); err != nil {
		return nil, err
	}
	return srv, nil
}

// insertImageWithID inserts an image and returns its ID
func (s *Server) insertImageWithID(ctx context.Context, q *dbgen.Queries, params dbgen.InsertImageParams) (int64, error) {
	err := q.InsertImage(ctx, params)
	if err != nil {
		return 0, err
	}
	// Get the last inserted ID
	var id int64
	err = s.DB.QueryRowContext(ctx, "SELECT last_insert_rowid()").Scan(&id)
	return id, err
}

func (s *Server) setUpDatabase(dbPath string) error {
	wdb, err := db.Open(dbPath)
	if err != nil {
		return fmt.Errorf("failed to open db: %w", err)
	}
	s.DB = wdb
	if err := db.RunMigrations(wdb); err != nil {
		return fmt.Errorf("failed to run migrations: %w", err)
	}
	return nil
}

// sanitizeFilename removes unsafe characters from filenames
func sanitizeFilename(name string) string {
	re := regexp.MustCompile(`[^a-zA-Z0-9._-]`)
	return re.ReplaceAllString(name, "_")
}

// HandleAPI processes incoming car events
func (s *Server) HandleAPI(w http.ResponseWriter, r *http.Request) {
	var event IncomingEvent
	var rawJSON []byte
	var jsonFilename string // Original filename from multipart
	var uploadedImages []struct {
		Filename string
		Data     []byte
	}

	contentType := r.Header.Get("Content-Type")

	if strings.HasPrefix(contentType, "multipart/") {
		// Parse multipart (max 32MB)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			s.jsonError(w, "failed to parse multipart: "+err.Error(), http.StatusBadRequest)
			return
		}

		// Process all files
		if r.MultipartForm != nil && r.MultipartForm.File != nil {
			for _, files := range r.MultipartForm.File {
				for _, f := range files {
					file, err := f.Open()
					if err != nil {
						continue
					}
					data, _ := io.ReadAll(file)
					file.Close()

					lowerName := strings.ToLower(f.Filename)
					if strings.HasSuffix(lowerName, ".json") {
						rawJSON = data
						jsonFilename = f.Filename
					} else if strings.HasSuffix(lowerName, ".jpg") ||
						strings.HasSuffix(lowerName, ".jpeg") ||
						strings.HasSuffix(lowerName, ".png") {
						uploadedImages = append(uploadedImages, struct {
							Filename string
							Data     []byte
						}{Filename: f.Filename, Data: data})
					}
				}
			}
		}

		// Try form fields if no JSON file
		if rawJSON == nil {
			if jsonStr := r.FormValue("json"); jsonStr != "" {
				rawJSON = []byte(jsonStr)
			} else if jsonStr := r.FormValue("data"); jsonStr != "" {
				rawJSON = []byte(jsonStr)
			}
		}
	} else {
		// Plain JSON body
		var err error
		rawJSON, err = io.ReadAll(r.Body)
		if err != nil {
			s.jsonError(w, "failed to read body: "+err.Error(), http.StatusBadRequest)
			return
		}
	}

	if len(rawJSON) == 0 {
		s.jsonError(w, "no JSON data provided", http.StatusBadRequest)
		return
	}

	if err := json.Unmarshal(rawJSON, &event); err != nil {
		s.jsonError(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Normalize fields
	carID := coalesce(event.CarID, event.CarId, event.CarId2)
	if carID == "" {
		carID = fmt.Sprintf("auto-%d", time.Now().UnixNano())
	}
	carState := coalesce(event.CarState, event.CarState2)
	plate := coalesce(event.PlateUTF8, event.PlateText)

	// Parse confidence
	var plateConfidence *float64
	if event.PlateConfidence != "" {
		if conf, err := strconv.ParseFloat(event.PlateConfidence, 64); err == nil {
			plateConfidence = &conf
		}
	}

	// Geotag
	var geoLat, geoLon *float64
	if event.Geotag != nil {
		geoLat = &event.Geotag.Lat
		geoLon = &event.Geotag.Lon
	}

	// Vehicle info
	var vMake, vModel, vColor, vType, confMMR, confColor *string
	if event.VehicleInfo != nil {
		vMake = ptrIfNotEmpty(event.VehicleInfo.Make)
		vModel = ptrIfNotEmpty(event.VehicleInfo.Model)
		vColor = ptrIfNotEmpty(event.VehicleInfo.Color)
		vType = ptrIfNotEmpty(event.VehicleInfo.Type)
		confMMR = ptrIfNotEmpty(event.VehicleInfo.ConfidenceMMR)
		confColor = ptrIfNotEmpty(event.VehicleInfo.ConfidenceColor)
	}

	// Camera info
	var camSerial, camIP *string
	if event.CameraInfo != nil {
		camSerial = ptrIfNotEmpty(event.CameraInfo.SerialNumber)
		camIP = ptrIfNotEmpty(event.CameraInfo.IPAddress)
	}

	now := time.Now()
	rawJSONStr := string(rawJSON)

	// Insert event
	q := dbgen.New(s.DB)
	eventID, err := q.InsertEvent(r.Context(), dbgen.InsertEventParams{
		CarID:            carID,
		PlateUtf8:        ptrIfNotEmpty(plate),
		CarState:         ptrIfNotEmpty(carState),
		SensorProviderID: ptrIfNotEmpty(event.SensorProviderID),
		EventDatetime:    ptrIfNotEmpty(event.DateTime),
		CaptureTimestamp: ptrIfNotEmpty(event.CaptureTimestamp),
		PlateCountry:     ptrIfNotEmpty(event.PlateCountry),
		PlateRegion:      ptrIfNotEmpty(event.PlateRegion),
		PlateRegionCode:  ptrIfNotEmpty(event.PlateRegionCode),
		PlateConfidence:  plateConfidence,
		GeotagLat:        geoLat,
		GeotagLon:        geoLon,
		VehicleMake:      vMake,
		VehicleModel:     vModel,
		VehicleColor:     vColor,
		VehicleType:      vType,
		ConfidenceMmr:    confMMR,
		ConfidenceColor:  confColor,
		CameraSerial:     camSerial,
		CameraIp:         camIP,
		RawJson:          &rawJSONStr,
		CreatedAt:        now,
	})
	if err != nil {
		slog.Error("failed to insert event", "error", err)
		s.jsonError(w, "database error", http.StatusInternalServerError)
		return
	}

	imageCount := 0

	// Save JSON to disk
	if jsonFilename == "" {
		// Generate filename: id_plate.json
		safePlate := sanitizeFilename(plate)
		if safePlate == "" {
			safePlate = "unknown"
		}
		jsonFilename = fmt.Sprintf("%d_%s.json", eventID, safePlate)
	} else {
		// Prefix with event ID to ensure uniqueness
		jsonFilename = fmt.Sprintf("%d_%s", eventID, sanitizeFilename(jsonFilename))
	}
	jsonPath := filepath.Join(s.DataDir, "json", jsonFilename)
	if err := os.WriteFile(jsonPath, rawJSON, 0644); err != nil {
		slog.Warn("failed to save JSON to disk", "error", err)
	} else {
		q.UpdateEventJsonFilename(r.Context(), dbgen.UpdateEventJsonFilenameParams{
			JsonFilename: &jsonFilename,
			ID:           eventID,
		})
	}

	// Save uploaded images
	for i, img := range uploadedImages {
		imgID, err := s.insertImageWithID(r.Context(), q, dbgen.InsertImageParams{
			EventID:   eventID,
			ImageType: ptr("uploaded"),
			Filename:  &img.Filename,
			ImageData: img.Data,
			CreatedAt: now,
		})
		if err != nil {
			slog.Warn("failed to save uploaded image", "error", err)
			continue
		}
		imageCount++
		
		// Save to disk
		diskFilename := fmt.Sprintf("%d_%s", imgID, sanitizeFilename(img.Filename))
		if diskFilename == fmt.Sprintf("%d_", imgID) {
			safePlate := sanitizeFilename(plate)
			if safePlate == "" {
				safePlate = "unknown"
			}
			diskFilename = fmt.Sprintf("%d_%s_%d.jpg", imgID, safePlate, i)
		}
		imgPath := filepath.Join(s.DataDir, "images", diskFilename)
		if err := os.WriteFile(imgPath, img.Data, 0644); err != nil {
			slog.Warn("failed to save image to disk", "error", err)
		} else {
			q.UpdateImageDiskFilename(r.Context(), dbgen.UpdateImageDiskFilenameParams{
				DiskFilename: &diskFilename,
				ID:           imgID,
			})
		}
	}

	// Extract and save base64 images from JSON
	for i, img := range event.ImageArray {
		if img.BinaryImage == "" {
			continue
		}
		decoded, err := base64.StdEncoding.DecodeString(img.BinaryImage)
		if err != nil {
			slog.Warn("failed to decode base64 image", "index", i, "error", err)
			continue
		}
		imgType := img.ImageType
		if imgType == "" {
			imgType = "embedded"
		}
		ext := img.ImageFormat
		if ext == "" {
			ext = "jpg"
		}
		filename := fmt.Sprintf("%s_%d.%s", imgType, i, ext)

		imgID, err := s.insertImageWithID(r.Context(), q, dbgen.InsertImageParams{
			EventID:   eventID,
			ImageType: &imgType,
			Filename:  &filename,
			ImageData: decoded,
			CreatedAt: now,
		})
		if err != nil {
			slog.Warn("failed to save embedded image", "error", err)
			continue
		}
		imageCount++
		
		// Save to disk
		safePlate := sanitizeFilename(plate)
		if safePlate == "" {
			safePlate = "unknown"
		}
		diskFilename := fmt.Sprintf("%d_%s_%s.%s", imgID, safePlate, imgType, ext)
		imgPath := filepath.Join(s.DataDir, "images", diskFilename)
		if err := os.WriteFile(imgPath, decoded, 0644); err != nil {
			slog.Warn("failed to save image to disk", "error", err)
		} else {
			q.UpdateImageDiskFilename(r.Context(), dbgen.UpdateImageDiskFilenameParams{
				DiskFilename: &diskFilename,
				ID:           imgID,
			})
		}
	}

	slog.Info("event recorded", "id", eventID, "plate", plate, "images", imageCount)

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"success": true,
		"message": "event recorded",
		"id":      eventID,
		"plate":   plate,
		"images":  imageCount,
	})
}

func (s *Server) jsonError(w http.ResponseWriter, msg string, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(map[string]any{
		"success": false,
		"message": msg,
	})
}

// HandleRoot shows a dashboard
func (s *Server) HandleRoot(w http.ResponseWriter, r *http.Request) {
	q := dbgen.New(s.DB)
	count, _ := q.CountCurrentEvents(r.Context())
	events, _ := q.GetRecentEvents(r.Context(), 500)
	archives, _ := q.GetArchives(r.Context())

	data := struct {
		Hostname   string
		EventCount int64
		Events     []dbgen.GetRecentEventsRow
		Archives   []dbgen.Archive
		ArchiveID  int64
	}{
		Hostname:   s.Hostname,
		EventCount: count,
		Events:     events,
		Archives:   archives,
		ArchiveID:  0,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.renderTemplate(w, "dashboard.html", data); err != nil {
		slog.Warn("render template", "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// HandleArchive shows archived events
func (s *Server) HandleArchive(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid archive id", http.StatusBadRequest)
		return
	}

	q := dbgen.New(s.DB)
	archive, err := q.GetArchiveByID(r.Context(), id)
	if err != nil {
		http.Error(w, "archive not found", http.StatusNotFound)
		return
	}

	events, _ := q.GetArchivedEvents(r.Context(), &id)
	archives, _ := q.GetArchives(r.Context())

	data := struct {
		Hostname   string
		EventCount int64
		Events     []dbgen.GetArchivedEventsRow
		Archives   []dbgen.Archive
		ArchiveID  int64
		Archive    dbgen.Archive
	}{
		Hostname:   s.Hostname,
		EventCount: archive.EventCount,
		Events:     events,
		Archives:   archives,
		ArchiveID:  id,
		Archive:    archive,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.renderTemplate(w, "archive.html", data); err != nil {
		slog.Warn("render template", "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// HandleClean archives current events
func (s *Server) HandleClean(w http.ResponseWriter, r *http.Request) {
	q := dbgen.New(s.DB)
	
	// Count current events
	count, err := q.CountCurrentEvents(r.Context())
	if err != nil || count == 0 {
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	// Create archive
	now := time.Now()
	name := now.Format("2006-01-02 15:04:05")
	archiveID, err := q.CreateArchive(r.Context(), dbgen.CreateArchiveParams{
		Name:       &name,
		EventCount: count,
		CreatedAt:  now,
	})
	if err != nil {
		slog.Error("failed to create archive", "error", err)
		http.Error(w, "failed to create archive", http.StatusInternalServerError)
		return
	}

	// Move events to archive
	err = q.ArchiveCurrentEvents(r.Context(), &archiveID)
	if err != nil {
		slog.Error("failed to archive events", "error", err)
		http.Error(w, "failed to archive events", http.StatusInternalServerError)
		return
	}

	slog.Info("archived events", "archive_id", archiveID, "count", count)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleJsonFile serves JSON file for download
func (s *Server) HandleJsonFile(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}

	q := dbgen.New(s.DB)
	event, err := q.GetEventByID(r.Context(), id)
	if err != nil {
		http.Error(w, "event not found", http.StatusNotFound)
		return
	}

	if event.JsonFilename == nil || *event.JsonFilename == "" {
		// Return raw JSON from database
		if event.RawJson != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%d.json"`, id))
			w.Write([]byte(*event.RawJson))
			return
		}
		http.Error(w, "no JSON available", http.StatusNotFound)
		return
	}

	jsonPath := filepath.Join(s.DataDir, "json", *event.JsonFilename)
	data, err := os.ReadFile(jsonPath)
	if err != nil {
		// Fallback to database
		if event.RawJson != nil {
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, *event.JsonFilename))
			w.Write([]byte(*event.RawJson))
			return
		}
		http.Error(w, "JSON file not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, *event.JsonFilename))
	w.Write(data)
}

// HandleRawJson returns raw JSON for display
func (s *Server) HandleRawJson(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}

	q := dbgen.New(s.DB)
	event, err := q.GetEventByID(r.Context(), id)
	if err != nil {
		http.Error(w, "event not found", http.StatusNotFound)
		return
	}

	if event.RawJson == nil {
		http.Error(w, "no JSON available", http.StatusNotFound)
		return
	}

	// Pretty print JSON
	var prettyJSON map[string]any
	if err := json.Unmarshal([]byte(*event.RawJson), &prettyJSON); err == nil {
		// Remove large base64 images for display
		if imgArr, ok := prettyJSON["ImageArray"].([]any); ok {
			for _, img := range imgArr {
				if imgMap, ok := img.(map[string]any); ok {
					if _, has := imgMap["BinaryImage"]; has {
						imgMap["BinaryImage"] = "[base64 data omitted]"
					}
				}
			}
		}
		formatted, _ := json.MarshalIndent(prettyJSON, "", "  ")
		w.Header().Set("Content-Type", "application/json")
		w.Write(formatted)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(*event.RawJson))
}

// HandleEvent shows a single event
func (s *Server) HandleEvent(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid event id", http.StatusBadRequest)
		return
	}

	q := dbgen.New(s.DB)
	event, err := q.GetEventByID(r.Context(), id)
	if err != nil {
		http.Error(w, "event not found", http.StatusNotFound)
		return
	}

	images, _ := q.GetImagesByEventID(r.Context(), id)

	data := struct {
		Event  dbgen.Event
		Images []dbgen.GetImagesByEventIDRow
	}{
		Event:  event,
		Images: images,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.renderTemplate(w, "event.html", data); err != nil {
		slog.Warn("render template", "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// HandleImage serves image data
func (s *Server) HandleImage(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid image id", http.StatusBadRequest)
		return
	}

	q := dbgen.New(s.DB)
	data, err := q.GetImageData(r.Context(), id)
	if err != nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}

	// Detect content type from magic bytes
	contentType := http.DetectContentType(data)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Cache-Control", "public, max-age=86400")
	w.Write(data)
}

func (s *Server) renderTemplate(w http.ResponseWriter, name string, data any) error {
	path := filepath.Join(s.TemplatesDir, name)
	tmpl, err := template.ParseFiles(path)
	if err != nil {
		return fmt.Errorf("parse template %q: %w", name, err)
	}
	if err := tmpl.Execute(w, data); err != nil {
		return fmt.Errorf("execute template %q: %w", name, err)
	}
	return nil
}

// HandleDeleteArchive deletes an archive and its files
func (s *Server) HandleDeleteArchive(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid archive id", http.StatusBadRequest)
		return
	}

	q := dbgen.New(s.DB)
	
	// Get files to delete
	files, err := q.GetArchivedEventFiles(r.Context(), &id)
	if err != nil {
		slog.Warn("failed to get archive files", "error", err)
	}

	// Delete files from disk
	for _, f := range files {
		if f.JsonFilename != nil && *f.JsonFilename != "" {
			jsonPath := filepath.Join(s.DataDir, "json", *f.JsonFilename)
			os.Remove(jsonPath)
		}
		if f.DiskFilename != nil && *f.DiskFilename != "" {
			imgPath := filepath.Join(s.DataDir, "images", *f.DiskFilename)
			os.Remove(imgPath)
		}
	}

	// Delete from database
	if err := q.DeleteArchiveImages(r.Context(), &id); err != nil {
		slog.Warn("failed to delete archive images", "error", err)
	}
	if err := q.DeleteArchiveEvents(r.Context(), &id); err != nil {
		slog.Warn("failed to delete archive events", "error", err)
	}
	if err := q.DeleteArchive(r.Context(), id); err != nil {
		slog.Warn("failed to delete archive", "error", err)
	}

	slog.Info("deleted archive", "id", id)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// HandleEventsAPI returns recent events as JSON for live updates
func (s *Server) HandleEventsAPI(w http.ResponseWriter, r *http.Request) {
	q := dbgen.New(s.DB)
	events, err := q.GetRecentEvents(r.Context(), 500)
	if err != nil {
		s.jsonError(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(events)
}

// Serve starts the HTTP server
func (s *Server) Serve(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.HandleRoot)
	mux.HandleFunc("POST /api", s.HandleAPI)
	mux.HandleFunc("GET /api/events", s.HandleEventsAPI)
	mux.HandleFunc("GET /event/{id}", s.HandleEvent)
	mux.HandleFunc("GET /image/{id}", s.HandleImage)
	mux.HandleFunc("GET /archive/{id}", s.HandleArchive)
	mux.HandleFunc("POST /archive/{id}/delete", s.HandleDeleteArchive)
	mux.HandleFunc("POST /clean", s.HandleClean)
	mux.HandleFunc("GET /json/{id}", s.HandleRawJson)
	mux.HandleFunc("GET /json/{id}/download", s.HandleJsonFile)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.StaticDir))))
	slog.Info("starting server", "addr", addr)
	return http.ListenAndServe(addr, mux)
}
