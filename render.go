package main

import (
	"bytes"
	"fmt"
	"html"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	goldmarkhtml "github.com/yuin/goldmark/renderer/html"
)

// mdRenderer is a shared goldmark instance with unsafe HTML enabled,
// so inline HTML in markdown (links, images, iframes) passes through.
var mdRenderer = goldmark.New(
	goldmark.WithExtensions(
		extension.Table,
	),
	goldmark.WithRendererOptions(
		goldmarkhtml.WithUnsafe(),
	),
)

// rawHTML is pre-rendered HTML content that should be output without escaping.
type rawHTML string

// indentSpacer is the string used for each level of HTML indentation.
var indentSpacer = "  "

// maxInlineTagLength is the maximum total line length (including indentation)
// for collapsing a tag and its content onto a single line. When the opening
// tag, inner content, and closing tag fit within this limit, they are rendered
// on one line (e.g., <title>My Page</title>) instead of three.
var maxInlineTagLength = 76

// voidElements are HTML tags that must not have a closing tag.
var voidElements = map[string]bool{
	"meta": true, "link": true, "br": true, "hr": true, "img": true,
	"input": true, "source": true, "area": true, "base": true,
	"col": true, "embed": true, "track": true, "wbr": true,
}

// knownHTMLTags is the set of HTML tags recognized by the renderer.
var knownHTMLTags = map[string]bool{
	"html": true, "head": true, "body": true, "title": true,
	"meta": true, "link": true, "style": true, "script": true,
	"div": true, "span": true, "p": true, "br": true, "hr": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
	"a": true, "img": true, "ul": true, "ol": true, "li": true,
	"table": true, "tr": true, "td": true, "th": true, "thead": true, "tbody": true,
	"form": true, "input": true, "button": true, "textarea": true, "select": true, "option": true,
	"header": true, "footer": true, "nav": true, "main": true, "section": true, "article": true, "aside": true,
	"strong": true, "em": true, "b": true, "i": true, "u": true, "small": true,
	"pre": true, "code": true, "blockquote": true,
	"label": true, "fieldset": true, "legend": true,
	"video": true, "audio": true, "source": true, "canvas": true,
	"details": true, "summary": true,
}

// maxRenderDepth is the maximum recursion depth for rendering and resolution.
// This prevents infinite loops from circular references or deeply nested content.
const maxRenderDepth = 50

// DefaultMaxParentLevels is how many directory levels above docRoot the
// upward YAML search is allowed to traverse. This prevents the renderer
// from reading arbitrary files higher up in the filesystem.
// Set to -1 for unlimited (use with caution).
const DefaultMaxParentLevels = 1

// renderContext holds the state for rendering a single page request.
type renderContext struct {
	docRoot         string                 // virtual host root directory
	requestDir      string                 // directory of the requested path (for upward search)
	requestURI      string                 // URL path for this request (e.g., "/service", "/")
	httpRequest     *http.Request          // original HTTP request (nil in tests)
	maxParentLevels int                    // how many levels above docRoot to search (-1 = unlimited)
	defs            map[string]interface{} // content definitions (name -> yaml value)
	formats         map[string]*formatDef  // format definitions (^name -> formatDef)
	dataSources     map[string]*dataDef    // data source definitions ($name -> dataDef)
	filesLoaded     map[string]bool        // yaml files already loaded (prevent re-loading)
	yamlErrors      map[string]string      // yaml files that failed to parse (path -> error message)
	resolving       map[string]bool        // cycle detection
	debug           bool                   // emit HTML comments tracing resolution
}

// renderYAMLPage is the entry point: given a request for a path within docRoot,
// produce a complete HTML page. Returns the HTML and the list of source files
// loaded during rendering (for cache dependency tracking).
func renderYAMLPage(docRoot, reqPath string, debug bool, maxParentLevels int, r *http.Request) (string, []string) {
	ctx := &renderContext{
		docRoot:         docRoot,
		requestDir:      reqPath,
		requestURI:      computeRequestURI(docRoot, reqPath),
		httpRequest:     r,
		maxParentLevels: maxParentLevels,
		defs:            make(map[string]interface{}),
		formats:         make(map[string]*formatDef),
		dataSources:     make(map[string]*dataDef),
		filesLoaded:     make(map[string]bool),
		yamlErrors:      make(map[string]string),
		resolving:       make(map[string]bool),
		debug:           debug,
	}

	// Determine the request directory and optionally pre-load a specific yaml file.
	info, err := os.Stat(reqPath)
	if err == nil && info.IsDir() {
		ctx.requestDir = reqPath
	} else {
		ctx.requestDir = filepath.Dir(reqPath)
		if strings.HasSuffix(strings.ToLower(reqPath), ".yaml") {
			ctx.loadYAMLFile(reqPath)
		}
	}

	// Save page-level format overrides. The page file is loaded first, but
	// resolveAll may load html.yaml and other files whose ^name definitions
	// would overwrite the page's. Re-applying after resolution ensures
	// page-level formats take precedence.
	pageFormats := make(map[string]*formatDef)
	for k, v := range ctx.formats {
		pageFormats[k] = v
	}

	// Pass 1: resolve all names, loading all yaml files and applying +merges.
	// This ensures that features like +style from jumbo.yaml are applied
	// before style is rendered in pass 2.
	ctx.resolveAll("html", 0)

	// Re-apply page-level format overrides
	for k, v := range pageFormats {
		ctx.formats[k] = v
	}

	// Pass 2: render HTML now that all definitions are fully loaded.
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html>\n")
	ctx.renderName(&sb, "html", 0)
	return sb.String(), ctx.sourceFilesList()
}

