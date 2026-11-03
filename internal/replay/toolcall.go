package replay

// EventFromToolCall builds an ExtractedEvent from a recorded tool call (a
// tool name and its argument object), the same way the dataset adapters do:
// arguments are flattened to strings, and file_path / url are taken from the
// usual argument names. Weft's replay uses it with BuildProductionRequest so
// a recorded call is re-evaluated exactly as a shipped plugin would send it.
func EventFromToolCall(sessionID, toolName string, input map[string]interface{}) ExtractedEvent {
	ev := ExtractedEvent{
		SessionID: sessionID,
		Kind:      EventKindCall,
		ToolName:  toolName,
		Args:      flattenInput(input),
	}
	if p := firstOf(ev.Args, "path", "file_path", "filePath"); p != "" {
		ev.FilePath = p
	}
	if u := firstOf(ev.Args, "url", "uri"); u != "" {
		ev.URL = u
	}
	return ev
}
