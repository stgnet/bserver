package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"io"
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

	// Buffer the POST body once (r.Body can only be read once) and pass it
	// via an environment variable so script wrappers can populate $_POST etc.
	if !ctx.postBodyRead {
		ctx.postBodyRead = true
		if r.Body != nil {
			body, err := io.ReadAll(r.Body)
			if err == nil {
				ctx.postBody = body
			}
			r.Body.Close()
		}
	}
	if len(ctx.postBody) > 0 {
		env = append(env, fmt.Sprintf("CONTENT_LENGTH=%d", len(ctx.postBody)))
		env = append(env, "_POST_DATA="+string(ctx.postBody))
	} else if r.ContentLength >= 0 {
		env = append(env, fmt.Sprintf("CONTENT_LENGTH=%d", r.ContentLength))
	}

	return env
}

// renderScript executes a script (python, javascript, php, sh) to render data records.
// The script's `code` is wrapped in a per-language boilerplate that:
//   - Reads all records as JSON from stdin
//   - Iterates with `record` variable set to each record
//   - Collects stdout as the rendered HTML
//
func (ctx *renderContext) renderScript(fd *formatDef, data interface{}) string {
	// Convert OrderedMap to a list of {key, value} records for script iteration,
	// matching the $key/$value convention used by renderIterated.
	scriptData := data
	if data == nil {
		// No data — send an empty list so the script loop runs zero times
		// instead of crashing on a null record.
		scriptData = []interface{}{}
	} else if om, ok := data.(*OrderedMap); ok {
		var records []map[string]interface{}
		om.Range(func(k string, v interface{}) bool {
			records = append(records, map[string]interface{}{
				"key":   k,
				"value": ctx.preRenderValue(v),
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
		// No explicit code or file — if content is a string, use it as
		// the script code itself.  This supports format definitions like
		// ^php: { script: php } where the content provides the code.
		if str, ok := data.(string); ok && str != "" {
			code = strings.TrimSpace(str)
			if strings.HasPrefix(code, "<?php") {
				code = strings.TrimPrefix(code, "<?php")
			}
			if strings.HasSuffix(code, "?>") {
				code = strings.TrimSuffix(code, "?>")
			}
			code = strings.TrimSpace(code)
			// Single-element array so the foreach wrapper runs once
			jsonData = []byte("[null]")
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
	case "sh", "bash", "shell":
		interpreter = findScriptInterpreter("sh")
		flag = "-c"
		wrappedCode = shScriptWrapper(code)
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
	} else if ctx.sourceFile != "" {
		// For inline scripts, SCRIPT_FILENAME points to the page's
		// primary source file (e.g., index.yaml, page.md) so embedded
		// scripts can discover which file generated the current page.
		scriptFile = ctx.sourceFile
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
	case "sh", "bash", "shell":
		if p, err := exec.LookPath("bash"); err == nil {
			return p
		}
		if p, err := exec.LookPath("sh"); err == nil {
			return p
		}
	}
	return ""
}

// preRenderValue recursively walks a value and renders any list to HTML via
// renderListToHTML, wrapping the result as {"_html": "..."}.  Nested
// OrderedMaps (dropdown menus) are walked so that list values inside them
// are also rendered.
func (ctx *renderContext) preRenderValue(v interface{}) interface{} {
	switch val := v.(type) {
	case []interface{}:
		return map[string]interface{}{"_html": ctx.renderListToHTML(val)}
	case *OrderedMap:
		result := NewOrderedMap()
		val.Range(func(k string, inner interface{}) bool {
			result.Set(k, ctx.preRenderValue(inner))
			return true
		})
		return result
	default:
		return v
	}
}

// renderListToHTML renders a list of content elements to a single HTML string.
// Format definition references (^name) are rendered through bserver's format
// system; plain text strings are HTML-escaped. Elements are concatenated
// directly (no separator) to match renderContent's list behavior, and their
// order is preserved from the YAML source.
func (ctx *renderContext) renderListToHTML(list []interface{}) string {
	var sb strings.Builder
	for _, elem := range list {
		if s, ok := elem.(string); ok {
			tag, fd := ctx.tagForName(s)
			if tag != "" && fd != nil {
				ctx.renderInlineTag(&sb, s, tag, fd, nil, 0)
				continue
			}
			sb.WriteString(html.EscapeString(s))
			continue
		}
		sb.WriteString(html.EscapeString(fmt.Sprintf("%v", elem)))
	}
	return strings.TrimSpace(sb.String())
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

// shScriptWrapper wraps user code in a shell loop over JSON records.
// Each iteration sets $RECORD to the JSON representation of the current record.
// If jq is available, individual fields are also exported as $RECORD_<KEY>.
func shScriptWrapper(userCode string) string {
	var sb strings.Builder
	sb.WriteString("_INPUT=$(cat)\n")
	sb.WriteString("_COUNT=$(printf '%s' \"$_INPUT\" | jq -r 'if type==\"array\" then length else 1 end' 2>/dev/null || echo 1)\n")
	sb.WriteString("_IDX=0\n")
	sb.WriteString("while [ \"$_IDX\" -lt \"$_COUNT\" ]; do\n")
	sb.WriteString("  RECORD=$(printf '%s' \"$_INPUT\" | jq -c \"if type==\\\"array\\\" then .[${_IDX}] else . end\" 2>/dev/null || printf '%s' \"$_INPUT\")\n")
	sb.WriteString("  export RECORD\n")
	sb.WriteString(userCode)
	sb.WriteString("\n  _IDX=$((_IDX + 1))\n")
	sb.WriteString("done\n")
	return sb.String()
}

// phpScriptWrapper wraps user code in a PHP loop over JSON records.
// The user code has $record (an associative array) available for each iteration.
// PHP CLI mode doesn't auto-populate $_GET/$_POST/$_SERVER from CGI env vars,
// so we parse them manually from the environment.
func phpScriptWrapper(userCode string) string {
	var sb strings.Builder
	// Populate $_SERVER from CGI environment variables
	sb.WriteString("foreach (['REQUEST_METHOD','REQUEST_URI','QUERY_STRING','CONTENT_TYPE','CONTENT_LENGTH','DOCUMENT_ROOT','SCRIPT_FILENAME','SCRIPT_NAME','PHP_SELF','SERVER_NAME','SERVER_PORT','SERVER_PROTOCOL','SERVER_SOFTWARE','GATEWAY_INTERFACE','REMOTE_ADDR','HTTP_HOST','REDIRECT_STATUS','SERVER_ADDR','PATH_INFO'] as $_k) { $_v = getenv($_k); if ($_v !== false) $_SERVER[$_k] = $_v; }\n")
	sb.WriteString("foreach ($_SERVER as $_k => $_v) { if (strpos($_k, 'HTTP_') === 0) $_SERVER[$_k] = $_v; }\n")
	// Populate $_GET from QUERY_STRING
	sb.WriteString("parse_str(getenv('QUERY_STRING') ?: '', $_GET);\n")
	// Populate $_POST from _POST_DATA env var (body passed by bserver)
	sb.WriteString("$_POST = []; $_postData = getenv('_POST_DATA'); if ($_postData !== false && getenv('REQUEST_METHOD') === 'POST') { $_ct = getenv('CONTENT_TYPE') ?: ''; if (stripos($_ct, 'application/x-www-form-urlencoded') !== false) { parse_str($_postData, $_POST); } elseif (stripos($_ct, 'application/json') !== false) { $_POST = json_decode($_postData, true) ?: []; } }\n")
	// Populate $_REQUEST from merged GET+POST
	sb.WriteString("$_REQUEST = array_merge($_GET, $_POST);\n")
	sb.WriteString("$_data = json_decode(file_get_contents('php://stdin'), true);\n")
	sb.WriteString("if (!is_array($_data)) $_data = [$_data];\n")
	sb.WriteString("foreach ($_data as $record) {\n")
	sb.WriteString(userCode)
	sb.WriteString("\n}\n")
	return sb.String()
}
