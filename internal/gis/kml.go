package gis

// kml.go — server-side import of a Point/LineString KML or KMZ file into an
// existing Layer's Features. Deliberately server-side (unlike
// internal/auth's region KML importer, which parses client-side in
// map.html's own JS) specifically so a .kmz's embedded icon image — a file
// INSIDE the zip archive, not reachable by a plain browser <input
// type=file> without a zip library — can be captured and reused as the
// Layer's marker icon instead of always falling back to a generic colored
// circle. See icon.go for where the captured icon ends up.

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

type kmlRoot struct {
	Document kmlContainer `xml:"Document"`
}

// kmlContainer covers both <Document> and <Folder> — some exports (My Maps
// included, when a map has several "layers"/sheets) nest Placemarks one
// level deeper inside Folders; walking both shapes means it doesn't matter
// which one a given export used.
type kmlContainer struct {
	Styles     []kmlStyle     `xml:"Style"`
	StyleMaps  []kmlStyleMap  `xml:"StyleMap"`
	Placemarks []kmlPlacemark `xml:"Placemark"`
	Folders    []kmlContainer `xml:"Folder"`
}

type kmlStyle struct {
	ID        string        `xml:"id,attr"`
	IconStyle *kmlIconStyle `xml:"IconStyle"`
}

type kmlIconStyle struct {
	Icon struct {
		Href string `xml:"href"`
	} `xml:"Icon"`
}

type kmlStyleMap struct {
	ID    string         `xml:"id,attr"`
	Pairs []kmlStylePair `xml:"Pair"`
}

type kmlStylePair struct {
	Key      string `xml:"key"`
	StyleURL string `xml:"styleUrl"`
}

type kmlPlacemark struct {
	Name       string       `xml:"name"`
	StyleURL   string       `xml:"styleUrl"`
	Point      *kmlGeometry `xml:"Point"`
	LineString *kmlGeometry `xml:"LineString"`
}

type kmlGeometry struct {
	Coordinates string `xml:"coordinates"`
}

// ImportResult summarizes one completed import.
type ImportResult struct {
	PointsCreated   int  `json:"points_created"`
	SegmentsCreated int  `json:"segments_created"`
	Failed          int  `json:"failed"`
	IconCaptured    bool `json:"icon_captured"`
}

// ImportKML parses raw (a .kml or .kmz file's bytes) and creates one
// Feature in layerID per Point/LineString Placemark found — pointRadiusM
// for points, segmentRadiusM for segments. A radius of 0 skips that kind
// entirely (lets a caller import only the geometry kind they have a
// sensible radius for, without erroring on the other).
//
// If any Point Placemark's style resolves to an IconStyle whose <Icon>
// <href> points at a file INSIDE a .kmz archive (never an http(s) URL —
// fetching a remote icon isn't attempted, to keep this a pure local-file
// operation), the first such icon found is captured as the layer's own
// marker icon via SetLayerIcon. A plain .kml (no archive) never has an
// icon to capture — only its geometry is imported.
func ImportKML(layerID string, raw []byte, pointRadiusM, segmentRadiusM float64) (ImportResult, error) {
	if _, ok := GetLayer(layerID); !ok {
		return ImportResult{}, errLayerNotFound
	}

	kmlBytes := raw
	var archiveFiles map[string][]byte

	if looksLikeZip(raw) {
		zr, err := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
		if err != nil {
			return ImportResult{}, fmt.Errorf("invalid .kmz (zip) file: %w", err)
		}
		archiveFiles = make(map[string][]byte, len(zr.File))
		var kmlName string
		for _, f := range zr.File {
			if f.FileInfo().IsDir() {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				continue
			}
			data, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				continue
			}
			archiveFiles[f.Name] = data
			lower := strings.ToLower(f.Name)
			if kmlName == "" && strings.HasSuffix(lower, ".kml") {
				kmlName = f.Name
			} else if strings.EqualFold(f.Name, "doc.kml") {
				kmlName = f.Name // prefer the conventional entry name if present
			}
		}
		if kmlName == "" {
			return ImportResult{}, fmt.Errorf(".kmz file has no .kml entry inside")
		}
		kmlBytes = archiveFiles[kmlName]
	}

	var root kmlRoot
	if err := xml.Unmarshal(kmlBytes, &root); err != nil {
		return ImportResult{}, fmt.Errorf("invalid KML XML: %w", err)
	}

	result, err := importContainer(layerID, root.Document, archiveFiles, pointRadiusM, segmentRadiusM)
	return result, err
}

