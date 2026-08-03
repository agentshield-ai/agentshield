// Package rulelint validates AgentShield Sigma rules against the ground truth
// of what the detection pipeline can actually observe.
//
// A rule can be perfectly valid Sigma yet never fire, because it matches on an
// event_type no producer emits or a field no code populates. Those failures are
// silent: the rule loads, reports no error, and simply never alerts. This package
// makes that class of defect a build-time error.
//
// It checks four invariants:
//
//  1. Every event_type a rule matches on is emitted by some producer (plugin,
//     server, or replay), OR is explicitly listed as a known, documented gap
//     pending a producer (see KnownUnemittedEventTypes).
//  2. Every session.* field a rule matches on is injected by the session
//     registry, OR is a documented gap (see KnownUnpopulatedSessionFields).
//  3. Every ordinary field a rule matches on can be populated by a shipped
//     producer (see ProducibleFields), OR is a documented gap (see
//     KnownUnpopulatedFields).
//  4. Every MITRE ATT&CK tag is well-formed and not on the confirmed-invalid list.
//
// The ground-truth sets below are the single source of truth. When a producer
// starts emitting a new event_type (Phase 2), move it from KnownUnemitted* into
// the Emitted*/Injected* set; the tests enforce that the gap lists stay in sync
// with what the rules actually reference, so stale entries are caught too.
package rulelint

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	sigma "github.com/agentshield-ai/agentshield/pkg/sigma"
)

// EmittedEventTypes is the set of event_type values that some shipping producer
// actually emits. Provenance:
//   - plugins + server (internal/server/server.go defaultEventTypeForTool,
//     plugins/*/normalise.*, plugins/*/event-builder.*):
//     tool_call, file_read, file_write, file_edit, session_spawn, session_start,
//     tool_result
//   - replay (internal/replay/fieldmap.go):
//     content_retrieval, browser_action, message_send, agent_creation
var EmittedEventTypes = map[string]string{
	"tool_call":         "plugins/server",
	"file_read":         "plugins/server",
	"file_write":        "plugins/server",
	"file_edit":         "plugins/server",
	"session_spawn":     "plugins/server",
	"session_start":     "plugins",
	"tool_result":       "plugins (codex/gemini/claude/openclaw)",
	"tool_response":     "server: handleAuditEvent canonicalizes tool_result->tool_response for detection-only scanning of tool output",
	"user_input":        "plugins: Claude hook UserPromptSubmit posts the prompt as event_type user_input with params.message",
	"content_retrieval": "replay",
	"browser_action":    "replay",
	"message_send":      "replay",
	"agent_creation":    "replay",
}

// KnownUnemittedEventTypes are event_type values that rules match on but that NO
// producer currently emits. Each is a documented detection gap: the rule (or the
// relevant selection) cannot fire until a producer is added (Phase 2) or the rule
// is rewritten onto an emitted type. Keeping them here — rather than silently
// tolerating them — means a NEW rule cannot introduce a fresh phantom type
// without a reviewer consciously adding it to this list.
//
// The value is the rule/threat area affected, for traceability.
var KnownUnemittedEventTypes = map[string]string{
	"document_retrieval":      "context_poisoning (real emitted type is content_retrieval)",
	"tool_sequence":           "context_poisoning, lateral_movement, openclaw_prompt_injection (needs temporal-correlation producer)",
	"tool_description":        "mcp_tool_poisoning, mcp_rug_pull",
	"tool_description_update": "mcp_rug_pull",
	"tool_list_comparison":    "mcp_rug_pull",
	"tool_execution":          "mcp_rug_pull",
	"network_request":         "cloud_metadata_access, dns_tunneling/exfil_via_dns, api_endpoint_redirection",
	"dns_query":               "exfil_via_dns",
	"output_generation":       "prompt_injection_exfil",
	"llm_response":            "rag_image_exfiltration",
	"code_execution":          "steganographic_exfil",
	"agent_message":           "inter_agent_spoofing",
	"agent_handoff":           "inter_agent_spoofing",
	"config_load":             "api_endpoint_redirection",
	"config_modification":     "openclaw_mcp_manipulation",
	"env_set":                 "api_endpoint_redirection",
	"outbound_request":        "api_endpoint_redirection",
	"plugin_installation":     "openclaw_mcp_manipulation",
	"plugin_configuration":    "openclaw_mcp_manipulation",
	"skill_installation":      "openclaw_mcp_manipulation",
	"parallel_operations":     "lateral_movement",
	"authentication":          "lateral_movement",
	"model_call":              "recursive_loop_exhaustion",
}

