package llm

// IsBuiltinProvider reports whether name is a provider prefix Weft routes
// itself (so a custom Provider or runtime with that name would be shadowed).
func IsBuiltinProvider(name string) bool {
	switch name {
	case "mock", "anthropic":
		return true
	}
	_, ok := openAICompatible[name]
	return ok
}
