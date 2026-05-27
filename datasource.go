package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"sort"
	"strings"
	"syscall"
	"time"
)

// dataDef describes a data source definition (from $name keys in YAML).
// A data source runs a script whose JSON output becomes the content definition
// for the name. This is distinct from format definitions (^name) which control
// how content is rendered — data sources control what data is available.
type dataDef struct {
	Script string // script language: "python", "javascript", "php", "sh"
	Code   string // inline script code
	File   string // script file to load code from (relative to docRoot)
}

// parseDataDef parses an @name value into a dataDef struct.
func parseDataDef(v interface{}) *dataDef {
	m, ok := v.(*OrderedMap)
	if !ok {
		return nil
	}
	dd := &dataDef{}
	if scriptVal, ok := m.Get("script"); ok {
		if s, ok := scriptVal.(string); ok {
			dd.Script = s
		}
	}
	if codeVal, ok := m.Get("code"); ok {
		if s, ok := codeVal.(string); ok {
			dd.Code = s
		}
	}
	if fileVal, ok := m.Get("file"); ok {
		if s, ok := fileVal.(string); ok {
			dd.File = s
		}
	}
	return dd
}

// executeDataSource runs a data source script and returns the parsed JSON output
// as bserver content (using OrderedMap for objects to preserve key order).
// The script runs with CWD set to requestDir, so it can scan the "current" directory.
func (ctx *renderContext) executeDataSource(name string, dd *dataDef) (interface{}, error) {
	code := dd.Code
	if code == "" && dd.File != "" {
		filePath, err := resolveUnderRoot(ctx.docRoot, dd.File)
		if err != nil {
			return nil, fmt.Errorf("data source file %s rejected: %w", dd.File, err)
		}
		fileData, err := os.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("cannot read data source file %s: %w", dd.File, err)
		}
		code = string(fileData)
	}
	if code == "" {
		return nil, fmt.Errorf("data source %q has no code or file", name)
	}

	lang := strings.ToLower(dd.Script)

	// Embedded JS (goja) path — no fork; runs in-process. All JS aliases
	// route here; there is no external-node fallback.
	if lang == "javascript" || lang == "js" || lang == "node" {
		envList := ctx.buildScriptEnv("")
		envList = append(envList, "REQUEST_DIR="+ctx.requestDir)
		envMap := envListToMap(envList)
		output, err := runJS(code, envMap, nil, false, jsAccessRoot(ctx.docRoot, ctx.maxParentLevels))
		if err != nil {
			return nil, fmt.Errorf("script error: %w", err)
		}
		output = strings.TrimSpace(output)
		if output == "" {
			return nil, fmt.Errorf("data source %q produced no output", name)
		}
		var result interface{}
		if err := json.Unmarshal([]byte(output), &result); err != nil {
			log.Printf("data source %q JSON parse error: %v (output: %s)", name, err, output)
			return nil, fmt.Errorf("JSON parse error: %w", err)
		}
		return jsonToOrdered(result), nil
	}

	interpreter := findScriptInterpreter(lang)
	if interpreter == "" {
		return nil, fmt.Errorf("interpreter not found for %s", dd.Script)
	}

	// Determine interpreter flag for inline code
	var flag string
	switch lang {
	case "python", "python3":
		flag = "-c"
	case "php":
		flag = "-r"
	case "sh", "bash", "shell":
		flag = "-c"
	default:
		return nil, fmt.Errorf("unsupported script language: %s", dd.Script)
	}

	execCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(execCtx, interpreter, flag, code)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		// Kill the entire process group so any child processes (e.g. shell
		// subprocesses, python multiprocessing workers) are reaped too.
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return os.ErrProcessDone
	}
	cmd.Dir = ctx.requestDir
	cmd.Env = ctx.buildScriptEnv("")
	cmd.Env = append(cmd.Env, "REQUEST_DIR="+ctx.requestDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &cappedWriter{w: &stdout, limit: maxScriptOutputBytes}
	cmd.Stderr = &cappedWriter{w: &stderr, limit: maxScriptOutputBytes}

	runErr := cmd.Run()
	if errors.Is(execCtx.Err(), context.DeadlineExceeded) {
		return nil, fmt.Errorf("script timeout (30s)")
	}
	if runErr != nil {
		return nil, fmt.Errorf("script error: %v: %s", runErr, stderr.String())
	}

	output := strings.TrimSpace(stdout.String())
	if output == "" {
		return nil, fmt.Errorf("data source %q produced no output", name)
	}

	var result interface{}
	if err := json.Unmarshal([]byte(output), &result); err != nil {
		log.Printf("data source %q JSON parse error: %v (output: %s)", name, err, output)
		return nil, fmt.Errorf("JSON parse error: %w", err)
	}

	return jsonToOrdered(result), nil
}

// jsonToOrdered recursively converts JSON-decoded values (which use
// map[string]interface{} for objects) into bserver's OrderedMap representation,
// preserving compatibility with the rendering pipeline.
func jsonToOrdered(v interface{}) interface{} {
	switch val := v.(type) {
	case map[string]interface{}:
		om := NewOrderedMap()
		// Sort keys for deterministic output
		keys := make([]string, 0, len(val))
		for k := range val {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			om.Set(k, jsonToOrdered(val[k]))
		}
		return om
	case []interface{}:
		for i, item := range val {
			val[i] = jsonToOrdered(item)
		}
		return val
	default:
		// strings, float64, bool, nil pass through unchanged
		return v
	}
}