// InjectedSessionFields is the closed set of session.* fields the session
// registry injects into the evaluation context
// (internal/session/registry.go:265-337). Any session.* field outside this set
// evaluates to false forever.
var InjectedSessionFields = map[string]bool{
	"session.tool_count":                 true,
	"session.recent_tools":               true,
	"session.unique_tool_count":          true,
	"session.alert_count":                true,
	"session.approval_count":             true,
	"session.override_count":             true,
	"session.cross_session_alert_count":  true,
	"session.cross_session_count":        true,
	"session.cross_session_tool_overlap": true,
}

// KnownUnpopulatedSessionFields are session.* fields rules reference that the
// registry does not inject. Documented gaps pending a producer (Phase 2).
var KnownUnpopulatedSessionFields = map[string]string{
	"session.step_count":                "recursive_loop_exhaustion",
	"session.self_prompt_depth":         "recursive_loop_exhaustion",
	"session.identical_tool_call_count": "recursive_loop_exhaustion",
	"session.goal_state_changed":        "recursive_loop_exhaustion",
	"session.cumulative_tokens":         "recursive_loop_exhaustion",
	"session.phase":                     "settings_hook_injection",
	"session.project_first_open":        "settings_hook_injection",
}

// ProducibleFields is the closed set of ordinary (non-event_type, non-session.*)
// field names a shipped producer can cause the rule engine to see. Two paths
// reach the engine and both are enumerated here.
//
// Pre-execution: a plugin posts an evaluation request and
// server.normalizePluginRequest derives tool, event_type, context, source,
// command and file_path, then copies every call parameter into fields verbatim
// under its own key.
//
// Post-execution: a plugin posts a tool result to /api/v1/audit and
// toolresult.DetectionFields maps it to event_type tool_response plus response,
// content, source and tool.
//
// The verbatim parameter copy means a field is producible whenever some shipped
// tool sends a parameter of that name. Those entries name the tool, so the claim
// can be checked rather than assumed; a field reachable only in principle, with
// no shipped caller, belongs in KnownUnpopulatedFields instead.
var ProducibleFields = map[string]string{
	"tool":       "server: normalizePluginRequest, from tool or the tool_name alias",
	"event_type": "server: normalizePluginRequest; audit: toolresult.DetectionFields",
	"context":    "server: normalizePluginRequest, from resolveExecutionContext",
	"source":     "server: normalizePluginRequest; audit: toolresult.DetectionFields",
	"command":    "server: normalizePluginRequest, from the command arg",
	"file_path":  "server: normalizePluginRequest, from file_path/filePath/path",
	"response":   "audit: toolresult.DetectionFields, from result_summary",
	"content":    "audit: toolresult.DetectionFields, from result_summary",
	"url":        "verbatim param copy: the openclaw browser tool sends params.url",
	"action":     "verbatim param copy: the openclaw browser tool sends params.action",
	"message":    "verbatim param copy: the Claude hook posts UserPromptSubmit as params.message",
}

