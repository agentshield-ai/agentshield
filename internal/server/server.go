package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/agentshield-ai/agentshield/internal/auth"
	"github.com/agentshield-ai/agentshield/internal/cache"
	"github.com/agentshield-ai/agentshield/internal/config"
	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/feedback"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/agentshield-ai/agentshield/internal/store"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/riandyrn/otelchi"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/time/rate"
)

var (
	// Compile regex once at package level for better performance
	controlCharsRegex = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)

	// Allowed enum values, allocated once at package level
	validSeverities = []string{"low", "medium", "high", "critical"}
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
	verdictCache    *cache.VerdictCache
	tracerProvider  trace.TracerProvider
	httpServer      *http.Server
	rateLimiter     *ipRateLimiter
	startTime       time.Time
}

// HealthResponse represents the health check response
type HealthResponse struct {
	Status        string  `json:"status"`
	Version       string  `json:"version"`
	UptimeSeconds float64 `json:"uptime_seconds"`
}

// FeedbackRequest represents a feedback submission
type FeedbackRequest struct {
	EventID      string `json:"event_id"`
	AlertID      *int64 `json:"alert_id,omitempty"`
	FeedbackType string `json:"feedback_type"` // false_positive|true_positive|false_negative (improvement alias accepted)
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

	if req.Context != "" {
		if req.Context != "prod" && req.Context != "test" {
			return fmt.Errorf("context must be 'prod' or 'test'")
		}
	}

	// Validate ToolName (plugin compat alias)
	if req.ToolName != "" {
		if err := validateStringInput(req.ToolName, 100, "tool_name"); err != nil {
			return err
		}
	}

	// Validate Command (plugin compat alias)
	if req.Command != "" {
		if err := validateStringInput(req.Command, MaxFieldValueLength, "command"); err != nil {
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
func NewServer(cfg *config.Config, evaluator *evaluate.Evaluator, store *store.Store, verdictCache *cache.VerdictCache) (*Server, error) {
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
		verdictCache:    verdictCache,
		startTime:       time.Now(),
	}, nil
}

// SetTracerProvider sets the OTel TracerProvider for HTTP-level span creation.
func (s *Server) SetTracerProvider(tp trace.TracerProvider) {
	s.tracerProvider = tp
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

// ipRateLimiter provides per-IP rate limiting using golang.org/x/time/rate.
// Each IP gets its own token-bucket limiter. Stale entries are cleaned up
// periodically to prevent unbounded memory growth.
type ipRateLimiter struct {
	mu       sync.Mutex
	visitors map[string]*ipVisitor
	rps      rate.Limit // sustained requests per second
	burst    int        // maximum burst size
	done     chan struct{}
}

type ipVisitor struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

func newIPRateLimiter(rps rate.Limit, burst int) *ipRateLimiter {
	rl := &ipRateLimiter{
		visitors: make(map[string]*ipVisitor),
		rps:      rps,
		burst:    burst,
		done:     make(chan struct{}),
	}
	go rl.cleanupLoop()
	return rl
}

func (rl *ipRateLimiter) stop() {
	close(rl.done)
}

func (rl *ipRateLimiter) getLimiter(ip string) *rate.Limiter {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	v, exists := rl.visitors[ip]
	if !exists {
		limiter := rate.NewLimiter(rl.rps, rl.burst)
		rl.visitors[ip] = &ipVisitor{limiter: limiter, lastSeen: time.Now()}
		return limiter
	}
	v.lastSeen = time.Now()
	return v.limiter
}

// cleanupLoop removes entries that haven't been seen in 3 minutes.
func (rl *ipRateLimiter) cleanupLoop() {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			rl.mu.Lock()
			cutoff := time.Now().Add(-3 * time.Minute)
			for ip, v := range rl.visitors {
				if v.lastSeen.Before(cutoff) {
					delete(rl.visitors, ip)
				}
			}
			rl.mu.Unlock()
		case <-rl.done:
			return
		}
	}
}

// rateLimitMiddleware returns HTTP middleware that limits requests per IP.
func rateLimitMiddleware(limiter *ipRateLimiter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := r.RemoteAddr
			// Use the actual socket peer address only. Do not trust client-provided
			// forwarding headers for limiter identity by default.
			// Strip port using net.SplitHostPort which correctly handles
			// both IPv4 (1.2.3.4:8080) and IPv6 ([::1]:8080) addresses.
			if host, _, err := net.SplitHostPort(ip); err == nil {
				ip = host
			}

			if !limiter.getLimiter(ip).Allow() {
				slog.Warn("Rate limit exceeded", "remote_addr", ip)
				http.Error(w, "Too many requests", http.StatusTooManyRequests)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// securityHeaders adds standard security response headers to every response.
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'")
		next.ServeHTTP(w, r)
	})
}

