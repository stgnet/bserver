package main

import (
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// memMonitorConfig holds tunables for the memory monitor. Zero values
// expand to sensible defaults inside newMemMonitor.
type memMonitorConfig struct {
	Interval           time.Duration
	HeapThresholdMB    int
	GoroutineThreshold int
	GrowthMBPer5Min    int
	DumpDir            string
	DumpCooldown       time.Duration
	DumpMaxFiles       int
	PprofAddr          string
}

// cacheProvider reports a cache's current entry count for the snapshot log.
type cacheProvider = func() int

// memMonitor periodically samples runtime memory stats and known cache sizes,
// emits a greppable log line, and writes pprof dumps when thresholds are
// crossed. The goal is to leave a diagnostic trail *before* an OOM kill.
type memMonitor struct {
	cfg         memMonitorConfig
	rc          *renderCache
	providers   map[string]cacheProvider
	providerKey []string // stable iteration order for log output

	stopCh chan struct{}
	wg     sync.WaitGroup

	startTime time.Time // for startup grace period

	mu           sync.Mutex
	ring         []uint64 // recent HeapInuse samples (byte)
	lastHeapDump time.Time
	lastGoDump   time.Time

	// Dumps run on background goroutines so a slow pprof.WriteTo (which
	// can block for minutes when the heap is under GC pressure) doesn't
	// stall the snapshot ticker. The atomic flags prevent piling up
	// duplicate dumps while one is already running.
	heapDumpInFlight atomic.Bool
	goDumpInFlight   atomic.Bool
}

const slowDumpThreshold = 30 * time.Second

// startupGrace suppresses growth-rate alarms during warmup. The heap ramps
// from a few MB to steady-state (tens of MB) as caches populate, trivially
// exceeding any reasonable growth threshold. After this window the delta
// reflects real post-warmup behavior.
const startupGrace = 15 * time.Minute

const (
	defaultMemInterval         = 60 * time.Second
	defaultHeapThresholdMB     = 500
	defaultGoroutineThreshold  = 2000
	defaultGrowthMBPer5Min     = 150
	defaultMemDumpDir          = "/var/lib/bserver-diag"
	defaultMemDumpCooldown     = 10 * time.Minute
	defaultMemDumpMaxFiles     = 10
	defaultPprofAddr           = "127.0.0.1:6060"
	memRingSize                = 6 // ~5 minutes at 60s ticks
)

func newMemMonitor(cfg memMonitorConfig, rc *renderCache, providers map[string]cacheProvider) *memMonitor {
	if cfg.Interval <= 0 {
		cfg.Interval = defaultMemInterval
	}
	if cfg.HeapThresholdMB <= 0 {
		cfg.HeapThresholdMB = defaultHeapThresholdMB
	}
	if cfg.GoroutineThreshold <= 0 {
		cfg.GoroutineThreshold = defaultGoroutineThreshold
	}
	if cfg.GrowthMBPer5Min <= 0 {
		cfg.GrowthMBPer5Min = defaultGrowthMBPer5Min
	}
	if cfg.DumpDir == "" {
		cfg.DumpDir = defaultMemDumpDir
	}
	if cfg.DumpCooldown <= 0 {
		cfg.DumpCooldown = defaultMemDumpCooldown
	}
	if cfg.DumpMaxFiles <= 0 {
		cfg.DumpMaxFiles = defaultMemDumpMaxFiles
	}

	keys := make([]string, 0, len(providers))
	for k := range providers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return &memMonitor{
		cfg:         cfg,
		rc:          rc,
		providers:   providers,
		providerKey: keys,
		stopCh:      make(chan struct{}),
		startTime:   time.Now(),
		ring:        make([]uint64, 0, memRingSize),
	}
}

func (m *memMonitor) Start() {
	m.wg.Add(1)
	go m.loop()
}

func (m *memMonitor) Close() {
	close(m.stopCh)
	m.wg.Wait()
}

func (m *memMonitor) loop() {
	defer m.wg.Done()
	t := time.NewTicker(m.cfg.Interval)
	defer t.Stop()
	m.tick() // emit baseline snapshot immediately so startup is visible
	for {
		select {
		case <-m.stopCh:
			return
		case <-t.C:
			m.tick()
		}
	}
}

func (m *memMonitor) tick() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	goroutines := runtime.NumGoroutine()

	var renderEntries int
	var renderBytes int64
	if m.rc != nil {
		renderEntries, renderBytes = m.rc.Stats()
	}

	cacheCounts := make(map[string]int, len(m.providers))
	cacheTotal := 0
	for _, name := range m.providerKey {
		n := m.providers[name]()
		cacheCounts[name] = n
		cacheTotal += n
	}

	// Build snapshot line
	var b strings.Builder
	b.WriteString("MEMMON")
	fmt.Fprintf(&b, " heap_alloc=%dMB", bytesToMB(ms.HeapAlloc))
	fmt.Fprintf(&b, " heap_inuse=%dMB", bytesToMB(ms.HeapInuse))
	fmt.Fprintf(&b, " heap_sys=%dMB", bytesToMB(ms.HeapSys))
	fmt.Fprintf(&b, " goroutines=%d", goroutines)
	fmt.Fprintf(&b, " num_gc=%d", ms.NumGC)
	fmt.Fprintf(&b, " last_pause_ms=%.2f", lastPauseMs(&ms))
	fmt.Fprintf(&b, " render_entries=%d", renderEntries)
	fmt.Fprintf(&b, " render_bytes=%dMB", bytesToMB(uint64(renderBytes)))
	for _, name := range m.providerKey {
		fmt.Fprintf(&b, " %s=%d", name, cacheCounts[name])
	}
	if m.heapDumpInFlight.Load() {
		b.WriteString(" dump_in_flight=heap")
	}
	if m.goDumpInFlight.Load() {
		b.WriteString(" dump_in_flight=goroutine")
	}
	log.Print(b.String())

	// Ring buffer for growth rate
	m.mu.Lock()
	m.ring = append(m.ring, ms.HeapInuse)
	if len(m.ring) > memRingSize {
		m.ring = m.ring[len(m.ring)-memRingSize:]
	}
	oldest := m.ring[0]
	ringLen := len(m.ring)
	lastHeap := m.lastHeapDump
	lastGo := m.lastGoDump
	m.mu.Unlock()

	// Growth-rate alarm: compare current to oldest in ring (~5 min if full).
	// Only meaningful once we have a full window AND the startup grace has
	// elapsed — otherwise a cold-start ramp triggers false positives.
	if ringLen >= memRingSize && time.Since(m.startTime) >= startupGrace {
		deltaMB := int64(bytesToMB(ms.HeapInuse)) - int64(bytesToMB(oldest))
		if deltaMB > int64(m.cfg.GrowthMBPer5Min) {
			// If cache growth explains most of the heap growth, don't warn —
			// that's expected activity, not a leak. This is a rough heuristic:
			// we don't know the per-cache byte footprint, only entry counts.
			// So we warn when heap grew AND no cache is growing notably.
			cacheGrowing := false
			for _, n := range cacheCounts {
				if n > 0 && n >= cacheTotal/2 {
					cacheGrowing = true
					break
				}
			}
			if !cacheGrowing || cacheTotal == 0 {
				var wb strings.Builder
				wb.WriteString("MEMMON_WARN")
				fmt.Fprintf(&wb, " growth_5m=%dMB heap_inuse=%dMB goroutines=%d", deltaMB, bytesToMB(ms.HeapInuse), goroutines)
				for _, name := range m.providerKey {
					fmt.Fprintf(&wb, " %s=%d", name, cacheCounts[name])
				}
				wb.WriteString(" (no matching cache growth)")
				log.Print(wb.String())
			}
		}
	}

	// Heap threshold dump (asynchronous: a stuck pprof.WriteTo must not
	// block the snapshot ticker — that's how the May 16 OOM happened
	// invisibly). The cooldown timestamp is set on dispatch, not on
	// completion, so a slow dump doesn't queue up successors.
	if int(bytesToMB(ms.HeapInuse)) >= m.cfg.HeapThresholdMB &&
		time.Since(lastHeap) >= m.cfg.DumpCooldown &&
		m.heapDumpInFlight.CompareAndSwap(false, true) {
		m.mu.Lock()
		m.lastHeapDump = time.Now()
		m.mu.Unlock()
		heapInuse := bytesToMB(ms.HeapInuse)
		go m.runDump("heap", "heap_threshold",
			fmt.Sprintf("heap_inuse=%dMB", heapInuse),
			m.dumpHeap, &m.heapDumpInFlight)
	}

	// Goroutine threshold dump (same async treatment)
	if goroutines >= m.cfg.GoroutineThreshold &&
		time.Since(lastGo) >= m.cfg.DumpCooldown &&
		m.goDumpInFlight.CompareAndSwap(false, true) {
		m.mu.Lock()
		m.lastGoDump = time.Now()
		m.mu.Unlock()
		count := goroutines
		go m.runDump("goroutine", "goroutine_threshold",
			fmt.Sprintf("count=%d", count),
			m.dumpGoroutines, &m.goDumpInFlight)
	}
}

