package config

import (
	"cmp"
	_ "embed"
	"fmt"
	"log/slog"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"

	"github.com/docker/docker-agent/pkg/environment"
	"github.com/docker/docker-agent/pkg/reference"
	"github.com/docker/docker-agent/pkg/userconfig"
)

//go:embed builtin-agents/default.yaml
var defaultAgent []byte

//go:embed builtin-agents/coder.yaml
var coderAgent []byte

// builtinAgents maps built-in agent names to their embedded YAML configurations.
var builtinAgents = map[string][]byte{
	"default": defaultAgent,
	"coder":   coderAgent,
}

// BuiltinAgentNames returns the names of all built-in agents.
func BuiltinAgentNames() []string {
	return slices.Sorted(maps.Keys(builtinAgents))
}

// ResolveAlias resolves an agent reference and returns the alias if it exists and has options.
// Returns nil if the reference is not an alias or doesn't have options.
func ResolveAlias(agentFilename string) *userconfig.Alias {
	agentFilename = cmp.Or(agentFilename, "default")

	cfg, err := userconfig.Load()
	if err != nil {
		slog.Warn("Failed to load user config; aliases are unavailable", "error", err)
		return nil
	}

	alias, ok := cfg.GetAlias(agentFilename)
	if !ok || !alias.HasOptions() {
		return nil
	}

	return alias
}

// ResolveSources resolves an agent file reference (local file, URL, or OCI image) to sources.
// If envProvider is non-nil, it will be used to look up GITHUB_TOKEN for authentication
// when fetching from GitHub URLs.
// For OCI references, always checks remote for updates but falls back to local cache if offline.
func ResolveSources(agentsPath string, envProvider environment.Provider) (Sources, error) {
	resolvedPath, err := resolve(agentsPath)
	if err != nil {
		// resolve() only fails for non-OCI, non-URL, non-builtin references
		// that can't be made absolute. Try OCI as last resort.
		if IsOCIReference(agentsPath) {
			return singleSource(reference.OciRefToFilename(agentsPath), NewOCISource(agentsPath)), nil
		}
		return nil, err
	}

	// Only directories need special handling to enumerate YAML files.
	if dirExists(resolvedPath) {
		return resolveDirectory(resolvedPath, envProvider)
	}

	// For all other reference types, delegate to resolveOne.
	key, source := resolveOne(resolvedPath, envProvider)
	return singleSource(key, source), nil
}

// Resolve resolves an agent file reference (local file, URL, or OCI image) to a source.
// If envProvider is non-nil, it will be used to look up GITHUB_TOKEN for authentication
// when fetching from GitHub URLs.
// For OCI references, always checks remote for updates but falls back to local cache if offline.
func Resolve(agentFilename string, envProvider environment.Provider) (Source, error) {
	resolvedPath, err := resolve(agentFilename)
	if err != nil {
		if IsOCIReference(agentFilename) {
			return NewOCISource(agentFilename), nil
		}
		return nil, err
	}

	_, source := resolveOne(resolvedPath, envProvider)
	return source, nil
}

// resolveOne maps a resolved path to the appropriate Source and a key for use
// in Sources maps. The path must already be resolved via resolve().
// This is the single place that decides which source type a reference maps to.
// To add a new source type, add a case here.
func resolveOne(resolvedPath string, envProvider environment.Provider) (string, Source) {
	switch {
	case builtinAgents[resolvedPath] != nil:
		return resolvedPath, NewBytesSource(resolvedPath, builtinAgents[resolvedPath])
	case IsURLReference(resolvedPath):
		// URL-encode the URL to make it safe for use as a map key
		return url.QueryEscape(resolvedPath), NewURLSource(resolvedPath, envProvider)
	case isLocalFile(resolvedPath):
		return fileNameWithoutExt(resolvedPath), NewFileSource(resolvedPath)
	default:
		return reference.OciRefToFilename(resolvedPath), NewOCISource(resolvedPath)
	}
}