// Start starts the HTTP server
func (s *Server) Start() error {
	r := chi.NewRouter()

	// OTel tracing middleware — outermost so every request gets a span
	if s.tracerProvider != nil {
		r.Use(otelchi.Middleware("agentshield",
			otelchi.WithTracerProvider(s.tracerProvider),
		))
	}

	// Add Chi middleware chain
	s.rateLimiter = newIPRateLimiter(rate.Every(600*time.Millisecond), 10)
	r.Use(middleware.Recoverer)                    // Panic recovery
	r.Use(middleware.RequestID)                    // Request ID generation
	r.Use(securityHeaders)                         // Security response headers
	r.Use(rateLimitMiddleware(s.rateLimiter))       // Per-IP rate limit: ~100 req/min, burst 10
	r.Use(s.requestLogger)                         // Custom request logging
	r.Use(middleware.Timeout(30 * time.Second))    // Request timeout

	// Apply auth middleware if configured, but skip health endpoints
	if s.auth != nil {
		r.Use(s.auth.ChiMiddleware)
	} else {
		slog.Warn("No auth token configured - server will accept all requests!")
	}

	// API v1 routes
	r.Route("/api/v1", func(r chi.Router) {
		r.Post("/evaluate", s.handleEvaluate)
		r.Post("/hooks/claude", s.handleClaudeHook)
		r.Get("/health", s.handleHealth)
		r.Get("/alerts", s.handleAlerts)
		r.Post("/audit", s.handleAuditEvent)
		r.Post("/lifecycle", s.handleLifecycleEvent)
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
	if s.rateLimiter != nil {
		s.rateLimiter.stop()
	}
	return s.httpServer.Shutdown(ctx)
}

// normalizePluginRequest normalises plugin-format aliases and populates the
// flat fields map that the rule engine expects. This handles requests coming
// from the OpenClaw plugin (tool_name, command, params) as well as the
// canonical format (tool, args, fields).
func normalizePluginRequest(req *models.EvaluationRequest, r *http.Request, cfg *config.Config) {
	// Normalise plugin compat aliases before field mapping
	if req.Tool == "" && req.ToolName != "" {
		req.Tool = req.ToolName
	}
	if req.Args == nil && req.RawParams != nil {
		req.Args = make(map[string]string, len(req.RawParams))
		for k, v := range req.RawParams {
			switch val := v.(type) {
			case string:
				req.Args[k] = val
			case float64:
				if val == float64(int64(val)) {
					req.Args[k] = fmt.Sprintf("%d", int64(val))
				} else {
					req.Args[k] = fmt.Sprintf("%g", val)
				}
			case bool:
				req.Args[k] = fmt.Sprintf("%t", val)
			default:
				// For complex types, marshal to JSON string
				if b, err := json.Marshal(v); err == nil {
					req.Args[k] = string(b)
				}
			}
		}
	}
	if req.Command != "" {
		if req.Args == nil {
			req.Args = make(map[string]string)
		}
		if _, ok := req.Args["command"]; !ok {
			req.Args["command"] = req.Command
		}
	}

	// SECURITY: only allow test context from trusted headers; never from request body/args.
	req.Context = resolveExecutionContext(r, cfg)

	// Build flat fields map for rule engine
	if req.Fields == nil {
		req.Fields = make(map[string]string)
	}
	if req.Tool != "" {
		if _, ok := req.Fields["tool"]; !ok {
			req.Fields["tool"] = req.Tool
		}
	}
	if _, ok := req.Fields["event_type"]; !ok {
		req.Fields["event_type"] = "tool_call"
	}
	if req.Context != "" {
		if _, ok := req.Fields["context"]; !ok {
			req.Fields["context"] = req.Context
		}
	}
	if req.Source != "" {
		if _, ok := req.Fields["source"]; !ok {
			req.Fields["source"] = req.Source
		}
	}
	if _, ok := req.Fields["command"]; !ok {
		if cmd, ok := req.Args["command"]; ok {
			req.Fields["command"] = cmd
		}
	}
	for k, v := range req.Args {
		if _, exists := req.Fields[k]; !exists {
			req.Fields[k] = v
		}
	}
}

func resolveExecutionContext(r *http.Request, cfg *config.Config) string {
	if r == nil || cfg == nil {
		return "prod"
	}
	if !cfg.TestContext.Enabled {
		return "prod"
	}

	requested := strings.ToLower(strings.TrimSpace(r.Header.Get("X-AgentShield-Context")))
	if requested != "test" {
		return "prod"
	}
	token := strings.TrimSpace(r.Header.Get("X-AgentShield-Context-Token"))
	if token == "" || subtle.ConstantTimeCompare([]byte(token), []byte(cfg.TestContext.Token)) != 1 {
		slog.Warn("Rejected untrusted test context request", "remote_addr", r.RemoteAddr)
		return "prod"
	}

	return "test"
}

// storeMatchedAlerts persists matched alerts to the database. Storage failures
// are logged but do not cause the request to fail.
func (s *Server) storeMatchedAlerts(response *evaluate.EvaluationResponse, req *models.EvaluationRequest) {
	for _, alert := range response.Alerts {
		if !alert.Matched {
			continue
		}
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
		}
	}
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

	// SECURITY: Normalize first so that plugin-compat aliases (params, tool_name,
	// command) are merged into the canonical fields before validation runs.
	// This prevents RawParams from bypassing field-level validation.
	normalizePluginRequest(&req, r, s.config)

	// SECURITY: Validate all input fields (after normalization)
	if err := validateEvaluationRequest(&req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid input: %v", err), http.StatusBadRequest)
		slog.Warn("Input validation failed", "error", err, "event_id", req.EventID, "remote_addr", r.RemoteAddr)
		return
	}

	// Evaluate the request
	response, err := s.evaluator.EvaluateWithContext(r.Context(), &req)
	if err != nil {
		slog.Error("Evaluation failed", "event_id", req.EventID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	// Store alerts in database (non-fatal on failure)
	if len(response.Alerts) > 0 {
		s.storeMatchedAlerts(response, &req)
	}

	// Return response
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Warn("Failed to write response", "error", err)
	}
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

	// SECURITY: Minimal information disclosure in unauthenticated health endpoint.
	// Cache stats and detailed config are not exposed — use an authenticated
	// status endpoint instead.
	response := HealthResponse{
		Status:        status,
		Version:       "1.0.0",
		UptimeSeconds: time.Since(s.startTime).Seconds(),
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Warn("Failed to write response", "error", err)
	}
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
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Warn("Failed to write response", "error", err)
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
	if strings.TrimSpace(req.EventID) == "" {
		http.Error(w, "event_id is required", http.StatusBadRequest)
		return
	}
	if err := validateStringInput(req.EventID, 256, "event_id"); err != nil {
		http.Error(w, fmt.Sprintf("Invalid event_id: %v", err), http.StatusBadRequest)
		return
	}

	normalizedType, ok := feedback.NormalizeFeedbackType(req.FeedbackType)
	if !ok {
		http.Error(w, "Invalid feedback_type (must be false_positive, true_positive, false_negative, or improvement)", http.StatusBadRequest)
		return
	}
	req.FeedbackType = normalizedType

	if req.Comment != "" {
		if err := validateStringInput(req.Comment, feedback.MaxCommentLength, "comment"); err != nil {
			http.Error(w, fmt.Sprintf("Invalid comment: %v", err), http.StatusBadRequest)
			return
		}
	}
	if req.AlertID != nil && *req.AlertID <= 0 {
		http.Error(w, "alert_id must be a positive integer", http.StatusBadRequest)
		return
	}

	// Resolve rule name for event-level feedback (without explicit alert_id).
	// When alert_id is present, store.InsertFeedback resolves rule_name by ID.
	var ruleName string
	if req.AlertID == nil {
		alertQuery := &store.AlertQuery{
			EventID: req.EventID,
			Limit:   1,
		}
		alerts, err := s.store.QueryAlerts(alertQuery)
		if err == nil && len(alerts) > 0 {
			ruleName = alerts[0].RuleName
		}
	}

	alertIDStr := ""
	if req.AlertID != nil {
		alertIDStr = strconv.FormatInt(*req.AlertID, 10)
	}

	// Create feedback object
	fb := &feedback.Feedback{
		EventID:   req.EventID,
		AlertID:   alertIDStr,
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
	if err := json.NewEncoder(w).Encode(map[string]string{
		"status":  "received",
		"message": "Thank you for your feedback",
	}); err != nil {
		slog.Warn("Failed to write response", "error", err)
	}
}

// handleAuditEvent accepts post-execution audit payloads from plugins.
func (s *Server) handleAuditEvent(w http.ResponseWriter, r *http.Request) {
	s.handleAcceptedEvent(w, r, "audit")
}

// handleLifecycleEvent accepts lifecycle telemetry payloads from plugins.
func (s *Server) handleLifecycleEvent(w http.ResponseWriter, r *http.Request) {
	s.handleAcceptedEvent(w, r, "lifecycle")
}

// handleAcceptedEvent validates JSON shape and returns 202 Accepted.
func (s *Server) handleAcceptedEvent(w http.ResponseWriter, r *http.Request, kind string) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)
	defer r.Body.Close()

	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid request format", http.StatusBadRequest)
		slog.Warn("Event JSON decode error", "kind", kind, "error", err, "remote_addr", r.RemoteAddr)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(map[string]string{
		"status":  "accepted",
		"message": kind + " event received",
	}); err != nil {
		slog.Warn("Failed to write response", "error", err)
	}
}

// handleFeedbackQuery handles GET /api/v1/feedback?rule=<name>
func (s *Server) handleFeedbackQuery(w http.ResponseWriter, r *http.Request) {
	ruleName := r.URL.Query().Get("rule")
	if ruleName == "" {
		http.Error(w, "rule parameter is required", http.StatusBadRequest)
		return
	}
	if err := validateStringInput(ruleName, 200, "rule"); err != nil {
		http.Error(w, fmt.Sprintf("Invalid rule parameter: %v", err), http.StatusBadRequest)
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
	if err := json.NewEncoder(w).Encode(response); err != nil {
		slog.Warn("Failed to write response", "error", err)
	}
}
