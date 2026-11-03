package models

import (
	"fmt"
	"regexp"
	"unicode/utf8"
)

// Request limits of the engine's HTTP API.
const (
	// MaxFieldValueLength limits individual field value length.
	MaxFieldValueLength = 10000 // 10KB per field
	// MaxFieldsCount limits number of fields in a request.
	MaxFieldsCount = 100
)

// controlCharsRegex matches the control characters no input may contain
// (newline, carriage return and tab are allowed).
var controlCharsRegex = regexp.MustCompile(`[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]`)

// ValidateString validates and sanitizes string input to prevent injection attacks
func ValidateString(input string, maxLength int, fieldName string) error {
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

// ValidateEvaluationRequest validates the evaluation request inputs, after
// the HTTP API has merged its plugin-compat aliases. The engine's HTTP API
// rejects a request that fails it (400); Weft's in-process replay applies
// it too, so it evaluates exactly the requests a remote engine would.
func ValidateEvaluationRequest(req *EvaluationRequest) error {
	// Validate EventID
	if req.EventID == "" {
		return fmt.Errorf("event_id is required")
	}
	if err := ValidateString(req.EventID, MaxIDLength, "event_id"); err != nil {
		return err
	}

	// Validate SessionID
	if req.SessionID != "" {
		if err := ValidateString(req.SessionID, MaxIDLength, "session_id"); err != nil {
			return err
		}
	}

	if req.EventType != "" {
		if err := ValidateString(req.EventType, 100, "event_type"); err != nil {
			return err
		}
	}

	// Validate Tool
	if req.Tool != "" {
		if err := ValidateString(req.Tool, 100, "tool"); err != nil {
			return err
		}
	}

	// Validate correlation ids (optional; forwarded to Weft and OTel spans)
	if req.ToolCallID != "" {
		if err := ValidateString(req.ToolCallID, MaxIDLength, "tool_call_id"); err != nil {
			return err
		}
	}
	if req.AgentName != "" {
		if err := ValidateString(req.AgentName, 256, "agent_name"); err != nil {
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
		if err := ValidateString(req.ToolName, 100, "tool_name"); err != nil {
			return err
		}
	}

	// Validate Command (plugin compat alias)
	if req.Command != "" {
		if err := ValidateString(req.Command, MaxFieldValueLength, "command"); err != nil {
			return err
		}
	}

	// Validate Args map
	if req.Args != nil {
		if len(req.Args) > MaxFieldsCount {
			return fmt.Errorf("too many args fields (max %d)", MaxFieldsCount)
		}

		for key, value := range req.Args {
			if err := ValidateString(key, 100, "args key"); err != nil {
				return err
			}
			if err := ValidateString(value, MaxFieldValueLength, "args value"); err != nil {
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
			if err := ValidateString(key, 100, "fields key"); err != nil {
				return err
			}
			if err := ValidateString(value, MaxFieldValueLength, "fields value"); err != nil {
				return err
			}
		}
	}

	return nil
}