// StableSourceKey reduces a Sources map key to its stable identity: the part
// that persists across variant selectors and caller/environment metadata.
//
// For URL references the identity is the URL's path (scheme + host + path); the
// entire query string and fragment are treated as volatile. This is what lets a
// session created under one variant (e.g. ?gordonTag=v9-light) resume under
// another (?gordonTag=v9-dev) after the server is relaunched with a different
// tag: only the query differs, so both share one identity. Treating the whole
// query as volatile — rather than an enumerated denylist — keeps this robust as
// new query parameters are introduced by callers.
//
// Keys for URL references are url.QueryEscape'd URLs; this decodes the key
// before parsing. For keys that are not URL references (local files, OCI refs,
// built-ins) or that cannot be decoded/parsed, the key is returned unchanged,
// so callers can compare identities without special-casing the source type.
//
// The resume fallback that consumes this (see the session manager's
// resolveSource) always prefers an exact key match and only uses the stable
// identity when it resolves unambiguously, so collapsing distinct query strings
// to one identity never silently selects the wrong side-by-side variant.
func StableSourceKey(key string) string {
	decoded, err := url.QueryUnescape(key)
	if err != nil {
		return key
	}
	if !IsURLReference(decoded) {
		return key
	}
	u, err := url.Parse(decoded)
	if err != nil {
		return key
	}
	u.RawQuery = ""
	u.Fragment = ""
	return u.String()
}

// resolveDirectory enumerates YAML files in a directory and resolves each one.
func resolveDirectory(dirPath string, envProvider environment.Provider) (Sources, error) {
	entries, err := os.ReadDir(dirPath)
	if err != nil {
		return nil, fmt.Errorf("reading agents directory %s: %w", dirPath, err)
	}

	sources := make(Sources)
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		ext := strings.ToLower(filepath.Ext(entry.Name()))
		if ext != ".yaml" && ext != ".yml" && ext != ".hcl" {
			continue
		}
		a := filepath.Join(dirPath, entry.Name())
		sources[fileNameWithoutExt(a)], err = Resolve(a, envProvider)
		if err != nil {
			return nil, err
		}
	}
	return sources, nil
}

// singleSource wraps a single source in a Sources map.
func singleSource(key string, source Source) Sources {
	return Sources{key: source}
}

// resolve resolves an agent reference, handling aliases and defaults
func resolve(agentFilename string) (string, error) {
	agentFilename = cmp.Or(agentFilename, "default")

	// Try to resolve as an alias first
	if cfg, err := userconfig.Load(); err == nil {
		if alias, ok := cfg.GetAlias(agentFilename); ok {
			slog.Debug("Resolved alias", "alias", agentFilename, "path", alias.Path)
			agentFilename = alias.Path
		}
	} else {
		slog.Warn("Failed to load user config; aliases are unavailable", "error", err)
	}

	// Built-in agent names (e.g. "default", "coder") are either user defined aliases or embedded agents
	if _, ok := builtinAgents[agentFilename]; ok {
		return agentFilename, nil
	}

	// Don't convert OCI references or URLs to absolute paths
	if IsOCIReference(agentFilename) || IsURLReference(agentFilename) {
		return agentFilename, nil
	}

	abs, err := filepath.Abs(agentFilename)
	if err != nil {
		return "", err
	}

	return abs, nil
}

// fileExists checks if a file exists at the given path
func fileExists(path string) bool {
	s, err := os.Stat(path)
	exists := err == nil && !s.IsDir()
	return exists
}

// dirExists checks if a directory exists at the given path
func dirExists(path string) bool {
	s, err := os.Stat(path)
	exists := err == nil && s.IsDir()
	return exists
}

// IsOCIReference checks if the input is a valid OCI reference
func IsOCIReference(input string) bool {
	if isLocalFile(input) {
		return false
	}
	_, err := name.ParseReference(input)
	return err == nil
}

// isLocalFile checks if the input is a local file
func isLocalFile(input string) bool {
	// Check for known config file extensions or file descriptors
	if hasConfigFileExt(input) || strings.HasPrefix(input, "/dev/fd/") {
		return true
	}
	// Check if it exists as a file on disk
	return fileExists(input)
}

// hasConfigFileExt reports whether path ends with a known agent config file
// extension (.yaml, .yml or .hcl).
func hasConfigFileExt(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	return ext == ".yaml" || ext == ".yml" || ext == ".hcl"
}

// IsLocalConfigReference reports whether input references a local agent
// config file: a relative or absolute path ending in .yaml, .yml or .hcl.
// The check is purely syntactic — the file does not have to exist — so the
// result never depends on the current directory's contents. A ":" is only
// accepted as part of a Windows drive designator followed by a path
// separator (e.g. "C:\agents\team.yaml"); any other colon means the input
// may carry a "name:reference" prefix (including single-letter names like
// "b:./team.yaml") or a URL scheme and is not a plain path.
func IsLocalConfigReference(input string) bool {
	if !hasConfigFileExt(input) {
		return false
	}
	rest := input
	if len(input) >= 3 && input[1] == ':' && isDriveLetter(input[0]) && isPathSeparator(input[2]) {
		rest = input[2:]
	}
	return !strings.Contains(rest, ":")
}

