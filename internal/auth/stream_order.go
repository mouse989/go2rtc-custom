package auth

// go2rtc's own streams config is parsed into plain Go maps (see
// internal/streams), which lose YAML key order the instant they're
// unmarshalled — so by the time a stream reaches /api/proxy/streams there
// is no way to tell "declared 1st in go2rtc.yaml" from "declared last".
// That matters for the map's "Set location" picker: an admin appends new
// cameras at the end of the file, and wants the picker to keep that order
// so newly-added, not-yet-placed cameras are easy to spot at the bottom of
// the list instead of scattered alphabetically among hundreds of others.
//
// This re-reads the raw config file (independent of the in-memory streams
// map) and walks its YAML node tree — which does preserve document order —
// to recover the declaration order of the top-level "streams:" mapping.

import (
	"os"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"gopkg.in/yaml.v3"
)

var (
	streamOrderMu      sync.Mutex
	streamOrderCache   map[string]int
	streamOrderCacheAt time.Time
)

// streamOrderCacheTTL keeps this from re-reading and re-parsing the config
// file on every /api/proxy/streams request (called often, by every page),
// while still picking up a freshly-added camera within a few seconds of
// editing go2rtc.yaml — no server restart needed.
const streamOrderCacheTTL = 5 * time.Second

// streamFileOrder returns stream name → its index in go2rtc.yaml's
// top-level "streams:" mapping. A name absent from the map means it wasn't
// found there (e.g. added through some other config source) — callers
// should sort those after every known-order entry.
func streamFileOrder() map[string]int {
	streamOrderMu.Lock()
	defer streamOrderMu.Unlock()
	if streamOrderCache != nil && time.Since(streamOrderCacheAt) < streamOrderCacheTTL {
		return streamOrderCache
	}

	order := parseStreamOrder(app.ConfigPath)
	streamOrderCache = order
	streamOrderCacheAt = time.Now()
	return order
}

func parseStreamOrder(path string) map[string]int {
	order := map[string]int{}
	if path == "" {
		return order
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return order
	}

	var root yaml.Node
	if err = yaml.Unmarshal(data, &root); err != nil || len(root.Content) == 0 {
		return order
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return order
	}

	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "streams" {
			continue
		}
		streamsNode := doc.Content[i+1]
		if streamsNode.Kind != yaml.MappingNode {
			break
		}
		idx := 0
		for j := 0; j+1 < len(streamsNode.Content); j += 2 {
			order[streamsNode.Content[j].Value] = idx
			idx++
		}
		break
	}
	return order
}