// renderErrorPage renders an error page through the YAML rendering pipeline.
// It pre-seeds error-related definitions and looks for a specific error
// template (e.g., error404) first, then a generic "error" template.
// Returns empty string if no error template is found, allowing the caller
// to fall back to a plain-text response.
//
// Pre-seeded definitions available to error templates via $varname:
//
//	errornumber      — the HTTP status code as text (e.g., "404")
//	errordescription — the standard status text (e.g., "Not Found")
//	errortitle       — "404 — Not Found" (plain text for use in tags like h1: $errortitle)
//	errormessage     — detail message (plain text for use in tags like p: $errormessage)
func renderErrorPage(docRoot string, statusCode int, message string, debug bool, maxParentLevels int, r *http.Request) (string, []string) {
	requestURI := "/"
	if r != nil && r.URL != nil {
		requestURI = r.URL.Path
	}
	ctx := &renderContext{
		docRoot:         docRoot,
		requestDir:      docRoot,
		requestURI:      requestURI,
		httpRequest:     r,
		maxParentLevels: maxParentLevels,
		defs:            make(map[string]interface{}),
		formats:         make(map[string]*formatDef),
		dataSources:     make(map[string]*dataDef),
		filesLoaded:     make(map[string]bool),
		yamlErrors:      make(map[string]string),
		resolving:       make(map[string]bool),
		debug:           debug,
	}

	// Pre-seed error information as named definitions.
	// These are available in format definitions via $varname substitution,
	// e.g., h1: $errortitle in a ^error content list.
	description := http.StatusText(statusCode)
	if description == "" {
		description = "Error"
	}
	msgText := message
	if msgText == "" {
		msgText = fmt.Sprintf("The requested page %q was not found.", requestURI)
	}

	ctx.defs["errornumber"] = rawHTML(fmt.Sprintf("%d", statusCode))
	ctx.defs["errordescription"] = rawHTML(html.EscapeString(description))
	ctx.defs["errortitle"] = fmt.Sprintf("%d — %s", statusCode, description)
	ctx.defs["errormessage"] = msgText

	// Try specific error page first (e.g., error404), then generic error.
	// We must load the error template BEFORE resolveAll so the definitions
	// and formats are available during resolution.
	specificName := fmt.Sprintf("error%d", statusCode)
	ctx.findDefinition(specificName)
	if _, ok := ctx.defs[specificName]; ok {
		// Wrap in a list so the name is resolved as a reference, not literal text
		ctx.defs["main"] = []interface{}{specificName}
	} else {
		ctx.findDefinition("error")
		if _, ok := ctx.defs["error"]; ok {
			ctx.defs["main"] = []interface{}{"error"}
		} else {
			return "", nil // no error template found
		}
	}

	// Resolve the full page tree through html.yaml.
	ctx.resolveAll("html", 0)

	// Re-apply title override (title.yaml may have overwritten it during resolution).
	ctx.defs["title"] = rawHTML(fmt.Sprintf("%d %s", statusCode, description))

	// Render the page.
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html>\n")
	ctx.renderName(&sb, "html", 0)
	return sb.String(), ctx.sourceFilesList()
}

// renderMarkdownPage renders a markdown file within the full YAML page structure.
// The markdown content becomes the "main" definition, so it gets the same
// header, navbar, styles, footer, etc. as YAML pages.
func renderMarkdownPage(docRoot, mdPath string, debug bool, maxParentLevels int, r *http.Request) (string, []string) {
	ctx := &renderContext{
		docRoot:         docRoot,
		requestDir:      filepath.Dir(mdPath),
		requestURI:      computeRequestURI(docRoot, mdPath),
		httpRequest:     r,
		maxParentLevels: maxParentLevels,
		defs:            make(map[string]interface{}),
		formats:         make(map[string]*formatDef),
		dataSources:     make(map[string]*dataDef),
		filesLoaded:     make(map[string]bool),
		yamlErrors:      make(map[string]string),
		resolving:       make(map[string]bool),
		debug:           debug,
	}

	// Read and convert markdown to HTML.
	// Track the .md file itself as a source dependency.
	ctx.filesLoaded[mdPath] = true
	mdData, err := os.ReadFile(mdPath)
	if err != nil {
		return fmt.Sprintf("<!-- error reading %s: %v -->\n", mdPath, err), nil
	}
	var buf bytes.Buffer
	if err := mdRenderer.Convert(mdData, &buf); err != nil {
		return fmt.Sprintf("<!-- error converting markdown %s: %v -->\n", mdPath, err), nil
	}

	// Inject the rendered markdown as the "main" content.
	// Using rawHTML so it passes through without escaping.
	ctx.defs["main"] = rawHTML(buf.String())

	// Pass 1: resolve all names
	ctx.resolveAll("html", 0)

	// Pass 2: render HTML
	var sb strings.Builder
	sb.WriteString("<!DOCTYPE html>\n")
	ctx.renderName(&sb, "html", 0)
	return sb.String(), ctx.sourceFilesList()
}

// sourceFilesList returns the list of files loaded during rendering.
func (ctx *renderContext) sourceFilesList() []string {
	files := make([]string, 0, len(ctx.filesLoaded))
	for f := range ctx.filesLoaded {
		files = append(files, f)
	}
	return files
}

// processMarkup converts string content through a markup processor.
// Currently supports "markdown" using goldmark.
func (ctx *renderContext) processMarkup(markup, content string) string {
	switch strings.ToLower(markup) {
	case "markdown", "md":
		var buf bytes.Buffer
		if err := mdRenderer.Convert([]byte(content), &buf); err != nil {
			return fmt.Sprintf("<!-- markup error: %v -->\n", err)
		}
		return buf.String()
	default:
		return fmt.Sprintf("<!-- unknown markup language: %s -->\n", markup)
	}
}

// loadYAMLFile reads a yaml file and processes its keys:
//   - ^name keys go into formats
//   - +name keys merge into existing defs
//   - plain keys go into defs (first definition wins)
func (ctx *renderContext) loadYAMLFile(path string) {
	if ctx.filesLoaded[path] {
		return
	}
	ctx.filesLoaded[path] = true

	data, err := os.ReadFile(path)
	if err != nil {
		return
	}

	parsed, parseErr := parseYAMLOrdered(data)
	if parseErr != nil {
		log.Printf("YAML parse error in %s: %v", path, parseErr)
		ctx.yamlErrors[path] = parseErr.Error()
		return
	}

	// If the document is an OrderedMap, process as definitions
	if doc, ok := parsed.(*OrderedMap); ok {
		ctx.mergeDoc(doc)
		return
	}

	// If it's a list, store as data (e.g., packages.yaml)
	if list, ok := parsed.([]interface{}); ok {
		name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
		if _, exists := ctx.defs[name]; !exists {
			ctx.defs[name] = list
		}
	}
}

// yamlErrorForName checks if a YAML parse error may have caused a name to be
// undefined. First checks for an exact filename match (name.yaml had error),
// then falls back to returning any YAML error in the context — since a parse
// error in any loaded file (e.g., index.yaml) could prevent names like "main"
// from being defined.
func (ctx *renderContext) yamlErrorForName(name string) (string, string) {
	// Exact match: name.yaml had error
	target := name + ".yaml"
	for path, errMsg := range ctx.yamlErrors {
		if filepath.Base(path) == target {
			return target, errMsg
		}
	}
	// Any YAML error could be the cause
	for path, errMsg := range ctx.yamlErrors {
		return filepath.Base(path), errMsg
	}
	return "", ""
}

