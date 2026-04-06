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

const sourceClaudeCode = "claude-code"

const (
	claudeDecisionDeny  = "deny"
	claudeDecisionAsk   = "ask"
	claudeDecisionAllow = "allow"
)

type claudeHookInput struct {
	SessionID     string                 `json:"session_id"`
	Cwd           string                 `json:"cwd"`
	HookEventName string                 `json:"hook_event_name"`
	ToolName      string                 `json:"tool_name"`
	ToolInput     map[string]interface{} `json:"tool_input"`
	ToolResult    interface{}            `json:"tool_result"`
	Prompt        string                 `json:"prompt"`
}

type claudeHookResponse struct {
	HookSpecificOutput *claudeHookOutput `json:"hookSpecificOutput,omitempty"`
}

type claudeHookOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

// emptyJSON is a pre-allocated response for hook events that require no action.
var emptyJSON = struct{}{}

func (s *Server) handleClaudeHook(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)

	var input claudeHookInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
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
		writeJSON(w, http.StatusOK, emptyJSON)
	}
}

// evaluateClaudeHook runs the shared normalize → validate → evaluate → store
// pipeline for a Claude Code hook request. Returns the response or writes an
// HTTP error and returns nil.
func (s *Server) evaluateClaudeHook(w http.ResponseWriter, r *http.Request, req *models.EvaluationRequest) *evaluate.EvaluationResponse {
	normalizePluginRequest(req, r, s.config)

	if err := validateEvaluationRequest(req); err != nil {
		http.Error(w, fmt.Sprintf("Invalid input: %v", err), http.StatusBadRequest)
		slog.Warn("Claude hook validation failed", "error", err, "event_id", req.EventID, "remote_addr", r.RemoteAddr)
		return nil
	}

	response, err := s.evaluator.EvaluateWithContext(r.Context(), req)
	if err != nil {
		slog.Error("Claude hook evaluation failed", "event_id", req.EventID, "error", err)
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		return nil
	}

	if len(response.Alerts) > 0 {
		s.storeMatchedAlerts(response, req)
	}

	return response
}

func (s *Server) handleClaudePreToolUse(w http.ResponseWriter, r *http.Request, input *claudeHookInput) {
	req := claudeHookToEvalRequest(input)

	response := s.evaluateClaudeHook(w, r, req)
	if response == nil {
		return
	}

	decision, reason := actionToClaudeDecision(response)
	writeJSON(w, http.StatusOK, claudeHookResponse{
		HookSpecificOutput: &claudeHookOutput{
			HookEventName:            "PreToolUse",
			PermissionDecision:       decision,
			PermissionDecisionReason: reason,
		},
	})
}

func (s *Server) handleClaudePostToolUse(w http.ResponseWriter, r *http.Request, input *claudeHookInput) {
	slog.Debug("Claude PostToolUse audit",
		"session_id", input.SessionID,
		"tool", input.ToolName,
		"cwd", input.Cwd,
	)
	writeJSON(w, http.StatusOK, emptyJSON)
}

func (s *Server) handleClaudeUserPromptSubmit(w http.ResponseWriter, r *http.Request, input *claudeHookInput) {
	req := &models.EvaluationRequest{
		EventID:   uuid.New().String(),
		SessionID: input.SessionID,
		Tool:      "UserPrompt",
		Source:    sourceClaudeCode,
		Args: map[string]string{
			"command": input.Prompt,
			"content": input.Prompt,
		},
		Fields: map[string]string{
			// event_type must be pre-set here so normalizePluginRequest doesn't
			// overwrite it with "tool_call".
			"event_type":  "user_prompt",
			"working_dir": input.Cwd,
		},
	}

	response := s.evaluateClaudeHook(w, r, req)
	if response == nil {
		return
	}

	decision, reason := actionToClaudeDecision(response)
	if decision == claudeDecisionAllow {
		writeJSON(w, http.StatusOK, emptyJSON)
	} else {
		writeJSON(w, http.StatusOK, claudeHookResponse{
			HookSpecificOutput: &claudeHookOutput{
				HookEventName:            "UserPromptSubmit",
				PermissionDecision:       decision,
				PermissionDecisionReason: reason,
			},
		})
	}
}

func claudeHookToEvalRequest(input *claudeHookInput) *models.EvaluationRequest {
	req := &models.EvaluationRequest{
		EventID:   uuid.New().String(),
		SessionID: input.SessionID,
		ToolName:  input.ToolName,
		Source:    sourceClaudeCode,
		RawParams: input.ToolInput,
	}

	// Extract top-level command so it populates fields["command"] correctly
	// after normalisation.
	if cmd, ok := input.ToolInput["command"]; ok {
		if cmdStr, ok := cmd.(string); ok {
			req.Command = cmdStr
		}
	}

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
		decision = claudeDecisionDeny
	case models.ActionRequireApproval:
		decision = claudeDecisionAsk
	default:
		return claudeDecisionAllow, ""
	}

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

func writeJSON(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		slog.Warn("Failed to write JSON response", "error", err)
	}
}
