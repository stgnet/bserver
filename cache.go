package main

import (
	"container/list"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"
)

const (
	defaultCacheMaxAge  = 15 * time.Minute
	defaultCacheMaxSize = 1 << 30 // 1 GB
)

// cacheEntry holds a single cached render result.
type cacheEntry struct {
	key         string
	output      string
	sourceFiles []string // all files loaded during rendering (for dependency tracking)
	createdAt   time.Time
	size        int64
	element     *list.Element // position in LRU list
}

// renderCache provides an in-memory LRU cache for rendered pages.
//
// Cache entries are invalidated automatically when source files change
// (via fsnotify), when they exceed maxAge, or when total cache size
// exceeds maxSize (oldest entries evicted first).
//
// Only rendered output (YAML and markdown pages) should be cached.
// Static files served verbatim from disk are not cached here.
type renderCache struct {
	mu          sync.Mutex
	entries     map[string]*cacheEntry
	lru         *list.List // front = most recently used
	maxSize     int64
	maxAge      time.Duration
	currentSize int64

	// File watching for automatic invalidation
	watcher     *fsnotify.Watcher
	fileDeps    map[string]map[string]bool // source file path → set of cache keys
	dirDeps     map[string]map[string]bool // directory path → set of cache keys
	watchedDirs map[string]bool

	stopCh chan struct{}
}

// newRenderCache creates a new render cache with the given size limit and max age.
// It starts a file watcher goroutine for automatic cache invalidation.
func newRenderCache(maxSize int64, maxAge time.Duration) *renderCache {
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("Warning: file watcher unavailable: %v (cache will use time-based expiry only)", err)
	}

	rc := &renderCache{
		entries:     make(map[string]*cacheEntry),
		lru:         list.New(),
		maxSize:     maxSize,
		maxAge:      maxAge,
		watcher:     watcher,
		fileDeps:    make(map[string]map[string]bool),
		dirDeps:     make(map[string]map[string]bool),
		watchedDirs: make(map[string]bool),
		stopCh:      make(chan struct{}),
	}

	if watcher != nil {
		go rc.watchLoop()
	}

	return rc
}

// Get retrieves a cached render result.
// Returns the cached HTML and true on hit, or empty string and false on miss.
func (rc *renderCache) Get(key string) (string, bool) {
	rc.mu.Lock()
	defer rc.mu.Unlock()

	entry, ok := rc.entries[key]
	if !ok {
		return "", false
	}

	// Check age
	if time.Since(entry.createdAt) > rc.maxAge {
		rc.removeLocked(entry)
		return "", false
	}

	// Move to front of LRU
	rc.lru.MoveToFront(entry.element)
	return entry.output, true
}

// Put stores a render result in the cache with its source file dependencies.
// The source files are watched for changes; any change invalidates this entry.
func (rc *renderCache) Put(key, output string, sourceFiles []string) {
	size := int64(len(output))

	// Don't cache if single entry exceeds max size
	if size > rc.maxSize {
		return
	}

	rc.mu.Lock()
	defer rc.mu.Unlock()

	// Remove old entry if exists
	if old, ok := rc.entries[key]; ok {
		rc.removeLocked(old)
	}

	// Evict oldest entries until we have room
	for rc.currentSize+size > rc.maxSize && rc.lru.Len() > 0 {
		oldest := rc.lru.Back()
		if oldest != nil {
			rc.removeLocked(oldest.Value.(*cacheEntry))
		}
	}

	entry := &cacheEntry{
		key:         key,
		output:      output,
		sourceFiles: sourceFiles,
		createdAt:   time.Now(),
		size:        size,
	}
	entry.element = rc.lru.PushFront(entry)
	rc.entries[key] = entry
	rc.currentSize += size

	// Register file dependencies and watch directories
	for _, f := range sourceFiles {
		if rc.fileDeps[f] == nil {
			rc.fileDeps[f] = make(map[string]bool)
		}
		rc.fileDeps[f][key] = true

		dir := filepath.Dir(f)
		if rc.dirDeps[dir] == nil {
			rc.dirDeps[dir] = make(map[string]bool)
		}
		rc.dirDeps[dir][key] = true

		if !rc.watchedDirs[dir] && rc.watcher != nil {
			if err := rc.watcher.Add(dir); err == nil {
				rc.watchedDirs[dir] = true
			}
		}
	}
}

// removeLocked removes a cache entry and cleans up its file dependencies.
// Must be called with rc.mu held.
func (rc *renderCache) removeLocked(entry *cacheEntry) {
	rc.lru.Remove(entry.element)
	delete(rc.entries, entry.key)
	rc.currentSize -= entry.size

	// Clean up file and directory dependency maps
	for _, f := range entry.sourceFiles {
		if deps, ok := rc.fileDeps[f]; ok {
			delete(deps, entry.key)
			if len(deps) == 0 {
				delete(rc.fileDeps, f)
			}
		}
		dir := filepath.Dir(f)
		if deps, ok := rc.dirDeps[dir]; ok {
			delete(deps, entry.key)
			if len(deps) == 0 {
				delete(rc.dirDeps, dir)
				// Unwatch directory if no more deps
				if rc.watcher != nil {
					rc.watcher.Remove(dir)
				}
				delete(rc.watchedDirs, dir)
			}
		}
	}
}

