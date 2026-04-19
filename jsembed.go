package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/dop251/goja"
)

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
// Host builtins available to all scripts:
//
//	env        — object: env.VAR returns "" if unset
//	print(...) — appends a space-joined line to captured output
//	listdir(p) — array of entry names (sorted order not guaranteed)
//	readFileHead(p, n) — first n bytes of file p as string
//	readFile(p) — entire file contents as string
//	joinPath(a, b, ...) — filepath.Join wrapper
//	splitExt(name)      — [basename, ext] pair (ext includes leading dot)
func runJS(source string, envMap map[string]string, dataJSON []byte, wrap bool) (string, error) {
	final := source
	if wrap {
		final = jsFormatWrapper(source)
	}
	prog, err := compileJS(final)
	if err != nil {
		return "", fmt.Errorf("compile: %w", err)
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
	_ = vm.Set("listdir", func(path string) ([]string, error) {
		entries, err := os.ReadDir(path)
		if err != nil {
			return nil, err
		}
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		return names, nil
	})
	_ = vm.Set("readFileHead", func(path string, n int) (string, error) {
		if n <= 0 {
			return "", nil
		}
		f, err := os.Open(path)
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
	_ = vm.Set("readFile", func(path string) (string, error) {
		data, err := os.ReadFile(path)
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

	if _, err := vm.RunProgram(prog); err != nil {
		var iErr *goja.InterruptedError
		if errors.As(err, &iErr) {
			return out.String(), fmt.Errorf("script timeout (30s)")
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
