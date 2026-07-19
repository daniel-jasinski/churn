package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"churn/internal/analytics"
)

// Workspace settings — the §3.4 recommendation weights — are deliberately
// NOT in the event log: the log records decisions, never advice (§3.4
// "recommendations are advice given now, not reproducible historical
// artifacts"). They live in settings.json in the data directory, written
// atomically (temp file + rename); absent file means
// analytics.DefaultSettings. Spec-conformant: settings are not facts.

// settingsFileName is the settings file inside the data directory.
const settingsFileName = "settings.json"

// weightsDTO is the wire and file form of the §3.4 weights. Pointers make
// PUT explicit: all five fields are required (full replacement, like every
// supersession in this system).
type weightsDTO struct {
	ImmediateUnlock *float64 `json:"immediate_unlock"`
	DownstreamReach *float64 `json:"downstream_reach"`
	RemainingDepth  *float64 `json:"remaining_depth"`
	WaitingAge      *float64 `json:"waiting_age"`
	ScarcityPenalty *float64 `json:"scarcity_penalty"`
}

func buildWeightsDTO(s analytics.Settings) weightsDTO {
	return weightsDTO{
		ImmediateUnlock: &s.ImmediateUnlock,
		DownstreamReach: &s.DownstreamReach,
		RemainingDepth:  &s.RemainingDepth,
		WaitingAge:      &s.WaitingAge,
		ScarcityPenalty: &s.ScarcityPenalty,
	}
}

// toSettings validates the DTO (all five fields present and finite) and
// returns the analytics settings.
func (w weightsDTO) toSettings() (analytics.Settings, *apiError) {
	var s analytics.Settings
	for _, f := range []struct {
		name string
		src  *float64
		dst  *float64
	}{
		{"immediate_unlock", w.ImmediateUnlock, &s.ImmediateUnlock},
		{"downstream_reach", w.DownstreamReach, &s.DownstreamReach},
		{"remaining_depth", w.RemainingDepth, &s.RemainingDepth},
		{"waiting_age", w.WaitingAge, &s.WaitingAge},
		{"scarcity_penalty", w.ScarcityPenalty, &s.ScarcityPenalty},
	} {
		if f.src == nil {
			return s, errBadRequest("settings: %s is required (PUT is full replacement)", f.name)
		}
		if math.IsNaN(*f.src) || math.IsInf(*f.src, 0) {
			return s, errBadRequest("settings: %s must be a finite number", f.name)
		}
		if *f.src < 0 {
			// The formula's one subtraction (scarcity) is built in (§3.4); a
			// negative weight would silently invert a term's meaning.
			return s, errBadRequest("settings: %s must be >= 0 (the scarcity penalty is already subtracted)", f.name)
		}
		*f.dst = *f.src
	}
	return s, nil
}

// settingsFile is the settings store: one small JSON file, mutex-guarded,
// atomic writes.
type settingsFile struct {
	mu   sync.Mutex
	path string
}

func newSettingsFile(dataDir string) *settingsFile {
	return &settingsFile{path: filepath.Join(dataDir, settingsFileName)}
}

// load reads the current settings; a missing file yields the defaults, a
// corrupt file a 500 (loud, not silently reset).
func (f *settingsFile) load() (analytics.Settings, *apiError) {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := os.ReadFile(f.path)
	if errors.Is(err, os.ErrNotExist) {
		return analytics.DefaultSettings(), nil
	}
	if err != nil {
		return analytics.Settings{}, &apiError{status: http.StatusInternalServerError,
			kind: "internal", message: fmt.Sprintf("reading %s: %v", settingsFileName, err)}
	}
	var dto weightsDTO
	if err := json.Unmarshal(b, &dto); err != nil {
		return analytics.Settings{}, &apiError{status: http.StatusInternalServerError,
			kind: "internal", message: fmt.Sprintf("%s is corrupt: %v", settingsFileName, err)}
	}
	s, e := dto.toSettings()
	if e != nil {
		return analytics.Settings{}, &apiError{status: http.StatusInternalServerError,
			kind: "internal", message: fmt.Sprintf("%s is invalid: %s", settingsFileName, e.message)}
	}
	return s, nil
}

// save writes the settings atomically: marshal to settings.json.tmp, then
// rename over settings.json — a failed write can never leave a partial or
// corrupt settings file behind.
func (f *settingsFile) save(s analytics.Settings) *apiError {
	f.mu.Lock()
	defer f.mu.Unlock()
	b, err := json.MarshalIndent(buildWeightsDTO(s), "", "  ")
	if err != nil {
		return &apiError{status: http.StatusInternalServerError, kind: "internal", message: err.Error()}
	}
	tmp := f.path + ".tmp"
	if err := os.WriteFile(tmp, append(b, '\n'), 0o644); err != nil {
		os.Remove(tmp)
		return &apiError{status: http.StatusInternalServerError, kind: "internal",
			message: fmt.Sprintf("writing %s: %v", settingsFileName, err)}
	}
	if err := os.Rename(tmp, f.path); err != nil {
		os.Remove(tmp)
		return &apiError{status: http.StatusInternalServerError, kind: "internal",
			message: fmt.Sprintf("publishing %s: %v", settingsFileName, err)}
	}
	return nil
}

// getSettings implements GET /api/v1/settings.
func (s *Server) getSettings(rw http.ResponseWriter, _ *http.Request) {
	settings, e := s.settings.load()
	if e != nil {
		writeError(rw, e)
		return
	}
	writeJSON(rw, http.StatusOK, buildWeightsDTO(settings))
}

// putSettings implements PUT /api/v1/settings: full replacement of the five
// weights, persisted atomically.
func (s *Server) putSettings(rw http.ResponseWriter, r *http.Request) {
	var dto weightsDTO
	if e := decodeJSON(r, &dto); e != nil {
		writeError(rw, e)
		return
	}
	settings, e := dto.toSettings()
	if e != nil {
		writeError(rw, e)
		return
	}
	if e := s.settings.save(settings); e != nil {
		writeError(rw, e)
		return
	}
	writeJSON(rw, http.StatusOK, buildWeightsDTO(settings))
}