// KnownUnpopulatedFields are ordinary field names rules match on that no shipped
// producer populates. Each is a documented detection gap: the selection cannot
// fire until a producer supplies the field or the rule is rewritten onto one
// that exists. Keeping them listed — rather than silently tolerating any
// unrecognised field — means a NEW rule cannot introduce a fresh phantom field
// without a reviewer consciously adding it here.
//
// Three kinds of entry appear below, and they are worth distinguishing when
// deciding what to fix:
//
//   - Fields with no producer that a producer could plausibly supply
//     (description, arguments, response_text, tool_name). These are the subject
//     of the measured production-reproducibility gap; see docs/replay-scoring.md.
//   - Custom engine constructs the evaluator does not implement at all
//     (time_window, time_between, tools_used, suspicious_pattern and the
//     analysis scores). These need engine features, not a producer.
//   - Structured sub-fields of an event shape the pipeline never builds
//     (message.*, handoff.*, request.headers, destination.host).
//
// The value is the rule area affected, for traceability.
var KnownUnpopulatedFields = map[string]string{
	// No producer today; a producer is conceivable. Tracked as the
	// production-reproducibility gap.
	"arguments":     "export_dir_credential_redirect, memory_poisoning, tool_arg_approval_ui_xss, tool_registry_tampering (no producer sends a JSON blob of call args; params arrive individually)",
	"description":   "mcp_tool_poisoning (no producer carries a tool's declared description)",
	"response_text": "rag_image_exfiltration (the audit path supplies response and content, not response_text)",
	"tool_name":     "mcp_rug_pull (the server sets tool, not tool_name, from the tool_name request alias)",

	// Custom constructs requiring engine features that do not exist.
	"time_window":                         "context_poisoning, lateral_movement, openclaw_prompt_injection (temporal correlation engine)",
	"time_between":                        "openclaw_prompt_injection (temporal correlation engine)",
	"tools_used":                          "context_poisoning, lateral_movement, openclaw_prompt_injection (temporal correlation engine)",
	"tools_sequence":                      "lateral_movement (temporal correlation engine)",
	"previous_tool":                       "openclaw_prompt_injection (temporal correlation engine)",
	"next_tool":                           "openclaw_prompt_injection (temporal correlation engine)",
	"suspicious_pattern":                  "openclaw_prompt_injection (behavioural analysis engine)",
	"suspicious_data_pattern":             "context_poisoning (behavioural analysis engine)",
	"cross_plugin_data_flow":              "context_poisoning (behavioural analysis engine)",
	"visibility_analysis":                 "rules_file_backdoor (content analysis engine)",
	"byte_size_to_visible_char_ratio":     "rules_file_backdoor (content analysis engine)",
	"size_increase_ratio":                 "steganographic_exfil (content analysis engine)",
	"description_hash_changed":            "mcp_rug_pull (tool-registry diffing engine)",
	"description_similarity_score":        "mcp_rug_pull (tool-registry diffing engine)",
	"description_length_ratio":            "mcp_rug_pull (tool-registry diffing engine)",
	"previous_description_exists":         "mcp_rug_pull (tool-registry diffing engine)",
	"new_description":                     "mcp_rug_pull (tool-registry diffing engine)",
	"actual_behavior_matches_description": "mcp_rug_pull (tool-registry diffing engine)",
	"query_length":                        "exfil_via_dns (network analysis engine)",
	"subdomain_count":                     "exfil_via_dns (network analysis engine)",
	"domain_entropy":                      "exfil_via_dns (network analysis engine)",
	"hosts_count":                         "lateral_movement (network analysis engine)",
	"destination_discovered_recently":     "lateral_movement (network analysis engine)",
	"signature_verification":              "openclaw_mcp_manipulation (supply-chain verification engine)",

	// Structured sub-fields of event shapes the pipeline never constructs.
	"message.claimed_role":           "inter_agent_spoofing (no agent-message producer)",
	"message.claimed_sender_id":      "inter_agent_spoofing (no agent-message producer)",
	"message.verified_sender_id":     "inter_agent_spoofing (no agent-message producer)",
	"message.peer_identity_verified": "inter_agent_spoofing (no agent-message producer)",
	"message.identity_match":         "inter_agent_spoofing (no agent-message producer)",
	"message.directive":              "inter_agent_spoofing (no agent-message producer)",
	"handoff.signature_valid":        "inter_agent_spoofing (no agent-handoff producer)",
	"request.headers":                "api_endpoint_redirection (no network-request producer)",
	"destination.host":               "api_endpoint_redirection (no network-request producer)",

	// Plain names with no producer: no shipped tool sends a param of this name,
	// and the server derives nothing of the kind.
	"code":                  "steganographic_exfil (no code_execution producer)",
	"file_extension":        "steganographic_exfil",
	"query":                 "exfil_via_dns (no dns_query producer)",
	"source_url":            "openclaw_mcp_manipulation",
	"permissions":           "openclaw_mcp_manipulation",
	"configuration_section": "openclaw_mcp_manipulation",
	"tool_input":            "mcp_command_injection (the server flattens params; it does not preserve a tool_input blob)",
	"tool_source":           "mcp_command_injection",
	"credential_source":     "lateral_movement",
	"operation_type":        "lateral_movement",
	"parent_agent_context":  "lateral_movement",
	"sensitive_files":       "lateral_movement",
	"target_host":           "lateral_movement",
	"target":                "lateral_movement",
	"location":              "lateral_movement",
	"pattern":               "lateral_movement",
}

