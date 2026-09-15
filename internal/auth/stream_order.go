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
	"errors"
	"os"
	"sync"
	"time"

	"github.com/AlexxIT/go2rtc/internal/app"
	"gopkg.in/yaml.v3"
)

var errNoConfigFile = errors.New("no config file")

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
	names, _ := readDeclaredStreamNames(path)
	order := make(map[string]int, len(names))
	for i, name := range names {
		// First occurrence wins the index — if a key is declared twice
		// (see readDeclaredStreamNames), sorting by its first appearance is
		// the least surprising behavior.
		if _, exists := order[name]; !exists {
			order[name] = i
		}
	}
	return order
}

// readDeclaredStreamNames walks go2rtc.yaml's raw YAML node tree — which,
// unlike the map[string]any go2rtc itself parses config into, preserves
// both document order and duplicate keys — and returns every key declared
// directly under the top-level "streams:" mapping, in file order, with
// duplicates included exactly as they appear. Shared by parseStreamOrder
// (the map's "Set location" picker sort) and the config-audit endpoint
// (config_audit.go), which uses the duplicates to flag a common cause of
// "camera in the file but never actually added": a copy-pasted key that
// silently overwrites an earlier entry once parsed into a Go map.
func readDeclaredStreamNames(path string) ([]string, error) {
	if path == "" {
		return nil, errNoConfigFile
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var root yaml.Node
	if err = yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return []string{}, nil
	}
	doc := root.Content[0]
	if doc.Kind != yaml.MappingNode {
		return []string{}, nil
	}

	for i := 0; i+1 < len(doc.Content); i += 2 {
		if doc.Content[i].Value != "streams" {
			continue
		}
		streamsNode := doc.Content[i+1]
		if streamsNode.Kind != yaml.MappingNode {
			return []string{}, nil
		}
		names := make([]string, 0, len(streamsNode.Content)/2)
		for j := 0; j+1 < len(streamsNode.Content); j += 2 {
			names = append(names, streamsNode.Content[j].Value)
		}
		return names, nil
	}
	return []string{}, nil
}
