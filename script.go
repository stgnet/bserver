package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// buildScriptEnv builds the environment variables for script execution.
// Only essential variables are passed — the full server environment is NOT
// inherited, to avoid leaking secrets (API keys, database passwords, etc.)
// to user-authored scripts.
func (ctx *renderContext) buildScriptEnv(scriptFile string) []string {
	env := []string{
		"REQUEST_URI=" + ctx.requestURI,
		"DOCUMENT_ROOT=" + ctx.docRoot,
		"REDIRECT_STATUS=200",
	}

	// Inherit only PATH so interpreters can find shared libraries
	if p := os.Getenv("PATH"); p != "" {
		env = append(env, "PATH="+p)
	}
	// Inherit HOME for interpreters that need it (e.g., pip cache)
	if h := os.Getenv("HOME"); h != "" {
		env = append(env, "HOME="+h)
	}

	if scriptFile != "" {
		env = append(env, "SCRIPT_FILENAME="+scriptFile)
	}
	env = append(env, "SCRIPT_NAME="+ctx.requestURI)
	env = append(env, "PHP_SELF="+ctx.requestURI)

	r := ctx.httpRequest
	if r == nil {
		return env
	}

	host := r.Host
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	port := "80"
	if r.TLS != nil {
		port = "443"
	}
	remoteAddr := r.RemoteAddr
	if h, _, err := net.SplitHostPort(remoteAddr); err == nil {
		remoteAddr = h
	}

	// Try to get server's own address from the connection
	serverAddr := host
	if addr, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr); ok {
		if h, _, err := net.SplitHostPort(addr.String()); err == nil {
			serverAddr = h
		} else {
			serverAddr = addr.String()
		}
	}

	env = append(env,
		"GATEWAY_INTERFACE=CGI/1.1",
		"SERVER_SOFTWARE=bserver",
		"SERVER_PROTOCOL="+r.Proto,
		"SERVER_NAME="+host,
		"SERVER_ADDR="+serverAddr,
		"SERVER_PORT="+port,
		"REQUEST_METHOD="+r.Method,
		"QUERY_STRING="+r.URL.RawQuery,
		"REMOTE_ADDR="+remoteAddr,
		"HTTP_HOST="+r.Host,
	)

	// Forward HTTP headers as HTTP_* variables
	for key, vals := range r.Header {
		envKey := "HTTP_" + strings.ReplaceAll(strings.ToUpper(key), "-", "_")
		env = append(env, envKey+"="+strings.Join(vals, ", "))
	}
	if ct := r.Header.Get("Content-Type"); ct != "" {
		env = append(env, "CONTENT_TYPE="+ct)
	}
	if r.ContentLength >= 0 {
		env = append(env, fmt.Sprintf("CONTENT_LENGTH=%d", r.ContentLength))
	}

	return env
}

