package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dop251/goja"
)

// jsHeapLimitBytes caps the additional process heap a single runJS call is
// allowed to grow by before being interrupted. 0 disables the check.
//
// Notes / caveats:
//   - The check is reactive: runtime.ReadMemStats is sampled every
//     jsHeapProbeInterval. A single huge allocation between probes can
//     still OOM the process; a deployment-level cgroup MemoryMax= is the
//     belt-and-braces backstop for that.
//   - HeapAlloc is process-global. With concurrent JS scripts, each sees
//     the combined growth. That biases toward false positives (innocent
//     scripts may be interrupted when a sibling runs away), which is
//     preferable to false negatives (a runaway escapes detection).
//   - vm.Interrupt only fires between bytecode ops; long native-side
//     work (e.g., a single huge String.repeat) doesn't yield until done.
var jsHeapLimitBytes atomic.Int64

const jsHeapProbeInterval = 100 * time.Millisecond

// SetJSHeapLimit sets the per-script heap growth cap in bytes. 0 disables
// the check. Called once during server startup from main.
func SetJSHeapLimit(b int64) { jsHeapLimitBytes.Store(b) }

// jsConcurrencySem bounds concurrent goja Runtime instances. Each runtime
// plus a user script can consume several MB; under thundering-herd load
// (e.g. a burst of ~200 simultaneous requests for an uncached path whose
// navbar uses a JS data source) the unbounded fan-out drove heap spikes
// from 150 MB to 700+ MB in a single tick. Worse, the per-script heap
// watchdog reads process-global HeapAlloc, so each concurrent script sees
// the combined sibling growth and many get falsely interrupted with
// "exceeded memory limit" once the herd is large enough.
//
// A modest cap preserves throughput for normal traffic (a 50 ms script
// gives ~640/s headroom at this size) while keeping the worst-case
// concurrent JS heap footprint bounded.
var jsConcurrencySem = make(chan struct{}, 32)

// jsAcquireTimeout caps how long runJS will wait for a concurrency slot.
// Long enough to ride out a brief queue, short enough to fail before the
// outer HTTP write deadline fires (default 120 s).
const jsAcquireTimeout = 15 * time.Second

// errJSConcurrencyTimeout is returned when a runJS call could not acquire
// a runtime slot within jsAcquireTimeout. Distinct error so callers (e.g.
// data source / script execution paths) can log it specifically.
var errJSConcurrencyTimeout = errors.New("js concurrency queue timeout")

// jsProgramCache caches compiled JavaScript programs keyed by source hash.
// Compilation costs microseconds but is the dominant overhead for short
// scripts; execution on a cached program is sub-microsecond. Runtimes are
// NOT goroutine-safe, so a fresh Runtime is created per invocation while
// the compiled Program is shared.
var jsProgramCache sync.Map // hex(sha256(source)) -> *goja.Program

func compileJS(source string) (*goja.Program, error) {
	sum := sha256.Sum256([]byte(source))
	key := hex.EncodeToString(sum[:])
	if cached, ok := jsProgramCache.Load(key); ok {
		return cached.(*goja.Program), nil
	}
	p, err := goja.Compile("script", source, true)
	if err != nil {
		return nil, err
	}
	jsProgramCache.Store(key, p)
	return p, nil
}

