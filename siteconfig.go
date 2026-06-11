package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// siteSettings holds per-site configuration that can be overridden by each
// virtual host's _config.yaml. Server-wide defaults come from www/_config.yaml.
type siteSettings struct {
	CacheAge       time.Duration
	StaticAge      time.Duration
	ParentLevels   int
	Index          []string
	Types          []string      // allowed file extensions (without dots), e.g. ["html", "css", "jpg"]
	PHPTimeout     time.Duration // idle timeout: kill php-cgi if no output for this long
	PHPStreamAfter time.Duration // buffer php-cgi output for this long before switching to chunked streaming
	AllowHTTP      bool          // serve this vhost over plain HTTP instead of redirecting to HTTPS
	BlockedPaths   []string      // extra path patterns to deny, beyond the built-in dotfile/vendor defaults
	AllowedPaths   []string      // path patterns to exempt from blocking, overriding the defaults and BlockedPaths
}

// loadConfigMap loads a _config.yaml file and returns its contents as a map.
// Returns nil if the file does not exist or cannot be parsed.
func loadConfigMap(path string) map[string]interface{} {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var m map[string]interface{}
	if err := yaml.Unmarshal(data, &m); err != nil {
		log.Printf("Warning: cannot parse %s: %v", path, err)
		return nil
	}
	return m
}

// configString extracts a string value from a config map.
// Returns the value and true if the key exists, or def and false if not.
func configString(m map[string]interface{}, key, def string) (string, bool) {
	if m == nil {
		return def, false
	}
	if v, ok := m[key]; ok {
		return fmt.Sprintf("%v", v), true
	}
	return def, false
}

// configInt extracts an integer value from a config map.
// Returns the value and true if the key exists, or def and false if not.
func configInt(m map[string]interface{}, key string, def int) (int, bool) {
	if m == nil {
		return def, false
	}
	v, ok := m[key]
	if !ok {
		return def, false
	}
	switch n := v.(type) {
	case int:
		return n, true
	case float64:
		return int(n), true
	case string:
		var i int
		if _, err := fmt.Sscanf(n, "%d", &i); err == nil {
			return i, true
		}
	}
	return def, false
}

// configBool extracts a boolean value from a config map.
// Accepts native bools, and the strings "true"/"false"/"yes"/"no"/"1"/"0"
// (case-insensitive). Returns the value and true if the key exists, or def
// and false if not.
func configBool(m map[string]interface{}, key string, def bool) (bool, bool) {
	if m == nil {
		return def, false
	}
	v, ok := m[key]
	if !ok {
		return def, false
	}
	switch b := v.(type) {
	case bool:
		return b, true
	case string:
		switch strings.ToLower(strings.TrimSpace(b)) {
		case "true", "yes", "1":
			return true, true
		case "false", "no", "0":
			return false, true
		}
	case int:
		return b != 0, true
	}
	return def, false
}

// configIndex extracts an index priority list from a config map.
// Supports both YAML lists and comma-separated strings.
// Returns the list and true if the key exists, or nil and false if not.
func configIndex(m map[string]interface{}, key string) ([]string, bool) {
	if m == nil {
		return nil, false
	}
	v, ok := m[key]
	if !ok {
		return nil, false
	}
	switch val := v.(type) {
	case string:
		var parts []string
		for _, p := range strings.Split(val, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				parts = append(parts, p)
			}
		}
		return parts, true
	case []interface{}:
		var parts []string
		for _, item := range val {
			if s := fmt.Sprintf("%v", item); s != "" {
				parts = append(parts, s)
			}
		}
		return parts, true
	}
	return nil, false
}

// applySiteSettings extracts per-site settings from a config map,
// overriding the provided defaults for any keys present.
func applySiteSettings(m map[string]interface{}, defaults siteSettings) siteSettings {
	s := defaults
	if m == nil {
		return s
	}
	if v, ok := configInt(m, "cache-age", 0); ok {
		s.CacheAge = time.Duration(v) * time.Second
	}
	if v, ok := configInt(m, "static-age", 0); ok {
		s.StaticAge = time.Duration(v) * time.Second
	}
	if v, ok := configInt(m, "parent-levels", 0); ok {
		s.ParentLevels = v
	}
	if idx, ok := configIndex(m, "index"); ok {
		s.Index = idx
	}
	if types, ok := configIndex(m, "types"); ok {
		s.Types = normalizeTypes(types)
	}
	if v, ok := configInt(m, "php-timeout", 0); ok && v > 0 {
		s.PHPTimeout = time.Duration(v) * time.Second
	}
	if v, ok := configInt(m, "php-stream-after", 0); ok && v >= 0 {
		s.PHPStreamAfter = time.Duration(v) * time.Second
	}
	if v, ok := configBool(m, "allow-http", false); ok {
		s.AllowHTTP = v
		if v {
			log.Printf("Warning: allow-http=true — HTTPS redirect disabled; session cookies and other secrets may transit in cleartext")
		}
	}
	if v, ok := configIndex(m, "block-paths"); ok {
		s.BlockedPaths = normalizePathPatterns(v)
	}
	if v, ok := configIndex(m, "allow-paths"); ok {
		s.AllowedPaths = normalizePathPatterns(v)
	}
	return s
}