func importContainer(layerID string, root kmlContainer, archiveFiles map[string][]byte, pointRadiusM, segmentRadiusM float64) (ImportResult, error) {
	styleIconHref := map[string]string{}  // style id -> Icon href
	styleMapNormal := map[string]string{} // StyleMap id -> target style id (its "normal" pair)

	var collectStyles func(c kmlContainer)
	collectStyles = func(c kmlContainer) {
		for _, s := range c.Styles {
			if s.IconStyle != nil && s.IconStyle.Icon.Href != "" {
				styleIconHref[s.ID] = s.IconStyle.Icon.Href
			}
		}
		for _, sm := range c.StyleMaps {
			for _, p := range sm.Pairs {
				if p.Key == "normal" {
					styleMapNormal[sm.ID] = strings.TrimPrefix(p.StyleURL, "#")
				}
			}
		}
		for _, sub := range c.Folders {
			collectStyles(sub)
		}
	}
	collectStyles(root)

	resolveIconHref := func(styleURL string) string {
		id := strings.TrimPrefix(styleURL, "#")
		if href, ok := styleIconHref[id]; ok {
			return href
		}
		if target, ok := styleMapNormal[id]; ok {
			return styleIconHref[target]
		}
		return ""
	}

	var result ImportResult
	iconCaptured := false

	var walk func(c kmlContainer)
	walk = func(c kmlContainer) {
		for _, pm := range c.Placemarks {
			switch {
			case pm.Point != nil && pointRadiusM > 0:
				pts := parseKMLCoordinates(pm.Point.Coordinates)
				if len(pts) == 0 {
					result.Failed++
					continue
				}
				f := &Feature{
					LayerID: layerID, Label: strings.TrimSpace(pm.Name), Kind: FeatureKindPoint,
					Lng: pts[0][0], Lat: pts[0][1], RadiusM: pointRadiusM,
				}
				if err := CreateFeature(f); err != nil {
					result.Failed++
				} else {
					result.PointsCreated++
				}
				if !iconCaptured && archiveFiles != nil {
					if href := resolveIconHref(pm.StyleURL); href != "" && !strings.Contains(href, "://") {
						if data, ok := archiveFiles[href]; ok && len(data) > 0 {
							if ct := sniffImageContentType(data); ct != "" {
								if err := SetLayerIcon(layerID, data, ct); err == nil {
									iconCaptured = true
								}
							}
						}
					}
				}

			case pm.LineString != nil && segmentRadiusM > 0:
				pts := parseKMLCoordinates(pm.LineString.Coordinates)
				if len(pts) < 2 {
					result.Failed++
					continue
				}
				f := &Feature{
					LayerID: layerID, Label: strings.TrimSpace(pm.Name), Kind: FeatureKindSegment,
					Line: pts, RadiusM: segmentRadiusM,
				}
				if err := CreateFeature(f); err != nil {
					result.Failed++
				} else {
					result.SegmentsCreated++
				}
			}
		}
		for _, sub := range c.Folders {
			walk(sub)
		}
	}
	walk(root)

	result.IconCaptured = iconCaptured
	return result, nil
}

func looksLikeZip(b []byte) bool {
	return len(b) >= 4 && b[0] == 'P' && b[1] == 'K' && b[2] == 0x03 && b[3] == 0x04
}

// parseKMLCoordinates parses KML's "lon,lat,alt lon,lat,alt ..." coordinate
// text (whitespace/newline separated tuples) into [lng,lat] pairs — mirrors
// map.html's own parseKmlCoordinates() exactly, including dropping a
// trailing point that exactly repeats the first (this app's open-ring
// convention for closed shapes — see Region.Polygons' own doc comment).
func parseKMLCoordinates(text string) [][2]float64 {
	fields := strings.Fields(text)
	pts := make([][2]float64, 0, len(fields))
	for _, tuple := range fields {
		parts := strings.Split(tuple, ",")
		if len(parts) < 2 {
			continue
		}
		lng, err1 := strconv.ParseFloat(parts[0], 64)
		lat, err2 := strconv.ParseFloat(parts[1], 64)
		if err1 != nil || err2 != nil {
			continue
		}
		pts = append(pts, [2]float64{lng, lat})
	}
	if len(pts) > 1 && pts[0] == pts[len(pts)-1] {
		pts = pts[:len(pts)-1]
	}
	return pts
}

func sniffImageContentType(data []byte) string {
	ct := http.DetectContentType(data)
	if strings.HasPrefix(ct, "image/") {
		return ct
	}
	return ""
}