// isDriveLetter reports whether c can be a Windows drive letter.
func isDriveLetter(c byte) bool {
	return ('a' <= c && c <= 'z') || ('A' <= c && c <= 'Z')
}

// isPathSeparator reports whether c is a Windows or POSIX path separator.
func isPathSeparator(c byte) bool {
	return c == '\\' || c == '/'
}

func fileNameWithoutExt(path string) string {
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	return strings.TrimSuffix(base, ext)
}

// IsExternalReference reports whether the input is an external agent reference
// (OCI image, URL, or local config file path) rather than an agent name
// defined in the same config. Agent names never contain "/" or a config file
// extension, which distinguishes them from OCI references like
// "myorg/agent:tag" and file paths like "./team.yaml".
// It also handles the "name:ref" syntax (e.g. "reviewer:myorg/review-pr").
func IsExternalReference(input string) bool {
	_, ref := ParseExternalAgentRef(input)
	return isExternalRef(ref)
}

// ParseExternalAgentRef parses an external agent reference that may include an
// explicit name prefix. The syntax is "name:reference" where name is a simple
// identifier (no slashes) and reference is an OCI reference, URL, or local
// config file path.
//
// If no explicit name is provided, the base name is derived from the reference:
//   - OCI refs: last path segment without tag (e.g. "myorg/review-pr" → "review-pr")
//   - URLs: filename without extension (e.g. "https://example.com/agent.yaml" → "agent")
//   - local files: filename without extension (e.g. "./secondary-team.yaml" → "secondary-team")
//
// Examples:
//
//	ParseExternalAgentRef("reviewer:myorg/review-pr") → ("reviewer", "myorg/review-pr")
//	ParseExternalAgentRef("myorg/review-pr") → ("review-pr", "myorg/review-pr")
//	ParseExternalAgentRef("docker.io/myorg/myagent:v1") → ("myagent", "docker.io/myorg/myagent:v1")
//	ParseExternalAgentRef("https://example.com/agent.yaml") → ("agent", "https://example.com/agent.yaml")
//	ParseExternalAgentRef("specialists:./secondary-team.yaml") → ("specialists", "./secondary-team.yaml")
func ParseExternalAgentRef(input string) (agentName, ref string) {
	// If the whole input is already a valid external reference, derive the name
	// from it without trying to split on ":".
	if isExternalRef(input) {
		return externalRefBaseName(input), input
	}

	// Check for explicit "name:reference" syntax.
	// A name prefix is identified by not containing "/" (distinguishing it from
	// OCI references or URLs which always contain slashes).
	if i := strings.Index(input, ":"); i > 0 {
		candidate := input[:i]
		if !strings.Contains(candidate, "/") {
			remainder := input[i+1:]
			if isExternalRef(remainder) {
				return candidate, remainder
			}
		}
	}

	// Fallback: return input as both name and ref (for local agent names).
	return input, input
}

// isExternalRef is the core check for whether a string is an external reference.
// It is used by both IsExternalReference and ParseExternalAgentRef to avoid
// circular dependencies.
func isExternalRef(input string) bool {
	return IsURLReference(input) || IsLocalConfigReference(input) || (strings.Contains(input, "/") && IsOCIReference(input))
}

// externalRefBaseName extracts a short agent name from an external reference.
//
//   - OCI: last path segment, tag/digest stripped
//     "myorg/review-pr" → "review-pr"
//     "docker.io/myorg/myagent:v1" → "myagent"
//
//   - URL or local file: filename without extension
//     "https://example.com/agent.yaml" → "agent"
//     "./secondary-team.yaml" → "secondary-team"
func externalRefBaseName(ref string) string {
	if IsURLReference(ref) || IsLocalConfigReference(ref) {
		return fileNameWithoutExt(ref)
	}

	// OCI reference: strip tag or digest, then take last path segment.
	base := ref
	if i := strings.LastIndex(base, "@"); i >= 0 {
		base = base[:i]
	}
	if i := strings.LastIndex(base, ":"); i >= 0 {
		// Only strip if the colon is after the last slash (i.e. it's a tag, not a port).
		if j := strings.LastIndex(base, "/"); j < i {
			base = base[:i]
		}
	}
	if i := strings.LastIndex(base, "/"); i >= 0 {
		base = base[i+1:]
	}
	return base
}