// normalizePathPatterns trims whitespace from each pattern and drops empties.
// Path patterns are case-sensitive (filesystem paths on Linux are too).
func normalizePathPatterns(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, p := range raw {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// pathBlocked reports whether the cleaned URL path should be denied (404).
//
// Precedence:
//  1. Allow list — the built-in "/.well-known" exemption plus any allow-paths
//     from _config.yaml. A match here always wins, so an operator can expose a
//     path that the defaults below would otherwise deny.
//  2. Built-in denies — any hidden segment (a dot-prefixed file or directory,
//     e.g. .git, .env) and any "vendor" directory at any depth.
//  3. block-paths from _config.yaml — additional operator-defined denies.
func (s siteSettings) pathBlocked(upath string) bool {
	for _, p := range s.AllowedPaths {
		if pathMatchesPattern(upath, p) {
			return false
		}
	}
	if pathMatchesPattern(upath, "/.well-known") {
		return false
	}
	if hasHiddenSegment(upath) || pathMatchesPattern(upath, "vendor") {
		return true
	}
	for _, p := range s.BlockedPaths {
		if pathMatchesPattern(upath, p) {
			return true
		}
	}
	return false
}

// hasHiddenSegment reports whether any segment of the cleaned URL path begins
// with a dot (a hidden file or directory), e.g. "/.git/index" or "/.env".
func hasHiddenSegment(upath string) bool {
	for _, seg := range splitPath(upath) {
		if seg[0] == '.' && seg != "." && seg != ".." {
			return true
		}
	}
	return false
}

// pathMatchesPattern reports whether the cleaned request path is matched by
// pattern. Two pattern forms:
//
//   - Bare name (single segment, no slash), e.g. "vendor": matches if ANY
//     segment of the path equals it — i.e. the named directory at any depth.
//   - Rooted prefix (leading slash or multiple segments), e.g. "/vendor" or
//     "vendor/public": matches the path only from the docroot, when the
//     pattern's segments are a leading prefix of the path's segments.
//
// Matching is segment-aware, so "vendor" never matches "/vendored/x".
func pathMatchesPattern(upath, pattern string) bool {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" || pattern == "/" {
		return false
	}
	rooted := strings.HasPrefix(pattern, "/")
	pat := splitPath(pattern)
	if len(pat) == 0 {
		return false
	}
	if len(pat) > 1 {
		rooted = true // multi-segment patterns are inherently rooted prefixes
	}
	segs := splitPath(upath)
	if !rooted {
		for _, s := range segs {
			if s == pat[0] {
				return true
			}
		}
		return false
	}
	if len(pat) > len(segs) {
		return false
	}
	for i, p := range pat {
		if segs[i] != p {
			return false
		}
	}
	return true
}

// splitPath splits a slash path into its non-empty segments.
func splitPath(p string) []string {
	t := strings.Trim(p, "/")
	if t == "" {
		return nil
	}
	return strings.Split(t, "/")
}

// normalizeTypes lowercases each entry and strips any leading dot.
func normalizeTypes(raw []string) []string {
	out := make([]string, 0, len(raw))
	for _, t := range raw {
		t = strings.TrimSpace(strings.ToLower(t))
		t = strings.TrimPrefix(t, ".")
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// isAllowedType checks whether a file extension (with or without leading dot)
// is in the allowed types list. Returns true if the list is empty (no filtering).
func isAllowedType(ext string, types []string) bool {
	if len(types) == 0 {
		return true
	}
	ext = strings.TrimPrefix(strings.ToLower(ext), ".")
	if ext == "" {
		return true // no extension — handled elsewhere (sibling lookup etc.)
	}
	for _, t := range types {
		if t == ext {
			return true
		}
	}
	return false
}

// --- Per-vhost config caching ---

type vhostConfigEntry struct {
	settings siteSettings
	modTime  time.Time // mtime of _config.yaml (zero if file absent)
}

var vhostConfigCache sync.Map // docRoot -> *vhostConfigEntry

func vhostConfigCacheSize() int {
	n := 0
	vhostConfigCache.Range(func(_, _ any) bool { n++; return true })
	return n
}

// vhostSettings returns the effective site settings for a given docRoot,
// checking for a per-vhost _config.yaml override. Results are cached with
// mtime-based invalidation.
func vhostSettings(docRoot string, defaults siteSettings) siteSettings {
	configPath := filepath.Join(docRoot, "_config.yaml")

	// Check file mtime
	var currentMtime time.Time
	if info, err := os.Stat(configPath); err == nil {
		currentMtime = info.ModTime()
	}

	// Return cached if mtime matches
	if cached, ok := vhostConfigCache.Load(docRoot); ok {
		entry := cached.(*vhostConfigEntry)
		if entry.modTime.Equal(currentMtime) {
			return entry.settings
		}
	}

	// Load and cache
	var settings siteSettings
	if currentMtime.IsZero() {
		settings = defaults
	} else {
		m := loadConfigMap(configPath)
		settings = applySiteSettings(m, defaults)
	}

	vhostConfigCache.Store(docRoot, &vhostConfigEntry{
		settings: settings,
		modTime:  currentMtime,
	})

	return settings
}
