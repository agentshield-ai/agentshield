package server

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/agentshield-ai/agentshield/internal/evaluate"
	"github.com/agentshield-ai/agentshield/internal/models"
	"github.com/google/uuid"
)

// Claude Code hook input JSON sent via HTTP hooks.
type claudeHookInput struct {
	SessionID     string                 `json:"session_id"`
	Cwd           string                 `json:"cwd"`
	HookEventName string                 `json:"hook_event_name"`
	ToolName      string                 `json:"tool_name"`
	ToolInput     map[string]interface{} `json:"tool_input"`
	ToolResult    interface{}            `json:"tool_result"`
	Prompt        string                 `json:"prompt"` // UserPromptSubmit
}

// Claude Code hook response structures.
type claudeHookResponse struct {
	HookSpecificOutput *claudeHookOutput `json:"hookSpecificOutput,omitempty"`
}

type claudeHookOutput struct {
	HookEventName          string `json:"hookEventName"`
	PermissionDecision     string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// handleClaudeHook handles Claude Code HTTP hook calls. It accepts Claude
// Code's native hook JSON format, translates it to an EvaluationRequest, runs
// it through the existing evaluation pipeline, and returns a response in the
// format Claude Code expects.
func (s *Server) handleClaudeHook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)

	var input claudeHookInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		var maxBytesErr *http.MaxBytesError
		if isMaxBytesErr(err, &maxBytesErr) {
			http.Error(w, "Request body too large", http.StatusRequestEntityTooLarge)
		} else {
			http.Error(w, "Invalid request format", http.StatusBadRequest)
		}
		slog.Warn("Claude hook JSON decode error", "error", err, "remote_addr", r.RemoteAddr)
		return
	}

	switch input.HookEventName {
	case "PreToolUse":
		s.handleClaudePreToolUse(w, r, &input)
	case "PostToolUse":
		s.handleClaudePostToolUse(w, r, &input)
	case "UserPromptSubmit":
		s.handleClaudeUserPromptSubmit(w, r, &input)
	default:
		// Unknown or unhandled hook event — return empty success so Claude Code
		// proceeds without error.
		writeJSON(w, http.StatusOK, map[string]interface{}{})
	}
}