// mergeDoc processes a parsed YAML document's keys into defs, formats, and data sources.
func (ctx *renderContext) mergeDoc(doc *OrderedMap) {
	doc.Range(func(k string, v interface{}) bool {
		if strings.HasPrefix(k, "^") {
			// Format definition: ^name
			name := k[1:]
			ctx.formats[name] = parseFormatDef(v)
		} else if strings.HasPrefix(k, "$") {
			// Data source definition: $name
			name := k[1:]
			if dd := parseDataDef(v); dd != nil {
				ctx.dataSources[name] = dd
			}
		} else if strings.HasPrefix(k, "+") {
			// Merge definition: +name adds to existing
			name := k[1:]
			ctx.mergeDef(name, v)
		} else {
			// Content definition: first one wins (page-level overrides inherited)
			if _, exists := ctx.defs[k]; !exists {
				ctx.defs[k] = v
			}
		}
		return true
	})
}

// mergeDef merges value into an existing definition. For maps, keys are added/overridden.
// For lists, items are appended. If the base definition doesn't exist yet,
// try loading name.yaml first so we merge into the canonical definition.
func (ctx *renderContext) mergeDef(name string, value interface{}) {
	if _, exists := ctx.defs[name]; !exists {
		// Try to load the base definition first
		ctx.findDefinition(name)
	}

	existing, exists := ctx.defs[name]
	if !exists {
		ctx.defs[name] = value
		return
	}

	// Map merge: add new keys, override existing
	if existMap, ok := existing.(*OrderedMap); ok {
		if newMap, ok := value.(*OrderedMap); ok {
			newMap.Range(func(k string, v interface{}) bool {
				existMap.Set(k, v)
				return true
			})
			return
		}
		// List-of-maps into map: extract entries from each list item
		if newList, ok := value.([]interface{}); ok {
			for _, item := range newList {
				if itemMap, ok := item.(*OrderedMap); ok {
					itemMap.Range(func(k string, v interface{}) bool {
						existMap.Set(k, v)
						return true
					})
				}
			}
			return
		}
	}

	// List merge: append
	if existList, ok := existing.([]interface{}); ok {
		if newList, ok := value.([]interface{}); ok {
			ctx.defs[name] = append(existList, newList...)
			return
		}
		// Single map into list: append as item
		if _, ok := value.(*OrderedMap); ok {
			ctx.defs[name] = append(existList, value)
			return
		}
	}

	// Fallback: override
	ctx.defs[name] = value
}

