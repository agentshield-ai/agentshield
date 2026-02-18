package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/agentshield-ai/agentshield/internal/auth"
	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/feedback"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/store"
	"github.com/agentshield-ai/agentshield/internal/triage"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

var (
	// Compile regex once at package level for better performance
	controlCharsRegex = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)
)

const (
	// MaxRequestBodySize limits request body size to prevent memory exhaustion attacks
	MaxRequestBodySize = 1024 * 1024 // 1MB
	// MaxFieldValueLength limits individual field value length
	MaxFieldValueLength = 10000 // 10KB per field
	// MaxFieldsCount limits number of fields in a request
	MaxFieldsCount = 100
)

// Server represents the HTTP server
type Server struct {
	config          *config.Config
	evaluator       *evaluate.Evaluator
	store           *store.Store
	auth            *auth.Middleware
	feedbackManager *feedback.FeedbackManager
	triager         *triage.Triager
	httpServer      *http.Server
	startTime       time.Time
}

// HealthResponse represents the health check response
type HealthResponse struct {
	Status        string                 `json:"status"`
	Version       string                 `json:"version"`
	UptimeSeconds float64                `json:"uptime_seconds"`
	Config        map[string]interface{} `json:"config"`
}

// FeedbackRequest represents a feedback submission
type FeedbackRequest struct {
	EventID      string `json:"event_id"`
	AlertID      *int64 `json:"alert_id,omitempty"`
	FeedbackType string `json:"feedback_type"` // 'false_positive', 'true_positive', 'improvement'
	Comment      string `json:"comment,omitempty"`
}

// validateStringInput validates and sanitizes string input to prevent injection attacks
func validateStringInput(input string, maxLength int, fieldName string) error {
	// Check length
	if len(input) > maxLength {
		return fmt.Errorf("%s exceeds maximum length of %d characters", fieldName, maxLength)
	}

	// Check for valid UTF-8
	if !utf8.ValidString(input) {
		return fmt.Errorf("%s contains invalid UTF-8 characters", fieldName)
	}

	// Check for control characters (except newline and tab which may be legitimate)
	if controlCharsRegex.MatchString(input) {
		return fmt.Errorf("%s contains forbidden control characters", fieldName)
	}

	return nil
}

// validateEvaluationRequest validates the evaluation request inputs
func validateEvaluationRequest(req *models.EvaluationRequest) error {
	// Validate EventID
	if req.EventID == "" {
		return fmt.Errorf("event_id is required")
	}
	if err := validateStringInput(req.EventID, 256, "event_id"); err != nil {
		return err
	}

	// Validate SessionID
	if req.SessionID != "" {
		if err := validateStringInput(req.SessionID, 256, "session_id"); err != nil {
			return err
		}
	}

	// Validate Tool
	if req.Tool != "" {
		if err := validateStringInput(req.Tool, 100, "tool"); err != nil {
			return err
		}
	}

	// Validate Args map
	if req.Args != nil {
		if len(req.Args) > MaxFieldsCount {
			return fmt.Errorf("too many args fields (max %d)", MaxFieldsCount)
		}

		for key, value := range req.Args {
			if err := validateStringInput(key, 100, "args key"); err != nil {
				return err
			}
			if err := validateStringInput(value, MaxFieldValueLength, "args value"); err != nil {
				return err
			}
		}
	}

	// Validate Fields map
	if req.Fields != nil {
		if len(req.Fields) > MaxFieldsCount {
			return fmt.Errorf("too many fields (max %d)", MaxFieldsCount)
		}

		for key, value := range req.Fields {
			if err := validateStringInput(key, 100, "fields key"); err != nil {
				return err
			}
			if err := validateStringInput(value, MaxFieldValueLength, "fields value"); err != nil {
				return err
			}
		}
	}

	return nil
}

// NewServer creates a new HTTP server instance
func NewServer(cfg *config.Config, evaluator *evaluate.Evaluator, store *store.Store, triager *triage.Triager) (*Server, error) {
	// Create auth middleware if token is configured
	var authMiddleware *auth.Middleware
	if cfg.Auth.Token != "" {
		var err error
		authMiddleware, err = auth.NewMiddleware(cfg.Auth.Token)
		if err != nil {
			return nil, fmt.Errorf("creating auth middleware: %w", err)
		}
	}

	// Create feedback manager
	feedbackManager := feedback.NewFeedbackManager(store)

	return &Server{
		config:          cfg,
		evaluator:       evaluator,
		store:           store,
		auth:            authMiddleware,
		feedbackManager: feedbackManager,
		triager:         triager,
		startTime:       time.Now(),
	}, nil
}

