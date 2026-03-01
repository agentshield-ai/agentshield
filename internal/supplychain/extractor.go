package supplychain

import (
	"encoding/json"
	"regexp"
	"strings"
)

// Registry identifies a package registry.
type Registry string

const (
	RegistryNPM  Registry = "npm"
	RegistryPyPI Registry = "pypi"
)

// PackageRef represents a package name and its registry.
type PackageRef struct {
	Name     string   `json:"name"`
	Registry Registry `json:"registry"`
}

// npmInstallRe matches "npm install <packages>" or "npm i <packages>".
var npmInstallRe = regexp.MustCompile(`(?:npm|npx)\s+(?:install|i|add)\s+(.+)`)

// pipInstallRe matches "pip install <packages>" or "pip3 install <packages>".
var pipInstallRe = regexp.MustCompile(`(?:pip3?|python3?\s+-m\s+pip)\s+install\s+(.+)`)

// npmFlags are flags we skip when parsing npm install arguments.
var npmFlags = map[string]bool{
	"--save": true, "--save-dev": true, "--save-exact": true,
	"--save-optional": true, "--save-peer": true,
	"-D": true, "-E": true, "-O": true, "-P": true,
	"--global": true, "-g": true,
	"--legacy-peer-deps": true, "--force": true,
}

// pipFlags are flags we skip when parsing pip install arguments.
var pipFlags = map[string]bool{
	"--upgrade": true, "-U": true,
	"--user": true, "--force-reinstall": true,
	"--no-deps": true, "--pre": true,
	"--quiet": true, "-q": true,
	"--verbose": true, "-v": true,
}

// ExtractPackages extracts package references from a tool call's arguments.
// It handles npm install, pip install, and file-write operations that produce
// package.json or requirements.txt files.
func ExtractPackages(toolName string, args map[string]interface{}) []PackageRef {
	var refs []PackageRef

	// Try extracting from command argument (Bash/shell tool calls)
	if cmd, ok := stringFromArgs(args, "command"); ok {
		refs = append(refs, extractFromCommand(cmd)...)
	}

	// Try extracting from content written to package files
	if content, ok := stringFromArgs(args, "content"); ok {
		if path, ok := stringFromArgs(args, "file_path"); ok {
			refs = append(refs, extractFromFileWrite(path, content)...)
		}
	}

	// Deduplicate
	return dedup(refs)
}

// extractFromCommand parses shell commands for package install invocations.
func extractFromCommand(cmd string) []PackageRef {
	var refs []PackageRef

	// npm install
	if matches := npmInstallRe.FindStringSubmatch(cmd); len(matches) > 1 {
		refs = append(refs, parseNPMArgs(matches[1])...)
	}

	// pip install
	if matches := pipInstallRe.FindStringSubmatch(cmd); len(matches) > 1 {
		refs = append(refs, parsePipArgs(matches[1])...)
	}

	return refs
}

// parseNPMArgs parses the arguments portion of an npm install command.
func parseNPMArgs(argsStr string) []PackageRef {
	var refs []PackageRef
	tokens := strings.Fields(argsStr)
	for _, tok := range tokens {
		if strings.HasPrefix(tok, "-") || npmFlags[tok] {
			continue
		}
		// Strip version specifier: "express@4.18.0" -> "express"
		name := tok
		if atIdx := strings.Index(name, "@"); atIdx > 0 {
			name = name[:atIdx]
		}
		// Skip scoped packages' version part but keep scope: "@types/node" is fine
		if name == "" {
			continue
		}
		refs = append(refs, PackageRef{Name: name, Registry: RegistryNPM})
	}
	return refs
}

// parsePipArgs parses the arguments portion of a pip install command.
func parsePipArgs(argsStr string) []PackageRef {
	var refs []PackageRef
	tokens := strings.Fields(argsStr)
	skipNext := false
	for _, tok := range tokens {
		if skipNext {
			skipNext = false
			continue
		}
		if strings.HasPrefix(tok, "-") {
			if pipFlags[tok] {
				continue
			}
			// Flags like -r (requirements file) consume next token
			if tok == "-r" || tok == "--requirement" || tok == "--index-url" || tok == "-i" || tok == "--extra-index-url" {
				skipNext = true
				continue
			}
			continue
		}
		// Strip version specifier: "requests>=2.28" -> "requests"
		name := stripPipVersion(tok)
		if name == "" {
			continue
		}
		refs = append(refs, PackageRef{Name: name, Registry: RegistryPyPI})
	}
	return refs
}

// stripPipVersion removes version specifiers from pip package names.
// "requests>=2.28" -> "requests", "flask==2.3.0" -> "flask"
func stripPipVersion(pkg string) string {
	for _, sep := range []string{">=", "<=", "!=", "==", "~=", ">", "<", "["} {
		if idx := strings.Index(pkg, sep); idx > 0 {
			return pkg[:idx]
		}
	}
	return pkg
}

// extractFromFileWrite checks if a write operation targets a package manifest
// file and extracts package names from its content.
func extractFromFileWrite(path, content string) []PackageRef {
	lower := strings.ToLower(path)
	if strings.HasSuffix(lower, "package.json") {
		return extractFromPackageJSON(content)
	}
	if strings.HasSuffix(lower, "requirements.txt") {
		return extractFromRequirementsTxt(content)
	}
	return nil
}

// extractFromPackageJSON parses a package.json for dependency names.
func extractFromPackageJSON(content string) []PackageRef {
	var pkg struct {
		Dependencies    map[string]string `json:"dependencies"`
		DevDependencies map[string]string `json:"devDependencies"`
	}
	if err := json.Unmarshal([]byte(content), &pkg); err != nil {
		return nil
	}

	var refs []PackageRef
	for name := range pkg.Dependencies {
		refs = append(refs, PackageRef{Name: name, Registry: RegistryNPM})
	}
	for name := range pkg.DevDependencies {
		refs = append(refs, PackageRef{Name: name, Registry: RegistryNPM})
	}
	return refs
}

// extractFromRequirementsTxt parses a requirements.txt for package names.
func extractFromRequirementsTxt(content string) []PackageRef {
	var refs []PackageRef
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "-") {
			continue
		}
		name := stripPipVersion(line)
		if name != "" {
			refs = append(refs, PackageRef{Name: name, Registry: RegistryPyPI})
		}
	}
	return refs
}

// stringFromArgs safely extracts a string value from an args map.
func stringFromArgs(args map[string]interface{}, key string) (string, bool) {
	if args == nil {
		return "", false
	}
	v, ok := args[key]
	if !ok {
		return "", false
	}
	s, ok := v.(string)
	return s, ok && s != ""
}

// dedup removes duplicate PackageRef entries.
func dedup(refs []PackageRef) []PackageRef {
	if len(refs) == 0 {
		return nil
	}
	seen := make(map[string]bool, len(refs))
	out := make([]PackageRef, 0, len(refs))
	for _, r := range refs {
		key := string(r.Registry) + ":" + r.Name
		if !seen[key] {
			seen[key] = true
			out = append(out, r)
		}
	}
	return out
}