// findDefinition looks up a name: first in already-loaded defs, then searches
// for name.yaml starting from requestDir and walking up through parent
// directories. The search goes up to maxParentLevels above docRoot
// (default 1) to prevent reading arbitrary files from higher in the
// filesystem. Set maxParentLevels to -1 for unlimited.
func (ctx *renderContext) findDefinition(name string) (interface{}, string, bool) {
	if v, ok := ctx.defs[name]; ok {
		return v, "cached", true
	}

	// Compute the ceiling directory: docRoot + maxParentLevels up
	ceiling := ""
	if ctx.maxParentLevels >= 0 {
		ceiling = ctx.docRoot
		for i := 0; i < ctx.maxParentLevels; i++ {
			parent := filepath.Dir(ceiling)
			if parent == ceiling {
				break
			}
			ceiling = parent
		}
	}

	dir := ctx.requestDir
	for {
		candidate := filepath.Join(dir, name+".yaml")
		if _, loaded := ctx.filesLoaded[candidate]; !loaded {
			if _, err := os.Stat(candidate); err == nil {
				ctx.loadYAMLFile(candidate)
				// After loading, check if the name is now defined
				if v, ok := ctx.defs[name]; ok {
					return v, candidate, true
				}
				// Check if a data source was loaded for this name
				if dd, ok := ctx.dataSources[name]; ok {
					if result, err := ctx.executeDataSource(name, dd); err == nil {
						ctx.defs[name] = result
						return result, candidate, true
					} else {
						log.Printf("data source %q error: %v", name, err)
					}
				}
			}
		}

		// Also check for name.md (markdown content file)
		mdCandidate := filepath.Join(dir, name+".md")
		if _, loaded := ctx.filesLoaded[mdCandidate]; !loaded {
			if _, err := os.Stat(mdCandidate); err == nil {
				ctx.filesLoaded[mdCandidate] = true
				if mdData, err := os.ReadFile(mdCandidate); err == nil {
					var buf bytes.Buffer
					if err := mdRenderer.Convert(mdData, &buf); err == nil {
						ctx.defs[name] = rawHTML(buf.String())
						return ctx.defs[name], mdCandidate, true
					}
				}
			}
		}

		// Stop if we've reached the ceiling
		if ceiling != "" && dir == ceiling {
			break
		}
		if dir == "/" || dir == "." {
			break
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	// Check if a data source is registered for this name (may have been
	// loaded from a YAML file during an earlier findDefinition call or
	// pre-loaded as the requested page file).
	if dd, ok := ctx.dataSources[name]; ok {
		if result, err := ctx.executeDataSource(name, dd); err == nil {
			ctx.defs[name] = result
			return result, "data source", true
		} else {
			log.Printf("data source %q error: %v", name, err)
		}
	}

	return nil, "", false
}

// findFormat looks up the format definition for a name (^name).
// Searches loaded formats first, then triggers file loading.
func (ctx *renderContext) findFormat(name string) *formatDef {
	if fd, ok := ctx.formats[name]; ok {
		return fd
	}
	return nil
}

// isNameRef returns true if a string looks like it could be a YAML name reference.
// Name references must start with a letter and contain only letters, digits,
// hyphens, and underscores. This rejects URLs (/path, http://..., tel:...),
// CSS values (#333, .5rem), filenames (logo.png), and other non-reference strings.
func isNameRef(s string) bool {
	if len(s) == 0 {
		return false
	}
	// Must start with a letter
	if !((s[0] >= 'a' && s[0] <= 'z') || (s[0] >= 'A' && s[0] <= 'Z')) {
		return false
	}
	// Must contain only letters, digits, hyphens, underscores
	for i := 1; i < len(s); i++ {
		c := s[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

// resolveAll recursively walks the name tree, triggering file loads
// and +merges without producing any output. This ensures all definitions
// are fully assembled before the render pass.
func (ctx *renderContext) resolveAll(name string, depth int) {
	if depth > maxRenderDepth {
		return
	}
	if ctx.resolving[name] {
		return
	}

	content, _, found := ctx.findDefinition(name)
	if !found {
		return
	}

	// Style content is CSS, not YAML name references — skip deep resolution.
	// The style tag has its own renderer (renderStyleYAML) that handles CSS.
	tag, _ := ctx.tagForName(name)
	if tag == "style" {
		return
	}

	ctx.resolving[name] = true
	ctx.resolveContent(content, depth+1)
	delete(ctx.resolving, name)
}

// resolveContent walks a yaml value tree, resolving all name references.
func (ctx *renderContext) resolveContent(val interface{}, depth int) {
	if depth > maxRenderDepth {
		return
	}
	switch v := val.(type) {
	case string:
		if isNameRef(v) {
			ctx.resolveAll(v, depth+1)
		}

	case *OrderedMap:
		v.Range(func(key string, child interface{}) bool {
			// Trigger file loading first (e.g. jumbo.yaml for ^jumbo and +style)
			// before checking tagForName, so formats are available.
			ctx.findDefinition(key)

			tag, _ := ctx.tagForName(key)
			if tag != "" {
				// It's a tag with inline content - resolve children
				ctx.resolveInlineContent(child, depth+1)
			} else {
				// Name reference: store inline content only if no file-based def exists
				if child != nil {
					if _, exists := ctx.defs[key]; !exists {
						ctx.defs[key] = child
					}
				}
				ctx.resolveAll(key, depth+1)
			}
			return true
		})

	case []interface{}:
		for _, item := range v {
			ctx.resolveContent(item, depth+1)
		}
	}
}

// resolveInlineContent resolves names within inline tag content.
func (ctx *renderContext) resolveInlineContent(val interface{}, depth int) {
	if depth > maxRenderDepth {
		return
	}
	switch v := val.(type) {
	case *OrderedMap:
		v.Range(func(key string, child interface{}) bool {
			ctx.findDefinition(key)
			tag, _ := ctx.tagForName(key)
			if tag != "" {
				ctx.resolveInlineContent(child, depth+1)
			} else {
				if child != nil {
					if _, exists := ctx.defs[key]; !exists {
						ctx.defs[key] = child
					}
				}
				ctx.resolveAll(key, depth+1)
			}
			return true
		})
	case []interface{}:
		for _, item := range v {
			ctx.resolveContent(item, depth+1)
		}
	case string:
		if isNameRef(v) {
			ctx.resolveAll(v, depth+1)
		}
	}
}

// isTag returns true if name is a recognized HTML tag.
func (ctx *renderContext) isTag(name string) bool {
	return knownHTMLTags[strings.ToLower(name)]
}

// tagForName determines the HTML tag to use for a name.
// Uses ^name format if available, otherwise the name itself if it's an HTML tag.
// If a ^name format exists but specifies no tag, it suppresses the HTML tag
// fallback — this allows ^name: with no tag to prevent tag output for names
// that happen to match HTML tags (e.g., ^header: without a tag field).
func (ctx *renderContext) tagForName(name string) (string, *formatDef) {
	fd := ctx.findFormat(name)
	if fd != nil && fd.Tag != "" {
		return fd.Tag, fd
	}
	if fd != nil {
		return "", fd
	}
	if ctx.isTag(name) {
		return name, fd
	}
	return "", fd
}

// renderName resolves a name and renders it.
func (ctx *renderContext) renderName(sb *strings.Builder, name string, depth int) {
	if depth > maxRenderDepth {
		fmt.Fprintf(sb, "<!-- render depth exceeded for %q -->\n", name)
		return
	}

	if ctx.resolving[name] {
		fmt.Fprintf(sb, "<!-- circular reference: %q -->\n", name)
		return
	}

	content, source, found := ctx.findDefinition(name)

	// If not found and a YAML parse error exists, show it immediately.
	// This must happen before special-case handlers (scripts, iteration, etc.)
	// that would otherwise return early and hide the error.
	if !found {
		if file, errMsg := ctx.yamlErrorForName(name); errMsg != "" {
			if ctx.debug {
				fmt.Fprintf(sb, "<!-- %q: YAML parse error in %s: %s -->\n", name, file, errMsg)
			}
			fmt.Fprintf(sb, "%s<div style=\"border:2px dashed red;padding:8px;margin:4px;color:red;\">"+
				"YAML error in <strong>%s</strong>: %s</div>\n",
				indent(depth), html.EscapeString(file), html.EscapeString(errMsg))
			return
		}
	}

	if ctx.debug {
		if found {
			fmt.Fprintf(sb, "<!-- resolve %q from %s -->\n", name, source)
		} else {
			fmt.Fprintf(sb, "<!-- %q: not found -->\n", name)
		}
	}

	tag, fd := ctx.tagForName(name)

	if ctx.debug && fd != nil {
		fmt.Fprintf(sb, "<!-- ^%s: tag=%q contents=%q -->\n", name, fd.Tag, fd.Contents)
	}

	// Special case: style tag renders CSS from its content map
	if tag == "style" && found {
		css := renderStyleYAML(content, depth+1)
		if css != "" {
			writeTagWithContent(sb, "style", "", css, depth)
		}
		return
	}

	// Special case: script-based rendering (script: "python" etc.)
	// Scripts can run even without content data (e.g., ^main with file: but no main: definition)
	if fd != nil && fd.Script != "" {
		data := content
		if !found {
			data = nil
		}
		sb.WriteString(ctx.renderScript(fd, data))
		return
	}

	// Special case: markup processing (markup: "markdown")
	// Converts the string content through a markup processor before rendering.
	if fd != nil && fd.Markup != "" && found {
		if str, ok := content.(string); ok {
			converted := ctx.processMarkup(fd.Markup, str)
			if tag != "" {
				attrs := ""
				if fd.Params != nil {
					attrs = formatParamsWithVars(fd.Params, nil)
				}
				writeTagWithContent(sb, tag, attrs, converted, depth)
			} else {
				sb.WriteString(converted)
			}
			return
		}
	}

	// If we have a format with $key/$value params and no $* contents,
	// this is an iteration format (like ^meta): render each entry separately.
	if fd != nil && fd.Tag != "" && hasVarSubstitution(fd) && fd.Contents != "$*" {
		if found {
			ctx.resolving[name] = true
			ctx.renderIterated(sb, name, fd, content, depth)
			delete(ctx.resolving, name)
		}
		return
	}

	// We have a tag (either from ^name or because name is an HTML tag)
	if tag != "" {
		attrs := ""
		if fd != nil && fd.Params != nil {
			attrs = formatParamsWithVars(fd.Params, nil)
		}
		if found {
			ctx.resolving[name] = true

			// ContentWrap: the format specifies a structural content wrapper
			// (e.g., content: {card-body: '$*'}). Substitute $* with the actual
			// children, then render the wrapped structure.
			// ContentWrapPlural (contents: plural): wrap each iterable item
			// individually rather than wrapping all content as one block.
			if fd != nil && fd.ContentWrap != nil {
				if voidElements[tag] {
					fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
				} else if fd.ContentWrapPlural {
					// Plural: wrap each list item (or map entry value) individually
					fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
					if items, ok := content.([]interface{}); ok {
						for _, item := range items {
							wrapped := substituteContentWrap(fd.ContentWrap, item)
							ctx.renderContent(sb, wrapped, depth+1)
						}
					} else if contentMap, ok := content.(*OrderedMap); ok {
						ctx.renderContentWrapPluralMap(sb, fd.ContentWrap, contentMap, depth+1)
					} else {
						// Single item, wrap it once
						wrapped := substituteContentWrap(fd.ContentWrap, content)
						ctx.renderContent(sb, wrapped, depth+1)
					}
					fmt.Fprintf(sb, "%s</%s>\n", indent(depth), tag)
				} else {
					// Singular: wrap all content as one block
					wrapped := substituteContentWrap(fd.ContentWrap, content)
					var inner strings.Builder
					ctx.renderContent(&inner, wrapped, depth+1)
					writeTagWithContent(sb, tag, attrs, inner.String(), depth)
				}
				delete(ctx.resolving, name)
				return
			}

			// If content is a list of maps and the format defines a wrapping tag,
			// iterate and wrap each list item separately in the tag.
			// E.g., ^navitems: {tag: li} with navitems: [{link: /a, text: A}]
			// renders each map item as its own <li>.
			// Lists of strings (name references like [brand, navbarnav]) are
			// NOT iterated — they are children of a single container.
			if items, ok := content.([]interface{}); ok && fd != nil && fd.Tag != "" && len(items) > 0 {
				if _, firstIsMap := items[0].(*OrderedMap); firstIsMap {
					for _, item := range items {
						if voidElements[tag] {
							fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
						} else {
							var inner strings.Builder
							ctx.renderContent(&inner, item, depth+1)
							writeTagWithContent(sb, tag, attrs, inner.String(), depth)
						}
					}
					delete(ctx.resolving, name)
					return
				}
			}

			if voidElements[tag] {
				fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
			} else if raw, ok := content.(rawHTML); ok {
				// Raw HTML (e.g., rendered markdown) is typically large; always multi-line.
				fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
				sb.WriteString(string(raw))
				fmt.Fprintf(sb, "%s</%s>\n", indent(depth), tag)
			} else {
				// Render content to a buffer, then collapse to one line if short enough.
				var inner strings.Builder
				if tag == "pre" {
					// <pre> content must not be indented; whitespace is significant.
					if str, ok := content.(string); ok {
						fmt.Fprintf(&inner, "%s\n", html.EscapeString(str))
					} else {
						ctx.renderContent(&inner, content, 0)
					}
				} else if str, ok := content.(string); ok {
					fmt.Fprintf(&inner, "%s%s\n", indent(depth+1), html.EscapeString(str))
				} else {
					ctx.renderContent(&inner, content, depth+1)
				}
				writeTagWithContent(sb, tag, attrs, inner.String(), depth)
			}
			delete(ctx.resolving, name)
		} else {
			if voidElements[tag] {
				fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
			} else {
				fmt.Fprintf(sb, "%s<%s%s></%s>\n", indent(depth), tag, attrs, tag)
			}
		}
		return
	}

	// No tag - just a named definition, render its content directly
	if found {
		ctx.resolving[name] = true
		if raw, ok := content.(rawHTML); ok {
			sb.WriteString(string(raw))
		} else {
			ctx.renderContent(sb, content, depth+1)
		}
		delete(ctx.resolving, name)
		return
	}

	// Unresolved name (YAML errors already handled above)
	fmt.Fprintf(sb, "%s<div style=\"border:2px dashed red;padding:8px;margin:4px;color:red;\">"+
		"Undefined name: <strong>%s</strong></div>\n",
		indent(depth), html.EscapeString(name))
}

// renderContent renders a yaml value as page content.
func (ctx *renderContext) renderContent(sb *strings.Builder, val interface{}, depth int) {
	// Raw HTML (e.g., rendered markdown) passes through directly
	if raw, ok := val.(rawHTML); ok {
		sb.WriteString(string(raw))
		return
	}

	switch v := val.(type) {
	case string:
		if isNameRef(v) {
			ctx.renderName(sb, v, depth+1)
		} else if len(v) > 1 && v[0] == '$' && isNameRef(v[1:]) {
			// $varname substitution: render the named definition's value
			if def, ok := ctx.defs[v[1:]]; ok {
				ctx.renderContent(sb, def, depth)
			} else {
				fmt.Fprintf(sb, "%s%s\n", indent(depth), html.EscapeString(v))
			}
		} else {
			// Literal text (contains spaces, etc.)
			fmt.Fprintf(sb, "%s%s\n", indent(depth), html.EscapeString(v))
		}

	case *OrderedMap:
		// Direct tag specification: {tag: "tagname", params: {...}, text: "..."}
		if tagVal, ok := v.Get("tag"); ok {
			if tagName, ok := tagVal.(string); ok {
				ctx.renderDirectTag(sb, tagName, v, depth+1)
				return
			}
		}

		// Check for container entry: a formatted key whose format has
		// contents with $var (like ^link's contents: '$contents').
		// Siblings are rendered as children of the container.
		if ctx.tryRenderContainer(sb, v, depth+1) {
			return
		}

		v.Range(func(key string, child interface{}) bool {
			if ctx.debug {
				fmt.Fprintf(sb, "<!-- key %q -->\n", key)
			}
			// If key is a tag (or has a ^format), render inline with child as content
			tag, fd := ctx.tagForName(key)
			if tag != "" {
				ctx.renderInlineTag(sb, key, tag, fd, child, depth+1)
			} else {
				// It's a name reference; store inline content if provided
				if child != nil {
					if _, exists := ctx.defs[key]; !exists {
						ctx.defs[key] = child
					}
				}
				ctx.renderName(sb, key, depth+1)
			}
			return true
		})

	case []interface{}:
		for i, item := range v {
			if ctx.debug {
				fmt.Fprintf(sb, "<!-- list item [%d] -->\n", i)
			}
			ctx.renderContent(sb, item, depth+1)
		}

	case nil:
		// nothing

	default:
		fmt.Fprintf(sb, "%s%v\n", indent(depth), v)
	}
}

// renderInlineTag renders a tag with inline content from a map entry like {h1: "text"}.
// This is used when renderContent encounters a map key that is a tag.
func (ctx *renderContext) renderInlineTag(sb *strings.Builder, name, tag string, fd *formatDef, content interface{}, depth int) {
	if ctx.debug {
		fmt.Fprintf(sb, "<!-- inline tag %q -> <%s> -->\n", name, tag)
	}

	// Script processing: execute content as script code (e.g., ^php: script: php)
	if fd != nil && fd.Script != "" {
		sb.WriteString(ctx.renderScript(fd, content))
		return
	}

	// Markup processing: convert content through markup processor (e.g., markdown)
	if fd != nil && fd.Markup != "" {
		if str, ok := content.(string); ok {
			converted := ctx.processMarkup(fd.Markup, str)
			if tag != "" {
				attrs := ""
				if fd.Params != nil {
					attrs = formatParamsWithVars(fd.Params, nil)
				}
				writeTagWithContent(sb, tag, attrs, converted, depth)
			} else {
				sb.WriteString(converted)
			}
			return
		}
	}

	// If the format has $var params and content is a map, use entries as var substitutions
	if fd != nil && fd.Params != nil && hasVarSubstitution(fd) {
		if _, ok := content.(*OrderedMap); ok {
			if usesKeyValueVars(fd) {
				// $key/$value format: iterate over map entries, each producing its own tag
				ctx.renderIterated(sb, name, fd, content, depth)
			} else {
				ctx.renderFormattedEntry(sb, name, fd, tag, content.(*OrderedMap), depth)
			}
			return
		}
		// List content with var-substitution format: iterate over list items
		if _, ok := content.([]interface{}); ok {
			ctx.renderIterated(sb, name, fd, content, depth)
			return
		}
		// String content: the inline value IS the primary parameter value.
		// Map it to $* and also to all named $vars in params and contents (e.g., $url, $contents).
		if str, ok := content.(string); ok {
			vars := map[string]string{"*": str}
			fd.Params.Range(func(_ string, pvRaw interface{}) bool {
				pv := fmt.Sprintf("%v", pvRaw)
				for _, vn := range extractVarNames(pv) {
					if _, exists := vars[vn]; !exists {
						vars[vn] = str
					}
				}
				return true
			})
			// Also resolve var names from fd.Contents so that e.g. $contents gets the string value
			if fd.Contents != "" {
				for _, vn := range extractVarNames(fd.Contents) {
					if _, exists := vars[vn]; !exists {
						vars[vn] = str
					}
				}
			}
			attrs := formatParamsWithVars(fd.Params, vars)
			contentsVal := substituteVars(fd.Contents, vars)
			if voidElements[tag] {
				fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
			} else if contentsVal != "" && !hasUnreplacedVars(contentsVal) {
				fmt.Fprintf(sb, "%s<%s%s>%s</%s>\n", indent(depth), tag, attrs, html.EscapeString(contentsVal), tag)
			} else {
				// No valid contents - render open/close with no content
				if ctx.debug {
					fmt.Fprintf(sb, "<!-- %s: no resolved content -->\n", name)
				}
				fmt.Fprintf(sb, "%s<%s%s></%s>\n", indent(depth), tag, attrs, tag)
			}
			return
		}
	}

	// $varname substitution: if the inline content is "$name", replace with
	// the named definition's value. This allows format definitions to reference
	// other definitions, e.g., content: [{h1: $errortitle}] in ^error.
	if str, ok := content.(string); ok && len(str) > 1 && str[0] == '$' && isNameRef(str[1:]) {
		if def, ok := ctx.defs[str[1:]]; ok {
			content = def
			// Convert rawHTML to string so it renders as inline tag content
			if raw, ok := content.(rawHTML); ok {
				content = string(raw)
			}
		}
	}

	attrs := ""
	if fd != nil && fd.Params != nil {
		attrs = formatParamsWithVars(fd.Params, nil)
	}

	if content == nil {
		if voidElements[tag] {
			fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
		} else {
			fmt.Fprintf(sb, "%s<%s%s></%s>\n", indent(depth), tag, attrs, tag)
		}
		return
	}

	if voidElements[tag] {
		fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
		return
	}

	// String content is literal text
	if str, ok := content.(string); ok {
		escaped := html.EscapeString(str)
		if tag == "pre" {
			// <pre> content must not be indented; whitespace is significant.
			// Close tag is also unindented to avoid trailing spaces in content.
			fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
			fmt.Fprintf(sb, "%s\n", escaped)
			fmt.Fprintf(sb, "</%s>\n", tag)
		} else {
			line := fmt.Sprintf("%s<%s%s>%s</%s>", indent(depth), tag, attrs, escaped, tag)
			if len(line) <= maxInlineTagLength {
				sb.WriteString(line)
				sb.WriteByte('\n')
			} else {
				fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
				fmt.Fprintf(sb, "%s%s\n", indent(depth+1), escaped)
				fmt.Fprintf(sb, "%s</%s>\n", indent(depth), tag)
			}
		}
		return
	}

	// ContentWrap: substitute $* with actual content, render wrapped structure.
	// This check comes before the ul/ol auto-wrap so explicit format definitions
	// take priority over the built-in fallback.
	// ContentWrapPlural (contents: plural): wrap each iterable item individually.
	if fd != nil && fd.ContentWrap != nil {
		var inner strings.Builder
		if fd.ContentWrapPlural {
			// Plural: wrap each list item (or map entry value) individually
			if items, ok := content.([]interface{}); ok {
				for _, item := range items {
					wrapped := substituteContentWrap(fd.ContentWrap, item)
					ctx.renderContent(&inner, wrapped, depth+1)
				}
			} else if contentMap, ok := content.(*OrderedMap); ok {
				ctx.renderContentWrapPluralMap(&inner, fd.ContentWrap, contentMap, depth+1)
			} else {
				// Single item, wrap it once
				wrapped := substituteContentWrap(fd.ContentWrap, content)
				ctx.renderContent(&inner, wrapped, depth+1)
			}
		} else {
			// Singular: wrap all content as one block
			wrapped := substituteContentWrap(fd.ContentWrap, content)
			ctx.renderContent(&inner, wrapped, depth+1)
		}
		writeTagWithContent(sb, tag, attrs, inner.String(), depth)
		return
	}

	// Otherwise render children recursively
	// For list-type tags (ul, ol), auto-wrap list items in <li>
	if (tag == "ul" || tag == "ol") {
		if items, ok := content.([]interface{}); ok {
			fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
			for _, item := range items {
				if str, ok := item.(string); ok {
					if isNameRef(str) {
						ctx.renderName(sb, str, depth+1)
					} else {
						fmt.Fprintf(sb, "%s<li>%s</li>\n", indent(depth+1), html.EscapeString(str))
					}
				} else if _, ok := item.(*OrderedMap); ok {
					fmt.Fprintf(sb, "%s<li>\n", indent(depth+1))
					ctx.renderContent(sb, item, depth+2)
					fmt.Fprintf(sb, "%s</li>\n", indent(depth+1))
				} else {
					fmt.Fprintf(sb, "%s<li>%v</li>\n", indent(depth+1), item)
				}
			}
			fmt.Fprintf(sb, "%s</%s>\n", indent(depth), tag)
			return
		}
	}

	var inner strings.Builder
	if tag == "pre" {
		ctx.renderContent(&inner, content, 0)
	} else {
		ctx.renderContent(&inner, content, depth+1)
	}
	writeTagWithContent(sb, tag, attrs, inner.String(), depth)
}

// renderDirectTag renders a map that has an explicit {tag: "...", params: {...}, text: "..."}.
// This is an alternative to format-based rendering, used for inline tag specifications.
func (ctx *renderContext) renderDirectTag(sb *strings.Builder, tagName string, m *OrderedMap, depth int) {
	attrs := ""
	if paramsVal, ok := m.Get("params"); ok {
		if params, ok := paramsVal.(*OrderedMap); ok {
			attrs = formatMapAsAttrs(params)
		}
	}

	text := ""
	if textVal, ok := m.Get("text"); ok {
		if t, ok := textVal.(string); ok {
			text = t
		}
	}

	if voidElements[tagName] {
		fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tagName, attrs)
	} else if text != "" {
		fmt.Fprintf(sb, "%s<%s%s>%s</%s>\n", indent(depth), tagName, attrs, html.EscapeString(text), tagName)
	} else {
		fmt.Fprintf(sb, "%s<%s%s></%s>\n", indent(depth), tagName, attrs, tagName)
	}
}

// tryRenderContainer checks if a map has a "container" entry — a formatted key
// whose format has contents with $var (like ^link's contents: '$contents').
// If found, siblings are rendered as children of that container tag.
// Returns true if a container was rendered.
func (ctx *renderContext) tryRenderContainer(sb *strings.Builder, m *OrderedMap, depth int) bool {
	// Find the container key: a formatted name with $var in contents
	var containerKey string
	var containerTag string
	var containerFd *formatDef
	var containerVal interface{}

	m.Range(func(key string, val interface{}) bool {
		tag, fd := ctx.tagForName(key)
		if tag != "" && fd != nil && fd.Contents != "" && fd.Contents != "$*" && hasUnreplacedVars(fd.Contents) {
			containerKey = key
			containerTag = tag
			containerFd = fd
			containerVal = val
			return false // stop
		}
		return true
	})

	if containerKey == "" {
		return false
	}

	// A container needs siblings to provide child content.
	// A single-entry map (no siblings) should be handled by renderInlineTag instead.
	if m.Len() <= 1 {
		return false
	}

	// If the container value is a map, it provides variable substitutions
	// (e.g., {link: {url: /service, contents: "text"}}), not sibling children.
	// Let renderInlineTag/renderFormattedEntry handle this case instead.
	if _, isMap := containerVal.(*OrderedMap); isMap {
		return false
	}

	// Build vars from the container's own value (the primary parameter value)
	vars := make(map[string]string)
	if str, ok := containerVal.(string); ok {
		vars["*"] = str
		// Map the value to all named $vars in params
		if containerFd.Params != nil {
			containerFd.Params.Range(func(_ string, pvRaw interface{}) bool {
				pv := fmt.Sprintf("%v", pvRaw)
				for _, vn := range extractVarNames(pv) {
					if _, exists := vars[vn]; !exists {
						vars[vn] = str
					}
				}
				return true
			})
		}
	}

	attrs := formatParamsWithVars(containerFd.Params, vars)

	// Render sibling entries as children
	var childSb strings.Builder
	m.Range(func(key string, child interface{}) bool {
		if key == containerKey {
			return true // continue
		}
		// "text" key provides literal text content
		if key == "text" {
			if str, ok := child.(string); ok {
				fmt.Fprintf(&childSb, "%s%s\n", indent(depth+1), html.EscapeString(str))
				return true
			}
		}
		tag, fd := ctx.tagForName(key)
		if tag != "" {
			ctx.renderInlineTag(&childSb, key, tag, fd, child, depth+1)
		} else {
			if child != nil {
				if _, exists := ctx.defs[key]; !exists {
					ctx.defs[key] = child
				}
			}
			ctx.renderName(&childSb, key, depth+1)
		}
		return true
	})

	childContent := childSb.String()

	if ctx.debug {
		fmt.Fprintf(sb, "<!-- container %q -> <%s> with %d siblings -->\n", containerKey, containerTag, m.Len()-1)
	}

	if voidElements[containerTag] {
		fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), containerTag, attrs)
	} else if childContent != "" {
		fmt.Fprintf(sb, "%s<%s%s>\n%s%s</%s>\n", indent(depth), containerTag, attrs, childContent, indent(depth), containerTag)
	} else {
		fmt.Fprintf(sb, "%s<%s%s></%s>\n", indent(depth), containerTag, attrs, containerTag)
	}

	return true
}

// renderIterated handles ^name definitions that use variable substitution.
// Supports three modes:
//   - ParamsWildcard ($*): each content entry (list of maps) becomes tag attributes
//   - $key/$value: each map entry produces a tag with key/value substituted in params
//   - Named vars ($url, $contents, etc.): content map fields substitute into params/contents
func (ctx *renderContext) renderIterated(sb *strings.Builder, name string, fd *formatDef, content interface{}, depth int) {
	tag := fd.Tag

	// Mode 1: ParamsWildcard - content is a list of maps, each map's entries become attributes
	if fd.ParamsWildcard {
		if contentList, ok := content.([]interface{}); ok {
			for _, item := range contentList {
				if itemMap, ok := item.(*OrderedMap); ok {
					attrs := formatMapAsAttrs(itemMap)
					if ctx.debug {
						fmt.Fprintf(sb, "<!-- ^%s wildcard params -->\n", name)
					}
					if voidElements[tag] {
						fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
					} else {
						fmt.Fprintf(sb, "%s<%s%s></%s>\n", indent(depth), tag, attrs, tag)
					}
				}
			}
		} else if contentMap, ok := content.(*OrderedMap); ok {
			// Single map - each key/value pair becomes one tag with that attribute
			contentMap.Range(func(key string, value interface{}) bool {
				valStr := fmt.Sprintf("%v", value)
				attr := fmt.Sprintf(" %s=\"%s\"", key, html.EscapeString(valStr))
				if voidElements[tag] {
					fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attr)
				} else {
					fmt.Fprintf(sb, "%s<%s%s></%s>\n", indent(depth), tag, attr, tag)
				}
				return true
			})
		}
		return
	}

	// Mode 2: $key/$value - content is a map, each entry becomes a tag instance
	if contentMap, ok := content.(*OrderedMap); ok {
		contentMap.Range(func(key string, value interface{}) bool {
			valStr := fmt.Sprintf("%v", value)
			vars := map[string]string{"key": key, "value": valStr}
			attrs := formatParamsWithVars(fd.Params, vars)

			if ctx.debug {
				fmt.Fprintf(sb, "<!-- ^%s iterate: key=%q value=%q -->\n", name, key, valStr)
			}

			if voidElements[tag] {
				fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
			} else {
				fmt.Fprintf(sb, "%s<%s%s>%s</%s>\n", indent(depth), tag, attrs, html.EscapeString(valStr), tag)
			}
			return true
		})
		return
	}

	// Mode 3: content is a list of maps with named vars ($url, $contents, etc.)
	if contentList, ok := content.([]interface{}); ok {
		for _, item := range contentList {
			if itemMap, ok := item.(*OrderedMap); ok {
				ctx.renderFormattedEntry(sb, name, fd, tag, itemMap, depth)
			}
		}
	}
}

// renderFormattedEntry renders a single entry using a format def with named $var substitution.
func (ctx *renderContext) renderFormattedEntry(sb *strings.Builder, name string, fd *formatDef, tag string, entry *OrderedMap, depth int) {
	// Build vars map from entry
	vars := make(map[string]string)
	entry.Range(func(k string, v interface{}) bool {
		vars[k] = fmt.Sprintf("%v", v)
		return true
	})

	attrs := formatParamsWithVars(fd.Params, vars)
	contentsVal := substituteVars(fd.Contents, vars)

	if ctx.debug {
		fmt.Fprintf(sb, "<!-- ^%s entry: vars=%v -->\n", name, vars)
	}

	if voidElements[tag] {
		fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
	} else if contentsVal != "" && !hasUnreplacedVars(contentsVal) {
		fmt.Fprintf(sb, "%s<%s%s>%s</%s>\n", indent(depth), tag, attrs, html.EscapeString(contentsVal), tag)
	} else {
		// contentsVal has unreplaced vars or is empty;
		// try rendering child content from entry values that are names
		rendered := ctx.renderEntryChildren(entry, depth+1)
		if rendered != "" {
			fmt.Fprintf(sb, "%s<%s%s>\n%s%s</%s>\n", indent(depth), tag, attrs, rendered, indent(depth), tag)
		} else if !hasUnreplacedVars(attrs) {
			fmt.Fprintf(sb, "%s<%s%s></%s>\n", indent(depth), tag, attrs, tag)
		} else {
			if ctx.debug {
				fmt.Fprintf(sb, "<!-- %s: suppressed, unreplaced vars -->\n", name)
			}
		}
	}
}

// renderEntryChildren renders the values of a map entry that are themselves
// names with tag formats. This allows sibling keys to become nested content.
// For example, {link: /, image: wt-logo.png} can render image inside link
// by detecting that "image" has a ^image format and rendering it.
func (ctx *renderContext) renderEntryChildren(entry *OrderedMap, depth int) string {
	var sb strings.Builder
	entry.Range(func(k string, v interface{}) bool {
		tag, fd := ctx.tagForName(k)
		if tag != "" {
			ctx.renderInlineTag(&sb, k, tag, fd, v, depth)
		}
		// Non-tag keys are consumed as var values, skip them here
		return true
	})
	return sb.String()
}

// renderContentWrapPluralMap handles ContentWrapPlural when the content is a map.
// If a map key has a format that iterates $key/$value (like ^links), the inner
// map entries are expanded individually — each entry gets wrapped separately.
// For example, ulist's {li: '$*'} wrapping links' {About: about, Contact: contact}
// produces one <li><a>...</a></li> per link entry.
func (ctx *renderContext) renderContentWrapPluralMap(sb *strings.Builder, contentWrap interface{}, contentMap *OrderedMap, depth int) {
	contentMap.Range(func(k string, v interface{}) bool {
		// Check if this key has a format that iterates $key/$value over maps
		_, childFd := ctx.tagForName(k)
		if childFd != nil && usesKeyValueVars(childFd) {
			if innerMap, ok := v.(*OrderedMap); ok {
				// Expand: each inner map entry becomes a separate wrapped item
				// with the format name preserved so it renders through the format.
				innerMap.Range(func(innerKey string, innerVal interface{}) bool {
					singleEntry := NewOrderedMap()
					singleEntry.Set(innerKey, innerVal)
					entryWithFormat := NewOrderedMap()
					entryWithFormat.Set(k, singleEntry)
					wrapped := substituteContentWrap(contentWrap, entryWithFormat)
					ctx.renderContent(sb, wrapped, depth)
					return true
				})
				return true
			}
		}
		// Default: wrap the value directly
		wrapped := substituteContentWrap(contentWrap, v)
		ctx.renderContent(sb, wrapped, depth)
		return true
	})
}

// indent returns the indentation string for the given depth.
func indent(depth int) string {
	return strings.Repeat(indentSpacer, depth)
}

// writeTagWithContent writes a non-void HTML tag with its already-rendered
// inner content. If the content is a single line and the total (indentation +
// opening tag + content + closing tag) fits within maxInlineTagLength, the
// tag and content are collapsed onto one line. Otherwise, the standard
// multi-line format is used.
func writeTagWithContent(sb *strings.Builder, tag, attrs, renderedContent string, depth int) {
	// Never collapse <pre> to inline; whitespace is significant.
	if tag != "pre" {
		trimmed := strings.TrimRight(renderedContent, "\n")
		if !strings.Contains(trimmed, "\n") {
			inlineContent := strings.TrimSpace(trimmed)
			if inlineContent != "" {
				line := fmt.Sprintf("%s<%s%s>%s</%s>", indent(depth), tag, attrs, inlineContent, tag)
				if len(line) <= maxInlineTagLength {
					sb.WriteString(line)
					sb.WriteByte('\n')
					return
				}
			}
		}
	}
	fmt.Fprintf(sb, "%s<%s%s>\n", indent(depth), tag, attrs)
	sb.WriteString(renderedContent)
	if tag == "pre" {
		// Close tag unindented to avoid trailing spaces in pre content.
		fmt.Fprintf(sb, "</%s>\n", tag)
	} else {
		fmt.Fprintf(sb, "%s</%s>\n", indent(depth), tag)
	}
}

// computeRequestURI derives the URL path from a filesystem request path
// and docRoot. For example:
//
//	docRoot="/srv/default", reqPath="/srv/default/status.yaml" -> "/status"
//	docRoot="/srv/default", reqPath="/srv/default/service/service.yaml" -> "/service"
//	docRoot="/srv/default", reqPath="/srv/default/index.yaml" -> "/"
func computeRequestURI(docRoot, reqPath string) string {
	rel, err := filepath.Rel(docRoot, reqPath)
	if err != nil {
		return "/"
	}
	// Strip file extension
	ext := filepath.Ext(rel)
	if ext != "" {
		rel = rel[:len(rel)-len(ext)]
	}
	// Handle index files -> use directory
	base := filepath.Base(rel)
	dir := filepath.Dir(rel)
	if base == "index" {
		if dir == "." {
			return "/"
		}
		return "/" + dir
	}
	// Handle name matching parent dir (e.g., service/service -> /service)
	if base == filepath.Base(dir) {
		return "/" + dir
	}
	// Plain file in docRoot (e.g., status -> /status)
	if dir == "." {
		return "/" + base
	}
	return "/" + rel
}