// handleClaudePreToolUse evaluates a tool call and returns a permission decision.
func (s *Server) handleClaudePreToolUse(w http.ResponseWriter, r *http.Request, input *claudeHookInput) {
	req := claudeHookToEvalRequest(input)

	normalizePluginRequest(req, r, s.config)

	if err := validateEvaluationRequest(req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid input: %v", err), http.StatusBadRequest)
		slog.Warn("Claude hook validation failed", "error", err, "event_id", req.EventID, "remote_addr", r.RemoteAddr)
		return
	}

	response, err := s.evaluator.EvaluateWithContext(r.Context(), req)
	if err != nil {
		slog.Error("Claude hook evaluation failed", "event_id", req.EventID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if len(response.Alerts) > 0 {
		s.storeMatchedAlerts(response, req)
	}

	decision, reason := actionToClaudeDecision(response)

	resp := claudeHookResponse{
		HookSpecificOutput: &claudeHookOutput{
			HookEventName:          "PreToolUse",
			PermissionDecision:     decision,
			PermissionDecisionReason: reason,
		},
	}

	writeJSON(w, http.StatusOK, resp)
}

// handleClaudePostToolUse stores the tool result as an audit event.
func (s *Server) handleClaudePostToolUse(w http.ResponseWriter, r *http.Request, input *claudeHookInput) {
	slog.Info("Claude PostToolUse audit",
		"session_id", input.SessionID,
		"tool", input.ToolName,
		"cwd", input.Cwd,
	)

	// Return empty object — Claude Code expects 200 OK with valid JSON.
	writeJSON(w, http.StatusOK, map[string]interface{}{})
}

// handleClaudeUserPromptSubmit evaluates user prompt text for prompt injection.
func (s *Server) handleClaudeUserPromptSubmit(w http.ResponseWriter, r *http.Request, input *claudeHookInput) {
	req := &models.EvaluationRequest{
		EventID:   uuid.New().String(),
		SessionID: input.SessionID,
		Tool:      "UserPrompt",
		Source:    "claude-code",
		Args: map[string]string{
			"command": input.Prompt,
			"content": input.Prompt,
		},
		Fields: map[string]string{
			"tool":        "UserPrompt",
			"event_type":  "user_prompt",
			"source":      "claude-code",
			"command":     input.Prompt,
			"content":     input.Prompt,
			"working_dir": input.Cwd,
		},
	}

	normalizePluginRequest(req, r, s.config)

	if err := validateEvaluationRequest(req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid input: %v", err), http.StatusBadRequest)
		slog.Warn("Claude prompt validation failed", "error", err, "remote_addr", r.RemoteAddr)
		return
	}

	response, err := s.evaluator.EvaluateWithContext(r.Context(), req)
	if err != nil {
		slog.Error("Claude prompt evaluation failed", "event_id", req.EventID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return
	}

	if len(response.Alerts) > 0 {
		s.storeMatchedAlerts(response, req)
	}

	decision, reason := actionToClaudeDecision(response)

	// UserPromptSubmit does not use hookSpecificOutput — it uses exit-code
	// semantics for command hooks but for HTTP hooks we return a simple object.
	// Claude Code checks for a "decision" field at the top level.
	if decision == "allow" {
		writeJSON(w, http.StatusOK, map[string]interface{}{})
	} else {
		resp := claudeHookResponse{
			HookSpecificOutput: &claudeHookOutput{
				HookEventName:          "UserPromptSubmit",
				PermissionDecision:     decision,
				PermissionDecisionReason: reason,
			},
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// claudeHookToEvalRequest translates a Claude Code hook input into an
// EvaluationRequest suitable for the existing evaluation pipeline.
func claudeHookToEvalRequest(input *claudeHookInput) *models.EvaluationRequest {
	req := &models.EvaluationRequest{
		EventID:   uuid.New().String(),
		SessionID: input.SessionID,
		ToolName:  input.ToolName,
		Source:    "claude-code",
		RawParams: input.ToolInput,
	}

	// Extract top-level command for tools that have one, so it populates
	// fields["command"] correctly after normalisation.
	if cmd, ok := input.ToolInput["command"]; ok {
		if cmdStr, ok := cmd.(string); ok {
			req.Command = cmdStr
		}
	}

	// Inject working directory into Fields so rules can match on it.
	if input.Cwd != "" {
		req.Fields = map[string]string{
			"working_dir": input.Cwd,
		}
	}

	return req
}

// actionToClaudeDecision maps an AgentShield evaluation response to a Claude
// Code permission decision and human-readable reason.
func actionToClaudeDecision(response *evaluate.EvaluationResponse) (decision string, reason string) {
	switch response.Action {
	case models.ActionBlock:
		decision = "deny"
	case models.ActionRequireApproval:
		decision = "ask"
	default:
		return "allow", ""
	}

	// Build a human-readable reason from the first alert.
	if len(response.Alerts) > 0 {
		first := response.Alerts[0]
		reason = fmt.Sprintf("AgentShield: %s (%s)", first.RuleName, first.Severity)
		if len(response.Alerts) > 1 {
			reason += fmt.Sprintf(" (+%d more)", len(response.Alerts)-1)
		}
	} else {
		reason = "AgentShield: policy violation"
	}

	return decision, reason
}

// isMaxBytesErr is a helper that wraps errors.As for http.MaxBytesError.
func isMaxBytesErr(err error, target **http.MaxBytesError) bool {
	return errors.As(err, target)
}

// writeJSON encodes v as JSON and writes it to w with the given status code.
func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("Failed to write JSON response", "error", err)
	}
}
