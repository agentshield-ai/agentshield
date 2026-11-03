package compat

import (
	"flag"
	"os"
	"strings"
)

// envAlias names a Weft flag, its WEFT_* variable and the WF_* variable the
// other product's self-hosted server reads for the same setting.
type envAlias struct{ flag, weftEnv, alias string }

var envAliases = []envAlias{
	{"remote-scorer-allow-insecure-http", "WEFT_REMOTE_SCORER_ALLOW_INSECURE_HTTP", "WF_SCORING_WORKER_REMOTE_SCORER_ALLOW_INSECURE_HTTP"},
}

// Enabled reports whether the adapter is on: the flag when given, else
// WEFT_WEAVE_COMPAT.
func Enabled(fs *flag.FlagSet, flagValue bool) bool {
	set := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == FlagName {
			set = true
		}
	})
	if set {
		return flagValue
	}
	v := strings.ToLower(strings.TrimSpace(os.Getenv(EnvName)))
	return v == "1" || v == "true" || v == "yes" || v == "on"
}

// ApplyEnvAliases sets each flag that has a WF_* alias from that variable
// when neither the flag nor its WEFT_* variable was given. Call it after
// Parse. It returns the aliases it applied.
func ApplyEnvAliases(fs *flag.FlagSet, getenv func(string) string) []string {
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	var applied []string
	for _, a := range envAliases {
		v := strings.TrimSpace(getenv(a.alias))
		if v == "" || given[a.flag] || strings.TrimSpace(getenv(a.weftEnv)) != "" || fs.Lookup(a.flag) == nil {
			continue
		}
		if err := fs.Set(a.flag, v); err == nil {
			applied = append(applied, a.alias)
		}
	}
	return applied
}