// requestLogger creates a custom request logging middleware using slog
func (s *Server) requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		
		// Create a response writer wrapper to capture status code
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		
		// Process request
		next.ServeHTTP(ww, r)
		
		// Log request details
		duration := time.Since(start)
		slog.Info("HTTP request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", ww.Status(),
			"bytes", ww.BytesWritten(),
			"duration", duration,
			"remote_addr", r.RemoteAddr,
			"user_agent", r.Header.Get("User-Agent"),
		)
	})
}

// Start starts the HTTP server
func (s *Server) Start() error {
	r := chi.NewRouter()

	// Add Chi middleware chain
	r.Use(middleware.Recoverer)     // Panic recovery
	r.Use(middleware.RealIP)        // Real IP detection
	r.Use(middleware.RequestID)     // Request ID generation
	r.Use(s.requestLogger)          // Custom request logging
	r.Use(middleware.Timeout(30 * time.Second)) // Request timeout

	// Apply auth middleware if configured, but skip health endpoints
	if s.auth != nil {
		r.Use(s.auth.ChiMiddleware)
	} else {
		slog.Warn("No auth token configured - server will accept all requests!")
	}

	// API v1 routes
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/evaluate", s.handleEvaluate)
		r.Get("/health", s.handleHealth)
		r.Get("/alerts", s.handleAlerts)
		r.Route("/feedback", func(r chi.Router) {
			r.Post("/", s.handleFeedbackSubmission)
			r.Get("/", s.handleFeedbackQuery)
		})
	})

	// Legacy routes for backwards compatibility
	r.Get("/health", s.handleHealth)
	r.Post("/evaluate", s.handleEvaluate)

	// Create HTTP server with security settings
	s.httpServer = &http.Server{
		Addr:         s.config.ListenAddr(),
		Handler:      r,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  120 * time.Second,
		// Security headers and limits
		MaxHeaderBytes: 1 << 20, // 1MB
	}

	slog.Info("Starting AgentShield server", "addr", s.config.ListenAddr(), "mode", string(s.config.EvaluationMode))

	return s.httpServer.ListenAndServe()
}

// Shutdown gracefully shuts down the server
func (s *Server) Shutdown(ctx context.Context) error {
	slog.Info("Shutting down server...")
	return s.httpServer.Shutdown(ctx)
}

