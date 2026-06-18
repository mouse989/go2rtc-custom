package workers

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// remoteEvent mirrors counting.CountEvent for deserialising data from a worker.
type remoteEvent struct {
	Timestamp    int64  `json:"ts"`
	CameraID     string `json:"cameraId"`
	Name         string `json:"name"`
	Count        int    `json:"count"`
	Total        int    `json:"total"`
	Direction    string `json:"dir,omitempty"`
	VehicleClass string `json:"vehicleClass,omitempty"`
	WorkerID     string `json:"workerId,omitempty"`
}

// runHealthLoop polls every worker every 30 seconds.
func runHealthLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		checkAllWorkers()
	}
}

func checkAllWorkers() {
	for _, wk := range listWorkers() {
		if !wk.Enabled {
			continue
		}
		go checkWorker(wk)
	}
}

func checkWorker(wk *Worker) {
	now := time.Now().UTC().Format(time.RFC3339)

	// GET /api/counting/status
	statusResp, err := workerRequest(wk, http.MethodGet, "/api/counting/status", nil, "")
	if err != nil {
		s := buildOfflineStatus(wk, err.Error(), now)
		setStatus(s)
		log.Debug().Str("worker", wk.ID).Err(err).Msg("[workers] health check failed")
		return
	}
	defer statusResp.Body.Close()

	body, err := io.ReadAll(statusResp.Body)
	if err != nil {
		s := buildOfflineStatus(wk, "read status body: "+err.Error(), now)
		setStatus(s)
		return
	}

	var statusData struct {
		Running bool `json:"running"`
		Cameras []struct {
			ID      string `json:"id"`
			Running bool   `json:"running"`
		} `json:"cameras"`
	}
	if err := json.Unmarshal(body, &statusData); err != nil {
		s := buildOfflineStatus(wk, "parse status: "+err.Error(), now)
		setStatus(s)
		return
	}
	// Derive actual running state: global flag OR any individual camera running.
	anyRunning := statusData.Running
	for _, c := range statusData.Cameras {
		if c.Running {
			anyRunning = true
			break
		}
	}

	// GET /api/counting/config → yoloModel
	yoloModel := ""
	cfgResp, err := workerRequest(wk, http.MethodGet, "/api/counting/config", nil, "")
	if err == nil {
		defer cfgResp.Body.Close()
		cfgBody, _ := io.ReadAll(cfgResp.Body)
		var cfgData struct {
			YoloModel string `json:"yoloModel"`
		}
		_ = json.Unmarshal(cfgBody, &cfgData)
		yoloModel = cfgData.YoloModel
	}

	// Retrieve previous state for carry-over fields.
	prev := getStatus(wk.ID)
	lastSync := ""
	trainedModel := ""
	wasTraining := false
	if prev != nil {
		lastSync = prev.LastSync
		trainedModel = prev.TrainedModel
		wasTraining = prev.Training
	}

	// Poll training status.
	training := false
	var trainFinishedModel string
	trainResp, err2 := workerRequest(wk, http.MethodGet, "/api/counting/train-status", nil, "")
	if err2 == nil {
		defer trainResp.Body.Close()
		trainBody, _ := io.ReadAll(trainResp.Body)
		var ts struct {
			Running    bool   `json:"running"`
			ReturnCode *int   `json:"returncode"`
			Model      string `json:"model"`
		}
		if json.Unmarshal(trainBody, &ts) == nil {
			training = ts.Running
			// Detect: was training before, now finished successfully.
			if wasTraining && !ts.Running && ts.ReturnCode != nil && *ts.ReturnCode == 0 && ts.Model != "" {
				trainFinishedModel = ts.Model
			}
		}
	}

	s := &WorkerStatus{
		ID:           wk.ID,
		Name:         wk.Name,
		URL:          wk.URL,
		Enabled:      wk.Enabled,
		Online:       true,
		LastCheck:    now,
		LastSync:     lastSync,
		Cameras:      len(statusData.Cameras),
		Running:      anyRunning,
		YoloModel:    yoloModel,
		Training:     training,
		TrainedModel: trainedModel,
	}
	setStatus(s)

	// Auto-pull trained model when training just finished.
	if trainFinishedModel != "" {
		go autoPullModel(wk, trainFinishedModel)
	}
}

