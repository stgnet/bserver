package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// dataDef describes a data source definition (from $name keys in YAML).
// A data source runs a script whose JSON output becomes the content definition
// for the name. This is distinct from format definitions (^name) which control
// how content is rendered — data sources control what data is available.
type dataDef struct {
	Script string // script language: "python", "javascript", "php"
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
		filePath := filepath.Join(ctx.docRoot, dd.File)
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
	interpreter := findScriptInterpreter(lang)
	if interpreter == "" {
		return nil, fmt.Errorf("interpreter not found for %s", dd.Script)
	}

	// Determine interpreter flag for inline code
	var flag string
	switch lang {
	case "python", "python3":
		flag = "-c"
	case "javascript", "js", "node":
		flag = "-e"
	case "php":
		flag = "-r"
	default:
		return nil, fmt.Errorf("unsupported script language: %s", dd.Script)
	}

	cmd := exec.Command(interpreter, flag, code)
	cmd.Dir = ctx.requestDir
	cmd.Env = ctx.buildScriptEnv("")
	cmd.Env = append(cmd.Env, "REQUEST_DIR="+ctx.requestDir)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		if err != nil {
			return nil, fmt.Errorf("script error: %v: %s", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		return nil, fmt.Errorf("script timeout (30s)")
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