// ConfirmedInvalidMitreIDs are ATT&CK technique IDs found in rules that are not
// valid Enterprise ATT&CK techniques. These are tracked debt, not live build
// failures: they pre-date this linter, so listing them here keeps CI green while
// documenting the fix. A NEW malformed tag (bad format) still hard-fails. Extend
// as more are confirmed; a full-catalog check could replace this list later.
var ConfirmedInvalidMitreIDs = map[string]string{
	// Empty: the previously-tracked invalid ids (t1684.001, t1685) were fixed —
	// t1684.001 -> t1656 (Impersonation); t1685 removed (no valid mapping, t1566
	// already covers the vector). New confirmed-invalid ids may be added here.
}

var mitreTechniqueRE = regexp.MustCompile(`^t\d{4}(\.\d{3})?$`)

// ValidMitreTactics is the closed set of 14 Enterprise ATT&CK tactic slugs used
// in `attack.<tactic>` tags (https://attack.mitre.org/tactics/enterprise/).
var ValidMitreTactics = map[string]bool{
	"reconnaissance":       true,
	"resource-development": true,
	"initial-access":       true,
	"execution":            true,
	"persistence":          true,
	"privilege-escalation": true,
	"defense-evasion":      true,
	"credential-access":    true,
	"discovery":            true,
	"lateral-movement":     true,
	"collection":           true,
	"command-and-control":  true,
	"exfiltration":         true,
	"impact":               true,
}

// NonStandardTactics are tactic slugs the corpus uses that are not standard
// ATT&CK tactics. Tracked debt (like ConfirmedInvalidMitreIDs): documented so CI
// stays green while the divergence is visible. `defense-impairment` should likely
// be `defense-evasion`; `stealth` has no ATT&CK equivalent.
var NonStandardTactics = map[string]string{
	"stealth":            "not an ATT&CK tactic; used widely as a custom tag",
	"defense-impairment": "not an ATT&CK tactic; standard slug is defense-evasion",
}

// Finding is a single lint violation.
type Finding struct {
	File    string
	Rule    string
	Kind    string // "event_type", "session_field", "field", "mitre"
	Message string
}

func (f Finding) String() string {
	return fmt.Sprintf("%s [%s] %s: %s", f.File, f.Rule, f.Kind, f.Message)
}

// Result carries findings plus the sets actually referenced by the corpus, so
// tests can detect stale gap-list entries (entries no rule uses any more).
type Result struct {
	Findings                []Finding
	ReferencedEventTypes    map[string]bool
	ReferencedSessionFields map[string]bool
	ReferencedFields        map[string]bool
	ReferencedMitreIDs      map[string]bool
	ReferencedTactics       map[string]bool
}

// LintDir parses every .yml/.yaml rule under dir and returns all findings.
func LintDir(dir string) (*Result, error) {
	res := &Result{
		ReferencedEventTypes:    map[string]bool{},
		ReferencedSessionFields: map[string]bool{},
		ReferencedFields:        map[string]bool{},
		ReferencedMitreIDs:      map[string]bool{},
		ReferencedTactics:       map[string]bool{},
	}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if ext := filepath.Ext(path); ext != ".yml" && ext != ".yaml" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rule, err := sigma.ParseRule(content)
		if err != nil {
			// A rule that doesn't parse is the engine's problem to report, not
			// ours; skip so lint failures stay focused on semantic gaps.
			return nil
		}
		lintRule(path, rule, res)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(res.Findings, func(i, j int) bool {
		return res.Findings[i].String() < res.Findings[j].String()
	})
	return res, nil
}