func autoPullModel(wk *Worker, modelPath string) {
	log.Info().Str("worker", wk.ID).Str("model", modelPath).Msg("[workers] training finished, auto-pulling model")
	pulled, err := pullModelFile(wk, modelPath)
	if err != nil {
		log.Error().Err(err).Str("worker", wk.ID).Msg("[workers] auto-pull model failed")
		return
	}
	log.Info().Str("saved", pulled).Str("worker", wk.ID).Msg("[workers] model pulled from worker")
	if s := getStatus(wk.ID); s != nil {
		s.TrainedModel = pulled
		setStatus(s)
	}
}

// pullModelFile downloads modelPath from the worker and saves to local models/ dir.
// Returns the relative path of the saved file.
func pullModelFile(wk *Worker, modelPath string) (string, error) {
	resp, err := workerRequest(wk, http.MethodGet, "/api/counting/models/download?file="+modelPath, nil, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("worker returned HTTP %d", resp.StatusCode)
	}
	modelsDir := filepath.Join(filepath.Dir(storeFile), "models")
	if err := os.MkdirAll(modelsDir, 0755); err != nil {
		return "", err
	}
	fname := filepath.Base(modelPath)
	dest := filepath.Join(modelsDir, fname)
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, data, 0644); err != nil {
		return "", err
	}
	return filepath.Join("models", fname), nil
}

func buildOfflineStatus(wk *Worker, errMsg, now string) *WorkerStatus {
	lastSync := ""
	if prev := getStatus(wk.ID); prev != nil {
		lastSync = prev.LastSync
	}
	return &WorkerStatus{
		ID:        wk.ID,
		Name:      wk.Name,
		URL:       wk.URL,
		Enabled:   wk.Enabled,
		Online:    false,
		Error:     errMsg,
		LastCheck: now,
		LastSync:  lastSync,
	}
}

// runSyncLoop waits 60 s on startup, then syncs every 5 minutes.
func runSyncLoop() {
	time.Sleep(60 * time.Second)
	syncAll()
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	for range ticker.C {
		syncAll()
	}
}

func syncAll() {
	for _, wk := range listWorkers() {
		if !wk.Enabled {
			continue
		}
		wk := wk // capture for goroutine
		go func() {
			if err := syncWorkerEvents(wk.ID); err != nil {
				log.Error().Err(err).Str("worker", wk.ID).Msg("[workers] sync failed")
			}
		}()
	}
}

// syncWorkerEvents fetches today's and yesterday's events from a worker and
// imports them into Server 1's counting store via the registered callback.
func syncWorkerEvents(id string) error {
	wk := getWorkerByID(id)
	if wk == nil {
		return fmt.Errorf("worker not found: %s", id)
	}

	loc, err := time.LoadLocation("Asia/Ho_Chi_Minh")
	if err != nil {
		loc = time.UTC
	}

	now := time.Now().In(loc)
	yesterday := now.AddDate(0, 0, -1)
	dates := []string{
		yesterday.Format("2006-01-02"),
		now.Format("2006-01-02"),
	}

	for _, date := range dates {
		if err := fetchAndImportDate(wk, date); err != nil {
			log.Warn().Err(err).Str("worker", id).Str("date", date).Msg("[workers] sync date failed")
		}
	}

	// Update last sync time in status cache.
	syncTime := time.Now().UTC().Format(time.RFC3339)
	s := getStatus(id)
	if s == nil {
		s = &WorkerStatus{
			ID:      wk.ID,
			Name:    wk.Name,
			URL:     wk.URL,
			Enabled: wk.Enabled,
		}
	}
	s.LastSync = syncTime
	setStatus(s)

	return nil
}

func fetchAndImportDate(wk *Worker, date string) error {
	resp, err := workerRequest(wk, http.MethodGet, "/api/counting/data?date="+date, nil, "")
	if err != nil {
		return fmt.Errorf("request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		// No data for this date — not an error.
		return nil
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("worker returned HTTP %d", resp.StatusCode)
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read body: %w", err)
	}

	// Validate it's a JSON array before passing to importer.
	var check []json.RawMessage
	if err := json.Unmarshal(raw, &check); err != nil {
		return fmt.Errorf("parse events: %w", err)
	}
	if len(check) == 0 {
		return nil
	}

	if eventImporter == nil {
		log.Debug().Str("worker", wk.ID).Msg("[workers] no event importer registered, skipping")
		return nil
	}

	return eventImporter(wk.ID, wk.Name, date, json.RawMessage(raw))
}