// runDump executes a dump function on a background goroutine, releasing
// the in-flight flag when done and logging slow or failed dumps.
func (m *memMonitor) runDump(kind, reason, context string, dump func() (string, error), inFlight *atomic.Bool) {
	defer inFlight.Store(false)
	start := time.Now()
	path, err := dump()
	elapsed := time.Since(start)
	if err != nil {
		log.Printf("MEMMON_WARN dump_failed=%s err=%v elapsed=%v", kind, err, elapsed.Round(time.Millisecond))
		return
	}
	log.Printf("MEMMON_DUMP kind=%s reason=%s %s elapsed=%v file=%s",
		kind, reason, context, elapsed.Round(time.Millisecond), path)
	if elapsed > slowDumpThreshold {
		log.Printf("MEMMON_WARN dump_slow kind=%s elapsed=%v threshold=%v file=%s",
			kind, elapsed.Round(time.Millisecond), slowDumpThreshold, path)
	}
}

func bytesToMB(b uint64) uint64 { return b / (1024 * 1024) }

func lastPauseMs(ms *runtime.MemStats) float64 {
	if ms.NumGC == 0 {
		return 0
	}
	idx := (ms.NumGC + 255) % 256
	return float64(ms.PauseNs[idx]) / 1e6
}

// dumpHeap writes a heap profile to DumpDir (falling back to os.TempDir on
// failure), prunes old dumps, and returns the path on success.
func (m *memMonitor) dumpHeap() (string, error) {
	return m.writeProfile("heap", 0, "pprof")
}