// handleEvaluate handles the main evaluation endpoint
func (s *Server) handleEvaluate(w http.ResponseWriter, r *http.Request) {

	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)

	var req models.EvaluationRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		// SECURITY: Don't leak internal error details
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
		}
		slog.Warn("JSON decode error", "error", err, "remote_addr", r.RemoteAddr)
		return
	}

	// SECURITY: Validate all input fields
	if err := validateEvaluationRequest(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid input: %v", err), http.StatusBadRequest)
		slog.Warn("Input validation failed", "error", err, "event_id", req.EventID, "remote_addr", r.RemoteAddr)
		return
	}

	// Auto-build fields from plugin format if fields map is empty.
	// The OpenClaw plugin sends: event_type, command, tool_name, params, etc.
	// as top-level JSON fields. The rule engine expects a flat "fields" map.
	if req.Fields == nil {
		req.Fields = make(map[string]string)
	}
	// Always populate fields from top-level request data if not already present
	if req.Tool != "" {
		if _, ok := req.Fields["tool"]; !ok {
			req.Fields["tool"] = req.Tool
		}
	}
	if _, ok := req.Fields["event_type"]; !ok {
		req.Fields["event_type"] = "tool_call"
	}
	// Extract command from args if not in fields
	if _, ok := req.Fields["command"]; !ok {
		if cmd, ok := req.Args["command"]; ok {
			req.Fields["command"] = cmd
		}
	}
	// Copy all args into fields for broad rule matching
	for k, v := range req.Args {
		if _, exists := req.Fields[k]; !exists {
			req.Fields[k] = v
		}
	}

	// Evaluate the request
	response, err := s.evaluator.Evaluate(&req)
	if err != nil {
		slog.Error("Evaluation failed", "event_id", req.EventID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Store alerts in database
	if len(response.Alerts) > 0 {
		for _, alert := range response.Alerts {
			if alert.Matched {
				argsJSON := ""
				if req.Args != nil {
					argsBytes, _ := json.Marshal(req.Args)
					argsJSON = string(argsBytes)
				}

				dbAlert := &store.Alert{
					RuleName:    alert.RuleName,
					Severity:    string(alert.Severity),
					Tool:        req.Tool,
					Args:        argsJSON,
					ActionTaken: string(response.Action),
					Timestamp:   response.Timestamp,
					SessionID:   req.SessionID,
					EventID:     req.EventID,
				}

				if err := s.store.InsertAlert(dbAlert); err != nil {
					slog.Error("Failed to store alert", "event_id", req.EventID, "error", err)
					// Continue processing - don't fail the request due to storage issues
				}
			}
		}
	}

	// Return response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(response)
}

// handleHealth handles the health check endpoint
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {

	// Check store health
	storeHealthy := true
	if err := s.store.Health(); err != nil {
		slog.Warn("Store health check failed", "error", err)
		storeHealthy = false
	}

	status := "ok"
	statusCode := http.StatusOK

	if !storeHealthy {
		status = "degraded"
		statusCode = http.StatusServiceUnavailable
	}

	// SECURITY: Limit information disclosure in health endpoint
	response := HealthResponse{
		Status:        status,
		Version:       "1.0.0", // TODO: Get from build info
		UptimeSeconds: time.Since(s.startTime).Seconds(),
		Config: map[string]interface{}{
			"evaluation_mode": s.config.EvaluationMode,
			"rules_dir":       s.config.Rules.Dir,
			"store_healthy":   storeHealthy,
			"auth_enabled":    s.auth != nil,
		},
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(response)
}

// handleAlerts handles the alerts listing endpoint
func (s *Server) handleAlerts(w http.ResponseWriter, r *http.Request) {

	// Parse query parameters
	query := &store.AlertQuery{
		Limit: 100, // Default limit
	}

	// Parse limit
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if limit, err := strconv.Atoi(limitStr); err == nil && limit > 0 && limit <= 1000 {
			query.Limit = limit
		}
	}

	// Parse offset
	if offsetStr := r.URL.Query().Get("offset"); offsetStr != "" {
		if offset, err := strconv.Atoi(offsetStr); err == nil && offset >= 0 {
			query.Offset = offset
		}
	}

	// Parse since timestamp
	if sinceStr := r.URL.Query().Get("since"); sinceStr != "" {
		if since, err := time.Parse(time.RFC3339, sinceStr); err == nil {
			query.Since = &since
		}
	}

	// Parse until timestamp
	if untilStr := r.URL.Query().Get("until"); untilStr != "" {
		if until, err := time.Parse(time.RFC3339, untilStr); err == nil {
			query.Until = &until
		}
	}

	// Parse severity filter with validation
	if severity := r.URL.Query().Get("severity"); severity != "" {
		validSeverities := []string{"low", "medium", "high", "critical"}
		validSev := false
		for _, vs := range validSeverities {
			if severity == vs {
				validSev = true
				break
			}
		}
		if !validSev {
			http.Error(w, "Invalid severity (must be low, medium, high, or critical)", http.StatusBadRequest)
			return
		}
		query.Severity = severity
	}

	// Parse rule filter with validation
	if rule := r.URL.Query().Get("rule"); rule != "" {
		if err := validateStringInput(rule, 200, "rule"); err != nil {
			http.Error(w, fmt.Sprintf("Invalid rule parameter: %v", err), http.StatusBadRequest)
			return
		}
		query.Rule = rule
	}

	// Parse session_id filter with validation
	if sessionID := r.URL.Query().Get("session_id"); sessionID != "" {
		if err := validateStringInput(sessionID, 256, "session_id"); err != nil {
			http.Error(w, fmt.Sprintf("Invalid session_id parameter: %v", err), http.StatusBadRequest)
			return
		}
		query.SessionID = sessionID
	}

	// Query alerts
	alerts, err := s.store.QueryAlerts(query)
	if err != nil {
		slog.Error("Failed to query alerts", "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Get total count for pagination
	totalCount, err := s.store.CountAlerts(query)
	if err != nil {
		slog.Error("Failed to count alerts", "error", err)
		// Continue without count info
		totalCount = -1
	}

	// Build response
	response := map[string]interface{}{
		"alerts":      alerts,
		"total_count": totalCount,
		"limit":       query.Limit,
		"offset":      query.Offset,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}

// handleFeedback handles feedback submission and retrieval (legacy, keeping for backward compatibility)
func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		s.handleFeedbackSubmission(w, r)
	case http.MethodGet:
		s.handleFeedbackQuery(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

// handleFeedbackSubmission handles POST /api/v1/feedback
func (s *Server) handleFeedbackSubmission(w http.ResponseWriter, r *http.Request) {
	// Limit request body size
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)

	var req FeedbackRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request format", http.StatusBadRequest)
		slog.Warn("Feedback JSON decode error", "error", err, "remote_addr", r.RemoteAddr)
		return
	}

	// SECURITY: Validate feedback input
	if err := validateStringInput(req.EventID, 256, "event_id"); err != nil {
		http.Error(w, fmt.Sprintf("Invalid event_id: %v", err), http.StatusBadRequest)
		return
	}
	
	validFeedbackTypes := []string{"false_positive", "true_positive", "improvement"}
	validType := false
	for _, vt := range validFeedbackTypes {
		if req.FeedbackType == vt {
			validType = true
			break
		}
	}
	if !validType {
		http.Error(w, "Invalid feedback_type (must be false_positive, true_positive, or improvement)", http.StatusBadRequest)
		return
	}

	if req.Comment != "" {
		if err := validateStringInput(req.Comment, 2000, "comment"); err != nil {
			http.Error(w, fmt.Sprintf("Invalid comment: %v", err), http.StatusBadRequest)
			return
		}
	}

	// Get rule name from the alert if not provided directly
	var ruleName string
	if req.AlertID != nil {
		alertQuery := &store.AlertQuery{
			EventID: req.EventID,
			Limit:   1,
		}
		alerts, err := s.store.QueryAlerts(alertQuery)
		if err == nil && len(alerts) > 0 {
			ruleName = alerts[0].RuleName
		}
	}

	// Create feedback object
	fb := &feedback.Feedback{
		AlertID:   req.EventID, // Use EventID as AlertID for now
		RuleName:  ruleName,
		Verdict:   req.FeedbackType,
		Comment:   req.Comment,
		Timestamp: time.Now(),
	}

	// Validate and store feedback
	if err := s.feedbackManager.SubmitFeedback(fb); err != nil {
		slog.Error("Failed to store feedback", "error", err)
		if strings.Contains(err.Error(), "invalid feedback type") ||
		   strings.Contains(err.Error(), "required") ||
		   strings.Contains(err.Error(), "too long") {
			http.Error(w, err.Error(), http.StatusBadRequest)
		} else {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	// Return success
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(map[string]string{
		"status":  "received",
		"message": "Thank you for your feedback",
	})
}

// handleFeedbackQuery handles GET /api/v1/feedback?rule=<name>
func (s *Server) handleFeedbackQuery(w http.ResponseWriter, r *http.Request) {
	ruleName := r.URL.Query().Get("rule")
	if ruleName == "" {
		http.Error(w, "rule parameter is required", http.StatusBadRequest)
		return
	}

	// Parse limit
	limit := 100 // Default
	if limitStr := r.URL.Query().Get("limit"); limitStr != "" {
		if parsedLimit, err := strconv.Atoi(limitStr); err == nil && parsedLimit > 0 && parsedLimit <= 1000 {
			limit = parsedLimit
		}
	}

	// Get feedback from store
	feedbacks, err := s.store.GetFeedbackForRule(ruleName, limit)
	if err != nil {
		slog.Error("Failed to get feedback for rule", "rule_name", ruleName, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Get rule statistics
	fpRate, err := s.store.GetRuleFPRate(ruleName)
	if err != nil {
		slog.Warn("Failed to get FP rate for rule", "rule_name", ruleName, "error", err)
		fpRate = 0
	}

	response := map[string]interface{}{
		"rule_name":           ruleName,
		"feedback":            feedbacks,
		"false_positive_rate": fpRate,
		"total_feedback":      len(feedbacks),
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(response)
}