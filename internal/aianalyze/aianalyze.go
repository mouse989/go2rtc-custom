// Package aianalyze hosts the published "AI ANALYZE"/"WATCH 30S" detector
// model — a custom-trained YOLO network exported to ONNX — that the
// browser's own Web Worker (www/ai-analyze.worker.js) downloads once and
// runs entirely on-device via ONNX Runtime Web (WASM). This package never
// runs the model itself and never sees a detection result: it only stores
// the published .onnx binary plus the label metadata read off it at
// export time (internal/counting's /export-onnx proxy), and serves both
// to any user holding the AllowAIAnalyze permission.
//
// This is deliberately a separate, much lighter concept from
// internal/counting's own server-side GPU vehicle-counting pipeline
// (yolo_counter): that one runs continuously per camera and needs a
// capable worker machine; this one runs a single frame (or a 30s burst of
// them) entirely in the visitor's own browser, with zero server-side
// inference cost. An admin "publishes" one exported model here — picked
// from whichever .pt weights internal/counting already manages — making
// it live for every AllowAIAnalyze user, independent of which model is
// currently running live counting.
package aianalyze

import (
	"os"
	"path/filepath"

	"github.com/AlexxIT/go2rtc/internal/app"
	"github.com/rs/zerolog"
)

var log zerolog.Logger

var dataDir string

// Init loads the currently published model's metadata (if any) and
// registers HTTP handlers. Call once at startup.
func Init() {
	log = app.GetLogger("aianalyze")

	dataDir = "ai_analyze_data"
	if app.ConfigPath != "" {
		dataDir = filepath.Join(filepath.Dir(app.ConfigPath), "ai_analyze_data")
	}
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		log.Error().Err(err).Msg("[aianalyze] cannot create data dir")
	}

	loadMeta()
	registerHandlers()

	log.Info().Str("dir", dataDir).Msg("[aianalyze] ready")
}

func modelFile() string { return filepath.Join(dataDir, "model.onnx") }
func metaFile() string  { return filepath.Join(dataDir, "model.json") }