// dumpGoroutines writes a full goroutine stack dump (text form, level 2) so
// it's greppable without `go tool pprof`.
func (m *memMonitor) dumpGoroutines() (string, error) {
	return m.writeProfile("goroutine", 2, "txt")
}

func (m *memMonitor) writeProfile(name string, debug int, ext string) (string, error) {
	prof := pprof.Lookup(name)
	if prof == nil {
		return "", fmt.Errorf("profile %q not available", name)
	}
	ts := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("%s-%s.%s", name, ts, ext)

	dir := m.cfg.DumpDir
	path, err := m.tryWrite(dir, filename, prof, debug)
	if err != nil && dir != os.TempDir() {
		// Fall back to tempdir so we at least get a dump before OOM.
		fallback := os.TempDir()
		log.Printf("MEMMON_WARN dump_fallback primary=%s fallback=%s err=%v", dir, fallback, err)
		path, err = m.tryWrite(fallback, filename, prof, debug)
	}
	if err == nil {
		m.pruneOld(filepath.Dir(path), name)
	}
	return path, err
}

func (m *memMonitor) tryWrite(dir, filename string, prof *pprof.Profile, debug int) (string, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return "", err
	}
	path := filepath.Join(dir, filename)
	f, err := os.Create(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	if err := prof.WriteTo(f, debug); err != nil {
		return "", err
	}
	return path, nil
}

// pruneOld keeps at most DumpMaxFiles dumps of a given kind, removing the
// oldest by filename (timestamps sort lexicographically).
func (m *memMonitor) pruneOld(dir, kind string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	prefix := kind + "-"
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasPrefix(e.Name(), prefix) {
			names = append(names, e.Name())
		}
	}
	if len(names) <= m.cfg.DumpMaxFiles {
		return
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-m.cfg.DumpMaxFiles] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}

// maybeStartPprof spins up a localhost-only pprof HTTP server on its own
// http.Server (never touches autocert). Returns quickly; the listener runs
// in the background for the lifetime of the process.
func maybeStartPprof(addr string) {
	if addr == "" {
		return
	}
	srv := &http.Server{
		Addr:              addr,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		log.Printf("pprof listening on %s", addr)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Printf("pprof server error: %v", err)
		}
	}()
}