// renderScript executes a script (python, javascript, php) to render data records.
// The script's `code` is wrapped in a per-language boilerplate that:
//   - Reads all records as JSON from stdin
//   - Iterates with `record` variable set to each record
//   - Collects stdout as the rendered HTML
//
func (ctx *renderContext) renderScript(fd *formatDef, data interface{}) string {
	// Convert OrderedMap to a list of {key, value} records for script iteration,
	// matching the $key/$value convention used by renderIterated.
	scriptData := data
	if om, ok := data.(*OrderedMap); ok {
		var records []map[string]string
		om.Range(func(k string, v interface{}) bool {
			records = append(records, map[string]string{
				"key":   k,
				"value": fmt.Sprintf("%v", v),
			})
			return true
		})
		scriptData = records
	}

	// Serialize data as JSON for the script
	jsonData, err := json.Marshal(scriptData)
	if err != nil {
		return fmt.Sprintf("<!-- script: json error: %v -->\n", err)
	}

	code := fd.Code
	if code == "" && fd.File != "" {
		// Load code from file (relative to docRoot)
		filePath := filepath.Join(ctx.docRoot, fd.File)
		fileData, err := os.ReadFile(filePath)
		if err != nil {
			return fmt.Sprintf("<!-- script: error reading %s: %v -->\n", fd.File, err)
		}
		code = string(fileData)
		// Strip PHP open/close tags since the wrapper already provides context
		code = strings.TrimSpace(code)
		if strings.HasPrefix(code, "<?php") {
			code = strings.TrimPrefix(code, "<?php")
		}
		if strings.HasSuffix(code, "?>") {
			code = strings.TrimSuffix(code, "?>")
		}
	}
	if code == "" {
		return "<!-- script: no code or file provided -->\n"
	}

	// Determine interpreter and wrap user code
	var interpreter, flag, wrappedCode string
	lang := strings.ToLower(fd.Script)
	switch lang {
	case "python", "python3":
		interpreter = findScriptInterpreter("python")
		flag = "-c"
		wrappedCode = pythonScriptWrapper(code)
	case "javascript", "js", "node":
		interpreter = findScriptInterpreter("node")
		flag = "-e"
		wrappedCode = jsScriptWrapper(code)
	case "php":
		interpreter = findScriptInterpreter("php")
		flag = "-r"
		wrappedCode = phpScriptWrapper(code)
	default:
		return fmt.Sprintf("<!-- unknown script language: %s -->\n", fd.Script)
	}

	if interpreter == "" {
		return fmt.Sprintf("<!-- %s interpreter not found -->\n", fd.Script)
	}

	// Execute with timeout, CWD set to docRoot for file resolution
	cmd := exec.Command(interpreter, flag, wrappedCode)
	cmd.Dir = ctx.docRoot
	scriptFile := ""
	if fd.File != "" {
		scriptFile = filepath.Join(ctx.docRoot, fd.File)
	}
	cmd.Env = ctx.buildScriptEnv(scriptFile)
	cmd.Stdin = bytes.NewReader(jsonData)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	done := make(chan error, 1)
	go func() { done <- cmd.Run() }()
	select {
	case err := <-done:
		if err != nil {
			return fmt.Sprintf("<!-- script error: %v: %s -->\n", err, stderr.String())
		}
	case <-time.After(30 * time.Second):
		cmd.Process.Kill()
		return "<!-- script timeout (30s) -->\n"
	}

	return stdout.String()
}

// findScriptInterpreter locates a script language interpreter.
func findScriptInterpreter(lang string) string {
	switch lang {
	case "python":
		if p, err := exec.LookPath("python3"); err == nil {
			return p
		}
		if p, err := exec.LookPath("python"); err == nil {
			return p
		}
	case "node":
		if p, err := exec.LookPath("node"); err == nil {
			return p
		}
	case "php":
		if p, err := exec.LookPath("php"); err == nil {
			return p
		}
	}
	return ""
}

// pythonScriptWrapper wraps user code in a Python loop over JSON records.
// The user code has `record` (a dict) available for each iteration.
func pythonScriptWrapper(userCode string) string {
	var sb strings.Builder
	sb.WriteString("import json, sys\n")
	sb.WriteString("_data = json.loads(sys.stdin.read())\n")
	sb.WriteString("if not isinstance(_data, list): _data = [_data]\n")
	sb.WriteString("for record in _data:\n")
	// Indent user code by 4 spaces to be inside the for loop
	for _, line := range strings.Split(userCode, "\n") {
		if strings.TrimSpace(line) == "" {
			sb.WriteString("\n")
		} else {
			sb.WriteString("    " + line + "\n")
		}
	}
	return sb.String()
}

// jsScriptWrapper wraps user code in a JavaScript loop over JSON records.
// The user code has `record` (an object) available for each iteration.
func jsScriptWrapper(userCode string) string {
	var sb strings.Builder
	sb.WriteString("const _data = JSON.parse(require('fs').readFileSync(0, 'utf8'));\n")
	sb.WriteString("const _records = Array.isArray(_data) ? _data : [_data];\n")
	sb.WriteString("for (const record of _records) {\n")
	sb.WriteString(userCode)
	sb.WriteString("\n}\n")
	return sb.String()
}

// phpScriptWrapper wraps user code in a PHP loop over JSON records.
// The user code has $record (an associative array) available for each iteration.
func phpScriptWrapper(userCode string) string {
	var sb strings.Builder
	sb.WriteString("$_data = json_decode(file_get_contents('php://stdin'), true);\n")
	sb.WriteString("if (!is_array($_data)) $_data = [$_data];\n")
	sb.WriteString("foreach ($_data as $record) {\n")
	sb.WriteString(userCode)
	sb.WriteString("\n}\n")
	return sb.String()
}
