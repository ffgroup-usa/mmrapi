package srv

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"path/filepath"
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
}

// Event JSON structures (flexible to handle different field naming conventions)
type IncomingEvent struct {
	// Car identifiers (support multiple naming conventions)
	CarID  string `json:"carID"`
	CarId  string `json:"carid"`
	CarId2 string `json:"carId"`

	// Plate info
	PlateUTF8   string `json:"plateUTF8"`
	PlateText   string `json:"plateText"`
	PlateRegion string `json:"plateRegion"`
	PlateCountry string `json:"plateCountry"`
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
		Make  string `json:"make"`
		Model string `json:"model"`
		Color string `json:"color"`
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
	srv := &Server{
		Hostname:     hostname,
		TemplatesDir: filepath.Join(baseDir, "templates"),
		StaticDir:    filepath.Join(baseDir, "static"),
	}
	if err := srv.setUpDatabase(dbPath); err != nil {
		return nil, err
	}
	return srv, nil
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

// HandleAPI processes incoming car events
func (s *Server) HandleAPI(w http.ResponseWriter, r *http.Request) {
	var event IncomingEvent
	var rawJSON []byte
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
	var vMake, vModel, vColor *string
	if event.VehicleInfo != nil {
		vMake = ptrIfNotEmpty(event.VehicleInfo.Make)
		vModel = ptrIfNotEmpty(event.VehicleInfo.Model)
		vColor = ptrIfNotEmpty(event.VehicleInfo.Color)
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
		PlateConfidence:  plateConfidence,
		GeotagLat:        geoLat,
		GeotagLon:        geoLon,
		VehicleMake:      vMake,
		VehicleModel:     vModel,
		VehicleColor:     vColor,
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

	// Save uploaded images
	for _, img := range uploadedImages {
		err := q.InsertImage(r.Context(), dbgen.InsertImageParams{
			EventID:   eventID,
			ImageType: ptr("uploaded"),
			Filename:  &img.Filename,
			ImageData: img.Data,
			CreatedAt: now,
		})
		if err != nil {
			slog.Warn("failed to save uploaded image", "error", err)
		} else {
			imageCount++
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

		err = q.InsertImage(r.Context(), dbgen.InsertImageParams{
			EventID:   eventID,
			ImageType: &imgType,
			Filename:  &filename,
			ImageData: decoded,
			CreatedAt: now,
		})
		if err != nil {
			slog.Warn("failed to save embedded image", "error", err)
		} else {
			imageCount++
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
	count, _ := q.CountEvents(r.Context())
	events, _ := q.GetRecentEvents(r.Context(), 50)

	data := struct {
		Hostname   string
		EventCount int64
		Events     []dbgen.GetRecentEventsRow
	}{
		Hostname:   s.Hostname,
		EventCount: count,
		Events:     events,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.renderTemplate(w, "dashboard.html", data); err != nil {
		slog.Warn("render template", "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
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

// Serve starts the HTTP server
func (s *Server) Serve(addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", s.HandleRoot)
	mux.HandleFunc("POST /api", s.HandleAPI)
	mux.HandleFunc("GET /event/{id}", s.HandleEvent)
	mux.HandleFunc("GET /image/{id}", s.HandleImage)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.StaticDir))))
	slog.Info("starting server", "addr", addr)
	return http.ListenAndServe(addr, mux)
}
