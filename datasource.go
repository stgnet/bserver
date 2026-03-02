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
	"strconv"
	"strings"
	"time"
)

// dataDef describes a data source definition (from $name keys in YAML).
// A data source runs a script whose JSON output becomes the content definition
// for the name. This is distinct from format definitions (^name) which control
// how content is rendered — data sources control what data is available.
type dataDef struct {
	Script  string      // script language: "python", "javascript", "php", "sh"
	Code    string      // inline script code
	File    string      // script file to load code from (relative to docRoot)
	Where   *OrderedMap // exact-match filters on list items
	Sort    string      // sort key for list of objects
	Order   string      // asc/desc
	Limit   int         // max number of items
	Offset  int         // list offset
	Page    int         // 1-based page index
	PerPage int         // items per page
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
	if whereVal, ok := m.Get("where"); ok {
		if wm, ok := whereVal.(*OrderedMap); ok {
			dd.Where = wm
		}
	}
	if sortVal, ok := m.Get("sort"); ok {
		if s, ok := sortVal.(string); ok {
			dd.Sort = s
		}
	}
	if orderVal, ok := m.Get("order"); ok {
		if s, ok := orderVal.(string); ok {
			dd.Order = strings.ToLower(s)
		}
	}
	if limitVal, ok := m.Get("limit"); ok {
		dd.Limit = parseInt(limitVal)
	}
	if offsetVal, ok := m.Get("offset"); ok {
		dd.Offset = parseInt(offsetVal)
	}
	if pageVal, ok := m.Get("page"); ok {
		dd.Page = parseInt(pageVal)
	}
	if ppVal, ok := m.Get("per-page"); ok {
		dd.PerPage = parseInt(ppVal)
	}
	return dd
}

func parseInt(v interface{}) int {
	s := fmt.Sprintf("%v", v)
	i, _ := strconv.Atoi(s)
	return i
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
	case "sh", "bash", "shell":
		flag = "-c"
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

	return applyDataPipeline(jsonToOrdered(result), dd), nil
}

func applyDataPipeline(v interface{}, dd *dataDef) interface{} {
	items, ok := v.([]interface{})
	if !ok {
		return v
	}

	if dd.Where != nil && dd.Where.Len() > 0 {
		filtered := make([]interface{}, 0, len(items))
		for _, it := range items {
			om, ok := it.(*OrderedMap)
			if !ok {
				continue
			}
			match := true
			dd.Where.Range(func(k string, expected interface{}) bool {
				actual, exists := om.Get(k)
				if !exists || fmt.Sprintf("%v", actual) != fmt.Sprintf("%v", expected) {
					match = false
					return false
				}
				return true
			})
			if match {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}

	if dd.Sort != "" {
		sort.SliceStable(items, func(i, j int) bool {
			a, aok := items[i].(*OrderedMap)
			b, bok := items[j].(*OrderedMap)
			if !aok || !bok {
				return false
			}
			av, _ := a.Get(dd.Sort)
			bv, _ := b.Get(dd.Sort)
			if dd.Order == "desc" {
				return fmt.Sprintf("%v", av) > fmt.Sprintf("%v", bv)
			}
			return fmt.Sprintf("%v", av) < fmt.Sprintf("%v", bv)
		})
	}

	if dd.PerPage > 0 && dd.Page > 0 {
		dd.Offset = (dd.Page - 1) * dd.PerPage
		dd.Limit = dd.PerPage
	}
	start := dd.Offset
	if start < 0 {
		start = 0
	}
	if start > len(items) {
		start = len(items)
	}
	end := len(items)
	if dd.Limit > 0 && start+dd.Limit < end {
		end = start + dd.Limit
	}
	return items[start:end]
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