// runJS evaluates user JavaScript in a fresh goja runtime with a small set
// of host builtins. If wrap is true, the source is wrapped in a loop that
// exposes `record` per iteration (data-driven format script semantics).
// If wrap is false, the source runs once (data-source semantics).
//
// File system access (listdir, readFile, readFileHead) is restricted to
// docRoot — paths that escape via "..", absolute paths outside docRoot, or
// symlinks pointing outside docRoot return an error. An empty docRoot
// disables file access entirely.
//
// Host builtins available to all scripts:
//
//	env        — object: env.VAR returns "" if unset
//	print(...) — appends a space-joined line to captured output
//	listdir(p) — array of entry names (sorted order not guaranteed)
//	readFileHead(p, n) — first n bytes of file p as string
//	readFile(p) — entire file contents as string
//	joinPath(a, b, ...) — filepath.Join wrapper
//	splitExt(name)      — [basename, ext] pair (ext includes leading dot)
func runJS(source string, envMap map[string]string, dataJSON []byte, wrap bool, docRoot string) (string, error) {
	final := source
	if wrap {
		final = jsFormatWrapper(source)
	}
	prog, err := compileJS(final)
	if err != nil {
		return "", fmt.Errorf("compile: %w", err)
	}

	// Bound concurrent runtimes. See jsConcurrencySem docs above.
	select {
	case jsConcurrencySem <- struct{}{}:
		defer func() { <-jsConcurrencySem }()
	case <-time.After(jsAcquireTimeout):
		return "", errJSConcurrencyTimeout
	}

	vm := goja.New()

	var out strings.Builder

	envObj := vm.NewObject()
	for k, v := range envMap {
		_ = envObj.Set(k, v)
	}
	_ = vm.Set("env", envObj)

	_ = vm.Set("print", func(call goja.FunctionCall) goja.Value {
		parts := make([]string, len(call.Arguments))
		for i, a := range call.Arguments {
			parts[i] = a.String()
		}
		out.WriteString(strings.Join(parts, " "))
		out.WriteString("\n")
		return goja.Undefined()
	})
	_ = vm.Set("listdir", func(p string) ([]string, error) {
		safe, err := resolveUnderRoot(docRoot, p)
		if err != nil {
			return nil, err
		}
		entries, err := os.ReadDir(safe)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names, nil
	})
	_ = vm.Set("readFileHead", func(p string, n int) (string, error) {
		if n <= 0 {
			return "", nil
		}
		safe, err := resolveUnderRoot(docRoot, p)
		if err != nil {
			return "", err
		}
		f, err := os.Open(safe)
		if err != nil {
			return "", err
		}
		defer f.Close()
		buf := make([]byte, n)
		r, err := io.ReadFull(f, buf)
		if err != nil && err != io.ErrUnexpectedEOF && err != io.EOF {
			return "", err
		}
		return string(buf[:r]), nil
	})
	_ = vm.Set("readFile", func(p string) (string, error) {
		safe, err := resolveUnderRoot(docRoot, p)
		if err != nil {
			return "", err
		}
		data, err := os.ReadFile(safe)
		if err != nil {
			return "", err
		}
		return string(data), nil
	})
	_ = vm.Set("joinPath", filepath.Join)
	_ = vm.Set("splitExt", func(name string) []string {
		ext := filepath.Ext(name)
		return []string{strings.TrimSuffix(name, ext), ext}
	})
	if dataJSON != nil {
		_ = vm.Set("_SCRIPT_DATA", string(dataJSON))
	} else {
		_ = vm.Set("_SCRIPT_DATA", "")
	}

	done := make(chan struct{})
	go func() {
		select {
		case <-time.After(30 * time.Second):
			vm.Interrupt("timeout")
		case <-done:
		}
	}()
	defer close(done)

	// Optional heap-growth watchdog. Captures a process-wide HeapAlloc
	// baseline and interrupts the VM if the heap grows beyond the cap
	// during this script's run. See jsHeapLimitBytes notes for caveats.
	if limit := jsHeapLimitBytes.Load(); limit > 0 {
		var baseline runtime.MemStats
		runtime.ReadMemStats(&baseline)
		go func() {
			ticker := time.NewTicker(jsHeapProbeInterval)
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					var ms runtime.MemStats
					runtime.ReadMemStats(&ms)
					// Signed diff: GC may shrink HeapAlloc between probes.
					if int64(ms.HeapAlloc)-int64(baseline.HeapAlloc) > limit {
						vm.Interrupt("memory limit")
						return
					}
				case <-done:
					return
				}
			}
		}()
	}

	if _, err := vm.RunProgram(prog); err != nil {
		var iErr *goja.InterruptedError
		if errors.As(err, &iErr) {
			switch v := iErr.Value().(type) {
			case string:
				if v == "memory limit" {
					return out.String(), fmt.Errorf("script exceeded memory limit")
				}
				return out.String(), fmt.Errorf("script %s", v)
			default:
				return out.String(), fmt.Errorf("script interrupted")
			}
		}
		return out.String(), err
	}

	return out.String(), nil
}

// jsFormatWrapper wraps user code in a loop that iterates _SCRIPT_DATA and
// exposes `record` per iteration, mirroring pythonScriptWrapper.
func jsFormatWrapper(userCode string) string {
	var sb strings.Builder
	sb.WriteString("(function(){\n")
	sb.WriteString("  var _data = _SCRIPT_DATA ? JSON.parse(_SCRIPT_DATA) : [];\n")
	sb.WriteString("  if (!Array.isArray(_data)) _data = [_data];\n")
	sb.WriteString("  for (var _i = 0; _i < _data.length; _i++) {\n")
	sb.WriteString("    var record = _data[_i];\n")
	sb.WriteString("    (function(){\n")
	sb.WriteString(userCode)
	sb.WriteString("\n    }).call(this);\n")
	sb.WriteString("  }\n")
	sb.WriteString("})();\n")
	return sb.String()
}

// jsAccessRoot returns the effective filesystem ceiling for JavaScript
// file-access helpers (listdir, readFile, readFileHead). JS scripts may
// read files anywhere from docRoot up to maxParentLevels directories
// above it — matching the scope of YAML name resolution, so a script
// can read the same shared files (e.g. www/fontawesome.yaml) that the
// YAML resolver can. A maxParentLevels value of -1 means unlimited
// (filesystem root); 0 limits access to docRoot itself.
func jsAccessRoot(docRoot string, maxParentLevels int) string {
	if docRoot == "" {
		return ""
	}
	if maxParentLevels < 0 {
		return string(filepath.Separator)
	}
	root := docRoot
	for i := 0; i < maxParentLevels; i++ {
		parent := filepath.Dir(root)
		if parent == root {
			break
		}
		root = parent
	}
	return root
}

// resolveUnderRoot resolves a script-supplied path against docRoot and
// rejects anything that escapes the root, including via symlinks. Returns
// the absolute, symlink-resolved path on success.
func resolveUnderRoot(docRoot, p string) (string, error) {
	if docRoot == "" {
		return "", errors.New("file access disabled: no docRoot")
	}
	rootAbs, err := filepath.Abs(docRoot)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(rootAbs); err == nil {
		rootAbs = resolved
	}
	// Resolve user path relative to docRoot when not absolute.
	full := p
	if !filepath.IsAbs(full) {
		full = filepath.Join(rootAbs, full)
	}
	full = filepath.Clean(full)
	// EvalSymlinks fails for missing files; in that case fall back to
	// containment check on the cleaned path so that the caller's open
	// or stat returns a normal "not found" error.
	if resolved, err := filepath.EvalSymlinks(full); err == nil {
		full = resolved
	}
	rel, err := filepath.Rel(rootAbs, full)
	if err != nil {
		return "", fmt.Errorf("path outside docRoot")
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path outside docRoot")
	}
	return full, nil
}

// envListToMap converts a []string of KEY=VALUE entries (as produced by
// buildScriptEnv) into a map for exposure to JS as `env.KEY`.
func envListToMap(envList []string) map[string]string {
	m := make(map[string]string, len(envList))
	for _, e := range envList {
		if i := strings.IndexByte(e, '='); i > 0 {
			m[e[:i]] = e[i+1:]
		}
	}
	return m
}