// watchLoop processes file system events from fsnotify.
func (rc *renderCache) watchLoop() {
	for {
		select {
		case event, ok := <-rc.watcher.Events:
			if !ok {
				return
			}
			rc.handleFileEvent(event)
		case err, ok := <-rc.watcher.Errors:
			if !ok {
				return
			}
			log.Printf("cache watcher error: %v", err)
		case <-rc.stopCh:
			return
		}
	}
}

// handleFileEvent invalidates cache entries affected by a file system change.
//
// For WRITE/REMOVE/RENAME: invalidates entries that depend on the changed file.
// For CREATE: also invalidates entries with sources in the same directory,
// since a new file might change YAML name resolution order.
func (rc *renderCache) handleFileEvent(event fsnotify.Event) {
	path := event.Name

	// Collect keys to invalidate (under lock), then invalidate (reacquires lock)
	rc.mu.Lock()
	var keysToInvalidate []string

	// Direct file dependency
	if deps, ok := rc.fileDeps[path]; ok {
		for key := range deps {
			keysToInvalidate = append(keysToInvalidate, key)
		}
	}

	// For CREATE events, a new file in a watched directory might change
	// name resolution (e.g., a new navbar.yaml overriding an inherited one).
	if event.Has(fsnotify.Create) {
		dir := filepath.Dir(path)
		if deps, ok := rc.dirDeps[dir]; ok {
			for key := range deps {
				keysToInvalidate = append(keysToInvalidate, key)
			}
		}
	}
	rc.mu.Unlock()

	// Deduplicate and invalidate
	seen := make(map[string]bool)
	for _, key := range keysToInvalidate {
		if !seen[key] {
			seen[key] = true
			rc.mu.Lock()
			if entry, ok := rc.entries[key]; ok {
				rc.removeLocked(entry)
			}
			rc.mu.Unlock()
		}
	}
}

// Close stops the file watcher and clears the cache.
func (rc *renderCache) Close() {
	close(rc.stopCh)
	if rc.watcher != nil {
		rc.watcher.Close()
	}
}

// Stats returns cache statistics.
func (rc *renderCache) Stats() (entries int, size int64) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.entries), rc.currentSize
}

// detectAvailableRAM checks the system's available memory and returns
// a recommended cache size limit. On Linux, it reads /proc/meminfo.
// When no swap is available, it is extra conservative to avoid memory
// pressure and page thrashing. On non-Linux platforms it returns the
// configured default.
func detectAvailableRAM(configuredMax int64) int64 {
	if runtime.GOOS != "linux" {
		return configuredMax
	}

	memAvailable, swapTotal, err := readMemInfo()
	if err != nil || memAvailable == 0 {
		return configuredMax
	}

	// If no swap, be very conservative: use at most 25% of available RAM.
	// If swap exists, use at most 50% of available RAM.
	var limit int64
	if swapTotal == 0 {
		limit = memAvailable / 4
		if limit < configuredMax {
			log.Printf("No swap detected; limiting cache to 25%% of available RAM")
		}
	} else {
		limit = memAvailable / 2
	}

	if limit < configuredMax {
		log.Printf("Warning: available RAM (%s) limits render cache to %s (configured: %s)",
			formatBytes(memAvailable), formatBytes(limit), formatBytes(configuredMax))
		return limit
	}

	return configuredMax
}

// readMemInfo parses /proc/meminfo for MemAvailable and SwapTotal (in bytes).
func readMemInfo() (memAvailable, swapTotal int64, err error) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		val *= 1024 // convert kB to bytes
		switch fields[0] {
		case "MemAvailable:":
			memAvailable = val
		case "SwapTotal:":
			swapTotal = val
		}
	}
	return memAvailable, swapTotal, nil
}

// formatBytes formats a byte count as a human-readable string.
func formatBytes(b int64) string {
	const (
		mb = 1 << 20
		gb = 1 << 30
	)
	if b >= gb {
		return fmt.Sprintf("%.1f GB", float64(b)/float64(gb))
	}
	return fmt.Sprintf("%.1f MB", float64(b)/float64(mb))
}

// cacheKey builds a cache key from the document root and file path.
func cacheKey(docRoot, filePath string) string {
	return docRoot + "\x00" + filePath
}

// staticFileCacheControl returns a Cache-Control max-age value for a static file,
// based on how long ago it was last modified. The heuristic uses half the file's
// age, capped at maxStaticAge.
func staticFileCacheControl(modTime time.Time, maxStaticAge time.Duration) string {
	if modTime.IsZero() {
		return ""
	}
	age := time.Since(modTime)
	if age < 0 {
		age = 0
	}
	maxAge := age / 2
	if maxAge > maxStaticAge {
		maxAge = maxStaticAge
	}
	// Minimum 60 seconds to avoid very short cache times for recently modified files
	if maxAge < 60*time.Second {
		maxAge = 60 * time.Second
	}
	return fmt.Sprintf("public, max-age=%d", int(maxAge.Seconds()))
}
