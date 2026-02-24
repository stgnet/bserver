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
	CacheAge     time.Duration
	StaticAge    time.Duration
	ParentLevels int
	Index        []string
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
	return s
}

// --- Per-vhost config caching ---

type vhostConfigEntry struct {
	settings siteSettings
	modTime  time.Time // mtime of _config.yaml (zero if file absent)
}

var vhostConfigCache sync.Map // docRoot -> *vhostConfigEntry

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