func lintRule(path string, rule *sigma.Rule, res *Result) {
	base := filepath.Base(path)

	atoms := collectAtoms(rule)
	for _, a := range atoms {
		switch {
		case strings.EqualFold(a.Field, "event_type"):
			for _, v := range a.Patterns {
				v = strings.TrimSpace(v)
				if v == "" {
					continue
				}
				res.ReferencedEventTypes[v] = true
				if _, ok := EmittedEventTypes[v]; ok {
					continue
				}
				if _, ok := KnownUnemittedEventTypes[v]; ok {
					continue
				}
				res.Findings = append(res.Findings, Finding{
					File: base, Rule: rule.Title, Kind: "event_type",
					Message: fmt.Sprintf("matches event_type %q which no producer emits and is not a documented gap; "+
						"emit it from a producer or add it to KnownUnemittedEventTypes with justification", v),
				})
			}
		case strings.HasPrefix(strings.ToLower(a.Field), "session."):
			field := strings.ToLower(a.Field)
			res.ReferencedSessionFields[field] = true
			if InjectedSessionFields[field] {
				continue
			}
			if _, ok := KnownUnpopulatedSessionFields[field]; ok {
				continue
			}
			res.Findings = append(res.Findings, Finding{
				File: base, Rule: rule.Title, Kind: "session_field",
				Message: fmt.Sprintf("matches %q which the session registry does not inject; "+
					"inject it in internal/session/registry.go or add it to KnownUnpopulatedSessionFields", field),
			})

		default:
			field := strings.ToLower(strings.TrimSpace(a.Field))
			if field == "" {
				continue
			}
			res.ReferencedFields[field] = true
			if _, ok := ProducibleFields[field]; ok {
				continue
			}
			if _, ok := KnownUnpopulatedFields[field]; ok {
				continue
			}
			res.Findings = append(res.Findings, Finding{
				File: base, Rule: rule.Title, Kind: "field",
				Message: fmt.Sprintf("matches field %q which no producer populates and is not a documented gap; "+
					"populate it from a producer and add it to ProducibleFields, or add it to "+
					"KnownUnpopulatedFields with justification", field),
			})
		}
	}

	for _, tag := range rule.Tags {
		if !strings.HasPrefix(tag, "attack.") {
			continue // non-ATT&CK tags are out of scope
		}
		slug := strings.TrimPrefix(tag, "attack.")
		if len(slug) >= 2 && slug[0] == 't' && slug[1] >= '0' && slug[1] <= '9' {
			// Technique id (attack.tNNNN[.NNN]).
			res.ReferencedMitreIDs[slug] = true
			if !mitreTechniqueRE.MatchString(slug) {
				// Malformed format is an unambiguous, hard error — fail the build.
				res.Findings = append(res.Findings, Finding{
					File: base, Rule: rule.Title, Kind: "mitre",
					Message: fmt.Sprintf("tag %q is not a well-formed ATT&CK technique id (want attack.tNNNN or attack.tNNNN.NNN)", tag),
				})
			}
			// Well-formed but confirmed-invalid IDs are tracked debt (see
			// ConfirmedInvalidMitreIDs); they do not fail the build here.
			continue
		}
		// Tactic tag (attack.<tactic>).
		res.ReferencedTactics[slug] = true
		if ValidMitreTactics[slug] {
			continue
		}
		if _, ok := NonStandardTactics[slug]; ok {
			continue // documented debt
		}
		res.Findings = append(res.Findings, Finding{
			File: base, Rule: rule.Title, Kind: "mitre",
			Message: fmt.Sprintf("tag %q is not a valid ATT&CK tactic; fix the slug or add it to NonStandardTactics with justification", tag),
		})
	}
}

// collectAtoms walks a rule's detection expression tree and returns every
// SearchAtom, so callers can inspect fields and patterns.
func collectAtoms(rule *sigma.Rule) []*sigma.SearchAtom {
	if rule.Detection == nil || rule.Detection.Expr == nil {
		return nil
	}
	var out []*sigma.SearchAtom
	var walk func(e sigma.Expr)
	walk = func(e sigma.Expr) {
		switch x := e.(type) {
		case *sigma.NamedExpr:
			walk(x.X)
		case *sigma.NotExpr:
			walk(x.X)
		case *sigma.AndExpr:
			for _, c := range x.X {
				walk(c)
			}
		case *sigma.OrExpr:
			for _, c := range x.X {
				walk(c)
			}
		case *sigma.SearchAtom:
			out = append(out, x)
		}
	}
	walk(rule.Detection.Expr)
	return out
}
