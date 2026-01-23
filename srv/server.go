package srv

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	_ "image/jpeg"
	_ "image/png"
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

	"github.com/xuri/excelize/v2"
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

	// Image file paths (for download filenames)
	ImageFile  string `json:"imageFile"`  // Usually vehicle/roi image
	ImageFile2 string `json:"imageFile2"` // Usually plate/lpup image

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

// toInt64 converts various numeric types to int64
func toInt64(v interface{}) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case int32:
		return int64(n)
	case float64:
		return int64(n)
	default:
		return 0
	}
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
		// Detect image type from filename
		imgType := "uploaded"
		lowerName := strings.ToLower(img.Filename)
		if strings.Contains(lowerName, "lpup") || strings.Contains(lowerName, "plate") {
			imgType = "plate"
		} else if strings.Contains(lowerName, "roi") || strings.Contains(lowerName, "vehicle") {
			imgType = "vehicle"
		}
		
		imgID, err := s.insertImageWithID(r.Context(), q, dbgen.InsertImageParams{
			EventID:   eventID,
			ImageType: ptr(imgType),
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
	events, _ := q.GetRecentEvents(r.Context(), 1000)
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

// HandleCompare shows compare page for an archive
func (s *Server) HandleCompare(w http.ResponseWriter, r *http.Request) {
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

	// Load saved compare results
	results, _ := q.GetCompareResults(r.Context(), id)
	incorrectMap := make(map[string]bool) // key: "eventID_field"
	for _, r := range results {
		if r.IsIncorrect {
			key := fmt.Sprintf("%d_%s", r.EventID, r.Field)
			incorrectMap[key] = true
		}
	}

	data := struct {
		Archive     dbgen.Archive
		Events      []dbgen.GetArchivedEventsRow
		Incorrect   map[string]bool
	}{
		Archive:   archive,
		Events:    events,
		Incorrect: incorrectMap,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.renderTemplate(w, "compare.html", data); err != nil {
		slog.Warn("render template", "error", err)
		http.Error(w, "template error", http.StatusInternalServerError)
	}
}

// HandleCompareToggle saves a compare result toggle via AJAX
func (s *Server) HandleCompareToggle(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	archiveID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid archive id", http.StatusBadRequest)
		return
	}

	var req struct {
		EventID   int64  `json:"event_id"`
		Field     string `json:"field"`
		Incorrect bool   `json:"incorrect"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}

	// Validate field
	validFields := map[string]bool{"plate": true, "maker": true, "model": true, "color": true}
	if !validFields[req.Field] {
		http.Error(w, "invalid field", http.StatusBadRequest)
		return
	}

	q := dbgen.New(s.DB)
	err = q.SetCompareResult(r.Context(), dbgen.SetCompareResultParams{
		ArchiveID:   archiveID,
		EventID:     req.EventID,
		Field:       req.Field,
		IsIncorrect: req.Incorrect,
	})
	if err != nil {
		slog.Warn("failed to save compare result", "error", err)
		http.Error(w, "database error", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"ok":true}`))
}

// HandleCompareExport exports compare data to XLSX with embedded images
func (s *Server) HandleCompareExport(w http.ResponseWriter, r *http.Request) {
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

	// Load saved compare results from database
	results, _ := q.GetCompareResults(r.Context(), id)
	incorrectPlates := make(map[int64]bool)
	incorrectMakers := make(map[int64]bool)
	incorrectModels := make(map[int64]bool)
	incorrectColors := make(map[int64]bool)

	for _, r := range results {
		if !r.IsIncorrect {
			continue
		}
		switch r.Field {
		case "plate":
			incorrectPlates[r.EventID] = true
		case "maker":
			incorrectMakers[r.EventID] = true
		case "model":
			incorrectModels[r.EventID] = true
		case "color":
			incorrectColors[r.EventID] = true
		}
	}

	// Create Excel file
	f := excelize.NewFile()
	defer f.Close()

	sheetName := "Compare Results"
	f.SetSheetName("Sheet1", sheetName)

	// Define styles
	redStyle, _ := f.NewStyle(&excelize.Style{
		Fill: excelize.Fill{Type: "pattern", Color: []string{"F8D7DA"}, Pattern: 1},
		Font: &excelize.Font{Color: "721C24"},
		Border: []excelize.Border{
			{Type: "left", Color: "E0E0E0", Style: 1},
			{Type: "top", Color: "E0E0E0", Style: 1},
			{Type: "bottom", Color: "E0E0E0", Style: 1},
			{Type: "right", Color: "E0E0E0", Style: 1},
		},
	})

	headerStyle, _ := f.NewStyle(&excelize.Style{
		Fill: excelize.Fill{Type: "pattern", Color: []string{"F8F9FA"}, Pattern: 1},
		Font: &excelize.Font{Bold: true},
		Border: []excelize.Border{
			{Type: "left", Color: "E0E0E0", Style: 1},
			{Type: "top", Color: "E0E0E0", Style: 1},
			{Type: "bottom", Color: "E0E0E0", Style: 1},
			{Type: "right", Color: "E0E0E0", Style: 1},
		},
	})

	// Headers
	headers := []string{"TIMESTAMP", "CAR_ID", "LPR_UTF8", "LP_CROP", "VEHICLE", "CAR_MAKER", "CAR_MODEL", "CAR_COLOR"}
	for i, h := range headers {
		cell, _ := excelize.CoordinatesToCellName(i+1, 1)
		f.SetCellValue(sheetName, cell, h)
		f.SetCellStyle(sheetName, cell, cell, headerStyle)
	}

	// Set column widths
	f.SetColWidth(sheetName, "A", "A", 20) // TIMESTAMP
	f.SetColWidth(sheetName, "B", "B", 10) // CAR_ID
	f.SetColWidth(sheetName, "C", "C", 12) // LPR_UTF8
	f.SetColWidth(sheetName, "D", "D", 15) // LP_CROP
	f.SetColWidth(sheetName, "E", "E", 20) // VEHICLE
	f.SetColWidth(sheetName, "F", "F", 15) // CAR_MAKER
	f.SetColWidth(sheetName, "G", "G", 25) // CAR_MODEL
	f.SetColWidth(sheetName, "H", "H", 12) // CAR_COLOR

	// Statistics counters
	var plateCorrect, plateIncorrect int
	var makerCorrect, makerIncorrect int
	var modelCorrect, modelIncorrect int
	var colorCorrect, colorIncorrect int

	// Data rows
	for i, e := range events {
		row := i + 2

		// Set row height for images
		f.SetRowHeight(sheetName, row, 50)

		// TIMESTAMP
		timestamp := ""
		if e.EventDatetime != nil {
			timestamp = *e.EventDatetime
		} else {
			timestamp = e.CreatedAt.Format("20060102 150405")
		}
		f.SetCellValue(sheetName, fmt.Sprintf("A%d", row), timestamp)

		// CAR_ID
		f.SetCellValue(sheetName, fmt.Sprintf("B%d", row), e.CarID)

		// LPR_UTF8
		plateCell := fmt.Sprintf("C%d", row)
		if e.PlateUtf8 != nil {
			f.SetCellValue(sheetName, plateCell, *e.PlateUtf8)
		}
		if incorrectPlates[e.ID] {
			f.SetCellStyle(sheetName, plateCell, plateCell, redStyle)
			plateIncorrect++
		} else {
			plateCorrect++
		}

		// LP_CROP image - handle various integer types from SQLite
		plateImgID := toInt64(e.PlateImageID)
		if plateImgID > 0 {
			imgData, err := q.GetImageData(r.Context(), plateImgID)
			if err == nil && len(imgData) > 0 {
				f.AddPictureFromBytes(sheetName, fmt.Sprintf("D%d", row), &excelize.Picture{
					Extension: ".jpg",
					File:      imgData,
					Format:    &excelize.GraphicOptions{ScaleX: 0.3, ScaleY: 0.3, Positioning: "oneCell"},
				})
			}
		}

		// VEHICLE image
		vehicleImgID := toInt64(e.VehicleImageID)
		if vehicleImgID > 0 {
			imgData, err := q.GetImageData(r.Context(), vehicleImgID)
			if err == nil && len(imgData) > 0 {
				f.AddPictureFromBytes(sheetName, fmt.Sprintf("E%d", row), &excelize.Picture{
					Extension: ".jpg",
					File:      imgData,
					Format:    &excelize.GraphicOptions{ScaleX: 0.15, ScaleY: 0.15, Positioning: "oneCell"},
				})
			}
		}

		// CAR_MAKER
		makerCell := fmt.Sprintf("F%d", row)
		if e.VehicleMake != nil {
			f.SetCellValue(sheetName, makerCell, *e.VehicleMake)
		}
		if incorrectMakers[e.ID] {
			f.SetCellStyle(sheetName, makerCell, makerCell, redStyle)
			makerIncorrect++
		} else {
			makerCorrect++
		}

		// CAR_MODEL
		modelCell := fmt.Sprintf("G%d", row)
		if e.VehicleModel != nil {
			f.SetCellValue(sheetName, modelCell, *e.VehicleModel)
		}
		if incorrectModels[e.ID] {
			f.SetCellStyle(sheetName, modelCell, modelCell, redStyle)
			modelIncorrect++
		} else {
			modelCorrect++
		}

		// CAR_COLOR
		colorCell := fmt.Sprintf("H%d", row)
		if e.VehicleColor != nil {
			f.SetCellValue(sheetName, colorCell, *e.VehicleColor)
		}
		if incorrectColors[e.ID] {
			f.SetCellStyle(sheetName, colorCell, colorCell, redStyle)
			colorIncorrect++
		} else {
			colorCorrect++
		}
	}

	// Add Statistics sheet
	statsSheet := "Statistics"
	f.NewSheet(statsSheet)

	f.SetCellValue(statsSheet, "A1", "Field")
	f.SetCellValue(statsSheet, "B1", "Total")
	f.SetCellValue(statsSheet, "C1", "Correct")
	f.SetCellValue(statsSheet, "D1", "Incorrect")
	f.SetCellValue(statsSheet, "E1", "Accuracy %")
	f.SetCellStyle(statsSheet, "A1", "E1", headerStyle)

	total := len(events)
	writeStatRow := func(row int, field string, correct, incorrect int) {
		pct := 0.0
		if total > 0 {
			pct = float64(correct) / float64(total) * 100
		}
		f.SetCellValue(statsSheet, fmt.Sprintf("A%d", row), field)
		f.SetCellValue(statsSheet, fmt.Sprintf("B%d", row), total)
		f.SetCellValue(statsSheet, fmt.Sprintf("C%d", row), correct)
		f.SetCellValue(statsSheet, fmt.Sprintf("D%d", row), incorrect)
		f.SetCellValue(statsSheet, fmt.Sprintf("E%d", row), fmt.Sprintf("%.1f%%", pct))
	}

	writeStatRow(2, "LPR (Plate)", plateCorrect, plateIncorrect)
	writeStatRow(3, "CAR_MAKER", makerCorrect, makerIncorrect)
	writeStatRow(4, "CAR_MODEL", modelCorrect, modelIncorrect)
	writeStatRow(5, "CAR_COLOR", colorCorrect, colorIncorrect)

	f.SetColWidth(statsSheet, "A", "A", 15)
	f.SetColWidth(statsSheet, "B", "E", 12)

	// Write to buffer
	var buf bytes.Buffer
	if err := f.Write(&buf); err != nil {
		slog.Warn("failed to write xlsx", "error", err)
		http.Error(w, "failed to generate xlsx", http.StatusInternalServerError)
		return
	}

	// Send response
	archiveName := "export"
	if archive.Name != nil {
		archiveName = sanitizeFilename(*archive.Name)
	}
	w.Header().Set("Content-Type", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="compare_%s.xlsx"`, archiveName))
	w.Write(buf.Bytes())
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

func (s *Server) HandleImageDownload(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid image id", http.StatusBadRequest)
		return
	}

	q := dbgen.New(s.DB)
	imgInfo, err := q.GetImageWithFilename(r.Context(), id)
	if err != nil {
		http.Error(w, "image not found", http.StatusNotFound)
		return
	}

	data, err := q.GetImageData(r.Context(), id)
	if err != nil {
		http.Error(w, "image data not found", http.StatusNotFound)
		return
	}

	// Use original filename if available
	filename := fmt.Sprintf("image_%d.jpg", id)
	if imgInfo.Filename != nil && *imgInfo.Filename != "" {
		filename = filepath.Base(*imgInfo.Filename)
	}

	contentType := http.DetectContentType(data)
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filename))
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

// HandleRenameArchive renames an archive
func (s *Server) HandleRenameArchive(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "invalid archive id", http.StatusBadRequest)
		return
	}

	name := strings.TrimSpace(r.FormValue("name"))
	if name == "" {
		http.Error(w, "name is required", http.StatusBadRequest)
		return
	}

	q := dbgen.New(s.DB)
	if err := q.RenameArchive(r.Context(), dbgen.RenameArchiveParams{
		Name: &name,
		ID:   id,
	}); err != nil {
		slog.Error("failed to rename archive", "error", err)
		http.Error(w, "failed to rename archive", http.StatusInternalServerError)
		return
	}

	slog.Info("renamed archive", "id", id, "name", name)
	
	// Redirect back to where they came from
	referer := r.Header.Get("Referer")
	if referer == "" {
		referer = "/"
	}
	http.Redirect(w, r, referer, http.StatusSeeOther)
}

// HandleEventsAPI returns recent events as JSON for live updates
func (s *Server) HandleEventsAPI(w http.ResponseWriter, r *http.Request) {
	q := dbgen.New(s.DB)
	events, err := q.GetRecentEvents(r.Context(), 1000)
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
	mux.HandleFunc("GET /image/{id}/download", s.HandleImageDownload)
	mux.HandleFunc("GET /archive/{id}", s.HandleArchive)
	mux.HandleFunc("GET /archive/{id}/compare", s.HandleCompare)
	mux.HandleFunc("GET /archive/{id}/compare/export", s.HandleCompareExport)
	mux.HandleFunc("POST /archive/{id}/compare/toggle", s.HandleCompareToggle)
	mux.HandleFunc("POST /archive/{id}/delete", s.HandleDeleteArchive)
	mux.HandleFunc("POST /archive/{id}/rename", s.HandleRenameArchive)
	mux.HandleFunc("POST /clean", s.HandleClean)
	mux.HandleFunc("GET /json/{id}", s.HandleRawJson)
	mux.HandleFunc("GET /json/{id}/download", s.HandleJsonFile)
	mux.Handle("/static/", http.StripPrefix("/static/", http.FileServer(http.Dir(s.StaticDir))))
	slog.Info("starting server", "addr", addr)
	return http.ListenAndServe(addr, mux)
}
