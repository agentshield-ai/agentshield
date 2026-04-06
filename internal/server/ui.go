package server

import (
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
)

//go:embed ui
var uiFS embed.FS

// parseQueryInt parses an integer query parameter with bounds validation.
func parseQueryInt(r *http.Request, key string, defaultVal, minVal, maxVal int) int {
	if v := r.URL.Query().Get(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= minVal && n <= maxVal {
			return n
		}
	}
	return defaultVal
}

// uiHandler serves the embedded SIEM Investigation Console.
func (s *Server) uiHandler() http.Handler {
	sub, _ := fs.Sub(uiFS, "ui")
	fileServer := http.FileServer(http.FS(sub))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Relax CSP for the UI: allow inline styles, scripts from self, data: URIs for SVG favicon
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; connect-src 'self'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")

		// Serve index.html for /, /ui, /ui/ (SPA fallback)
		path := strings.TrimPrefix(r.URL.Path, "/ui")
		path = strings.TrimPrefix(path, "/")
		if path == "" || path == "index.html" {
			r.URL.Path = "/index.html"
			w.Header().Set("Cache-Control", "no-cache")
		} else {
			// Static assets: cache for 1 hour
			w.Header().Set("Cache-Control", "public, max-age=3600")
		}

		fileServer.ServeHTTP(w, r)
	})
}

// handleStats returns aggregate statistics for the dashboard.
func (s *Server) handleStats(w http.ResponseWriter, r *http.Request) {
	stats, err := s.store.GetStats()
	if err != nil {
		slog.Error("Failed to get stats", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(stats)
}

// handleTimeline returns time-bucketed alert counts.
func (s *Server) handleTimeline(w http.ResponseWriter, r *http.Request) {
	hours := parseQueryInt(r, "hours", 24, 1, 720)
	bucketMin := parseQueryInt(r, "bucket_minutes", 60, 1, 1440)
	data, err := s.store.GetTimeline(hours, bucketMin)
	if err != nil {
		slog.Error("Failed to get timeline", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// handleRules returns rule-level alert summaries.
func (s *Server) handleRules(w http.ResponseWriter, r *http.Request) {
	limit := parseQueryInt(r, "limit", 100, 1, 1000)
	data, err := s.store.GetRuleSummaries(limit)
	if err != nil {
		slog.Error("Failed to get rule summaries", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// handleTopTools returns the most frequent tools in alerts.
func (s *Server) handleTopTools(w http.ResponseWriter, r *http.Request) {
	limit := parseQueryInt(r, "limit", 10, 1, 100)
	data, err := s.store.GetTopTools(limit)
	if err != nil {
		slog.Error("Failed to get top tools", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// handleSessions returns session summaries.
func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request) {
	limit := parseQueryInt(r, "limit", 100, 1, 1000)
	var since *time.Time
	if v := r.URL.Query().Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			since = &t
		}
	}
	data, err := s.store.GetSessions(limit, since)
	if err != nil {
		slog.Error("Failed to get sessions", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// handleSessionAlerts returns all alerts for a given session.
func (s *Server) handleSessionAlerts(w http.ResponseWriter, r *http.Request) {
	sessionID := chi.URLParam(r, "sessionID")
	if sessionID == "" {
		http.Error(w, "session_id required", http.StatusBadRequest)
		return
	}
	if err := validateStringInput(sessionID, 256, "session_id"); err != nil {
		http.Error(w, "Invalid session_id", http.StatusBadRequest)
		return
	}
	data, err := s.store.GetSessionAlerts(sessionID, 1000)
	if err != nil {
		slog.Error("Failed to get session alerts", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

// handleAlertByID returns a single alert by ID.
func (s *Server) handleAlertByID(w http.ResponseWriter, r *http.Request) {
	idStr := chi.URLParam(r, "alertID")
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "Invalid alert ID", http.StatusBadRequest)
		return
	}
	alert, err := s.store.GetAlertByID(id)
	if err != nil {
		slog.Error("Failed to get alert", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}
	if alert == nil {
		http.Error(w, "Alert not found", http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(alert)
}
