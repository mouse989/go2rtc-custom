package aianalyze

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// maxModelUploadBytes bounds a published .onnx — a YOLO nano/small model
// exported at a web-friendly input size (416/640) normally lands well
// under this; it exists only to reject an obviously-wrong upload (e.g. a
// .pt file dropped in by mistake) rather than to accommodate any
// legitimate model near this size.
const maxModelUploadBytes = 300 << 20 // 300MB

var errNoModel = errors.New("no AI Analyze model has been published yet")

// ModelMeta is the label/shape metadata read off a published model at
// export time (internal/counting's /export-onnx proxy reads model.names
// from the actual .pt — nothing here is ever hardcoded per-deployment).
type ModelMeta struct {
	Labels        []string  `json:"labels"`
	VehicleLabels []string  `json:"vehicleLabels"`
	PersonLabels  []string  `json:"personLabels,omitempty"`
	InputSize     int       `json:"inputSize"`
	Format        string    `json:"format"`                // "ultralytics" — the only decoder www/ai-analyze.worker.js implements today
	SourceModel   string    `json:"sourceModel,omitempty"` // which .pt this was exported from, for admin traceability only
	SizeBytes     int64     `json:"sizeBytes"`
	UpdatedAt     time.Time `json:"updated_at"`
	UpdatedBy     string    `json:"updated_by,omitempty"`
}

var (
	metaMu  sync.RWMutex
	meta    ModelMeta
	hasMeta bool
)

func loadMeta() {
	data, err := os.ReadFile(metaFile())
	if err != nil {
		return // not published yet — hasMeta stays false
	}
	var m ModelMeta
	if err := json.Unmarshal(data, &m); err != nil {
		log.Error().Err(err).Msg("[aianalyze] corrupt model.json, ignoring")
		return
	}
	metaMu.Lock()
	meta = m
	hasMeta = true
	metaMu.Unlock()
}

func saveMetaLocked() error {
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(metaFile(), data, 0644)
}

// GetModelMeta returns the currently published model's metadata, if any.
func GetModelMeta() (ModelMeta, bool) {
	metaMu.RLock()
	defer metaMu.RUnlock()
	return meta, hasMeta
}

// HasModel reports whether a model has been published and its binary is
// present on disk (the two can briefly disagree only if the data dir was
// tampered with outside this package).
func HasModel() bool {
	if _, ok := GetModelMeta(); !ok {
		return false
	}
	_, err := os.Stat(modelFile())
	return err == nil
}

// PublishModel validates and stores data as the new active .onnx, along
// with the metadata describing it, atomically enough for this use case
// (admin-only, infrequent, single active writer): the binary is written
// to a temp file and renamed into place so a reader never sees a
// half-written model.onnx mid-publish.
func PublishModel(data []byte, m ModelMeta, updatedBy string) (ModelMeta, error) {
	if len(data) == 0 {
		return ModelMeta{}, fmt.Errorf("empty model file")
	}
	if len(data) > maxModelUploadBytes {
		return ModelMeta{}, fmt.Errorf("model file too large (%d bytes, max %d)", len(data), maxModelUploadBytes)
	}
	if len(m.Labels) == 0 {
		return ModelMeta{}, fmt.Errorf("labels required — refusing to publish a model the browser decoder can't name classes for")
	}
	if m.InputSize <= 0 {
		m.InputSize = 416
	}
	if m.Format == "" {
		m.Format = "ultralytics"
	}

	tmp := modelFile() + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return ModelMeta{}, fmt.Errorf("write model file: %w", err)
	}
	if err := os.Rename(tmp, modelFile()); err != nil {
		_ = os.Remove(tmp)
		return ModelMeta{}, fmt.Errorf("finalize model file: %w", err)
	}

	m.SizeBytes = int64(len(data))
	m.UpdatedAt = time.Now().UTC()
	m.UpdatedBy = updatedBy

	metaMu.Lock()
	meta = m
	hasMeta = true
	err := saveMetaLocked()
	metaMu.Unlock()
	if err != nil {
		return ModelMeta{}, fmt.Errorf("save metadata: %w", err)
	}
	return m, nil
}
