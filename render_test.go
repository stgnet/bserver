package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// defaultDocRoot returns the www/default/ directory path for integration tests.
func defaultDocRoot(t *testing.T) string {
	t.Helper()
	base, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(base, "www", "default")
}

// setupMinimalSite creates a minimal site in a temp dir with html.yaml and body.yaml,
// plus any additional files provided as filename->content pairs.
func setupMinimalSite(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	// Always create the minimal structure
	os.WriteFile(filepath.Join(dir, "html.yaml"), []byte("html:\n - body\n"), 0644)
	os.WriteFile(filepath.Join(dir, "body.yaml"), []byte("body:\n - main\n"), 0644)
	for name, content := range files {
		os.WriteFile(filepath.Join(dir, name), []byte(content), 0644)
	}
	return dir
}

func TestHomepageContent(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output, _ := renderYAMLPage(docRoot, indexPath, false, 1, nil)

	if !strings.Contains(output, "bserver Documentation") {
		t.Errorf("Homepage missing 'bserver Documentation' heading.\nFirst 2000 chars:\n%s", output[:min(len(output), 2000)])
	}
	if !strings.Contains(output, "YAML-driven") {
		t.Error("Homepage missing YAML-driven description")
	}
	if !strings.Contains(output, "Key Features") {
		t.Error("Homepage missing 'Key Features' section")
	}
	if !strings.Contains(output, "<li>") {
		t.Error("Homepage missing list items from ulist")
	}
	if !strings.Contains(output, "Quick Example") {
		t.Error("Homepage missing 'Quick Example' section")
	}
	if !strings.Contains(output, "<pre>") {
		t.Error("Homepage missing code example in <pre> tag")
	}
	if !strings.Contains(output, `href="/getting-started"`) {
		t.Error("Homepage missing link to Getting Started")
	}
	if !strings.Contains(output, `href="/formats"`) {
		t.Error("Homepage missing link to Formats")
	}
	if strings.Contains(output, "Undefined name") {
		t.Errorf("Homepage has undefined name errors.\nOutput:\n%s", output)
	}
}

func TestFooterContent(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output, _ := renderYAMLPage(docRoot, indexPath, false, 1, nil)

	if !strings.Contains(output, "<footer>") {
		t.Error("Page missing <footer> tag")
	}
	if !strings.Contains(output, "text-muted") {
		t.Error("Footer missing text-muted class from ^muted format")
	}
	if !strings.Contains(output, "Powered by bserver") {
		t.Error("Footer missing 'Powered by bserver' text")
	}
}

func TestNavbarPresent(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output, _ := renderYAMLPage(docRoot, indexPath, false, 1, nil)

	if !strings.Contains(output, "navbar") {
		t.Error("Homepage missing navbar")
	}
	if !strings.Contains(output, `href="/"`) {
		t.Error("Navbar missing home link")
	}
	if !strings.Contains(output, "Home") {
		t.Error("Navbar missing Home text")
	}
	if !strings.Contains(output, `href="/getting-started"`) {
		t.Error("Navbar missing Getting Started link")
	}
	if !strings.Contains(output, `href="/formats"`) {
		t.Error("Navbar missing Formats link")
	}
	if !strings.Contains(output, `href="/components"`) {
		t.Error("Navbar missing Components link")
	}
	if !strings.Contains(output, `href="/scripts"`) {
		t.Error("Navbar missing Scripts link")
	}
	if !strings.Contains(output, `href="/advanced"`) {
		t.Error("Navbar missing Advanced link")
	}
	if !strings.Contains(output, "nav-link") {
		t.Error("Navbar missing nav-link class on links")
	}
	if !strings.Contains(output, "nav-link active") {
		t.Error("Navbar missing active class on current page link")
	}
}

func TestMarkdownGettingStarted(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	mdPath := filepath.Join(docRoot, "getting-started.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("getting-started.md not found")
	}
	output, _ := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

	if !strings.Contains(output, "<!DOCTYPE html>") {
		t.Error("Markdown page missing DOCTYPE")
	}
	if !strings.Contains(output, "Getting Started") {
		t.Error("Getting Started page missing heading")
	}
	if !strings.Contains(output, "Rendering Pipeline") {
		t.Error("Getting Started page missing pipeline section")
	}
	if !strings.Contains(output, "Name Resolution") {
		t.Error("Getting Started page missing name resolution section")
	}
	if !strings.Contains(output, "<table>") {
		t.Error("Getting Started page missing table (GFM table extension may not be enabled)")
	}
	if !strings.Contains(output, "navbar") {
		t.Error("Markdown page missing navbar from site structure")
	}
	if !strings.Contains(output, "<footer>") {
		t.Error("Markdown page missing footer from site structure")
	}
}

func TestMarkdownDefinitions(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	mdPath := filepath.Join(docRoot, "definitions.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("definitions.md not found")
	}
	output, _ := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

	if !strings.Contains(output, "Content Definitions") {
		t.Error("Definitions page missing heading")
	}
	if !strings.Contains(output, "Merge Definitions") {
		t.Error("Definitions page missing merge definitions section")
	}
	if !strings.Contains(output, "First Definition Wins") {
		t.Error("Definitions page missing first-definition-wins section")
	}
}

func TestMarkdownFormats(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	mdPath := filepath.Join(docRoot, "formats.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("formats.md not found")
	}
	output, _ := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

	if !strings.Contains(output, "Format Definitions") {
		t.Error("Formats page missing heading")
	}
	if !strings.Contains(output, "The ^ Prefix") {
		t.Error("Formats page missing ^ prefix section")
	}
	if !strings.Contains(output, "Variable Substitution") {
		t.Error("Formats page missing variable substitution section")
	}
	if !strings.Contains(output, "content") && !strings.Contains(output, "contents") {
		t.Error("Formats page missing content/contents section")
	}
}

func TestMarkdownComponents(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	mdPath := filepath.Join(docRoot, "components.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("components.md not found")
	}
	output, _ := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

	if !strings.Contains(output, "Built-in Components") {
		t.Error("Components page missing heading")
	}
	if !strings.Contains(output, "bootstrap5") {
		t.Error("Components page missing Bootstrap section")
	}
	if !strings.Contains(output, "navbar") {
		t.Error("Components page missing navbar section")
	}
}

func TestMarkdownScripts(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	mdPath := filepath.Join(docRoot, "scripts.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("scripts.md not found")
	}
	output, _ := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

	if !strings.Contains(output, "Server-Side Scripts") {
		t.Error("Scripts page missing heading")
	}
	if !strings.Contains(output, "Python") {
		t.Error("Scripts page missing Python section")
	}
	if !strings.Contains(output, "JavaScript") {
		t.Error("Scripts page missing JavaScript section")
	}
	if !strings.Contains(output, "PHP") {
		t.Error("Scripts page missing PHP section")
	}
	if !strings.Contains(output, "Environment Variables") {
		t.Error("Scripts page missing environment variables section")
	}
}

func TestMarkdownAdvanced(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	mdPath := filepath.Join(docRoot, "advanced.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("advanced.md not found")
	}
	output, _ := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

	if !strings.Contains(output, "Advanced Features") {
		t.Error("Advanced page missing heading")
	}
	if !strings.Contains(output, "Virtual Hosting") {
		t.Error("Advanced page missing virtual hosting section")
	}
	if !strings.Contains(output, "Style Rendering") {
		t.Error("Advanced page missing style rendering section")
	}
	if !strings.Contains(output, "Two-Pass Rendering") {
		t.Error("Advanced page missing two-pass rendering section")
	}
}

func TestNoUndefinedNames(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output, _ := renderYAMLPage(docRoot, indexPath, false, 1, nil)

	if strings.Contains(output, "Undefined name") {
		t.Errorf("Homepage has undefined name errors.\nOutput:\n%s", output)
	}
}

func TestIsNameRef(t *testing.T) {
	// Valid name refs
	for _, s := range []string{"html", "main", "navbarcon", "B-Haven", "h1", "col2"} {
		if !isNameRef(s) {
			t.Errorf("isNameRef(%q) = false, want true", s)
		}
	}
	// Invalid name refs (URLs, paths, CSS, filenames)
	for _, s := range []string{"/packages", "#333", ".card", "tel:123", "http://x", "logo.png", "1.5rem", "", "hello world"} {
		if isNameRef(s) {
			t.Errorf("isNameRef(%q) = true, want false", s)
		}
	}
}

func TestInlineLinksIteration(t *testing.T) {
	// Test that inline map content with a $key/$value format definition
	// produces one tag per map entry (not a single empty tag).
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, "html.yaml"), []byte("html:\n - body\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "body.yaml"), []byte("body:\n - main\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "links.yaml"), []byte("^links:\n  tag: a\n  params:\n    href: '$key'\n  contents: '$value'\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "index.yaml"), []byte("main:\n  links:\n    About: about\n    Contact: contact\n"), 0644)

	output, _ := renderYAMLPage(tmpDir, filepath.Join(tmpDir, "index.yaml"), false, 1, nil)

	if !strings.Contains(output, `href="About"`) {
		t.Errorf("Missing <a> tag with href=\"About\" from links iteration.\nOutput:\n%s", output)
	}
	if !strings.Contains(output, `href="Contact"`) {
		t.Errorf("Missing <a> tag with href=\"Contact\" from links iteration.\nOutput:\n%s", output)
	}
	if !strings.Contains(output, ">about</a>") {
		t.Errorf("Missing 'about' text content in <a> tag from links iteration.\nOutput:\n%s", output)
	}
	if !strings.Contains(output, ">contact</a>") {
		t.Errorf("Missing 'contact' text content in <a> tag from links iteration.\nOutput:\n%s", output)
	}
}

func TestUlistWrappingLinks(t *testing.T) {
	// Test that ulist wrapping links produces one <li> per link entry.
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, "html.yaml"), []byte("html:\n - body\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "body.yaml"), []byte("body:\n - main\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "ulist.yaml"), []byte("^ulist:\n  tag: ul\n  contents:\n    li: '$*'\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "links.yaml"), []byte("^links:\n  tag: a\n  params:\n    href: '$key'\n  contents: '$value'\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "index.yaml"), []byte("main:\n  ulist:\n    links:\n      About: about\n      Contact: contact\n"), 0644)

	output, _ := renderYAMLPage(tmpDir, filepath.Join(tmpDir, "index.yaml"), false, 1, nil)

	if strings.Contains(output, "Undefined name") {
		t.Errorf("Unexpected 'Undefined name' error in output.\nOutput:\n%s", output)
	}
	if !strings.Contains(output, "<li>") {
		t.Errorf("Missing <li> tags from ulist-wrapped links.\nOutput:\n%s", output)
	}
	if !strings.Contains(output, ">about</a>") {
		t.Errorf("Missing 'about' link text in ulist-wrapped links.\nOutput:\n%s", output)
	}
	if !strings.Contains(output, ">contact</a>") {
		t.Errorf("Missing 'contact' link text in ulist-wrapped links.\nOutput:\n%s", output)
	}
}

func TestContentWrapPlural(t *testing.T) {
	// Test that "contents:" (plural) in a format definition wraps each
	// list item individually.
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output, _ := renderYAMLPage(docRoot, indexPath, false, 1, nil)

	// The index.yaml uses ulist which has contents: (plural) with li: '$*'
	if !strings.Contains(output, "<ul>") {
		t.Error("Missing <ul> tag from ulist format")
	}
	if !strings.Contains(output, "<li>") {
		t.Error("Missing <li> tags from ulist format")
	}
	// Verify individual items are wrapped
	if !strings.Contains(output, "YAML-driven page generation") {
		t.Error("Missing first feature list item")
	}
}

func TestContentWrapSingular(t *testing.T) {
	// Test that "content:" (singular) wraps all content as a single block.
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, "html.yaml"), []byte("html:\n - body\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "body.yaml"), []byte("body:\n - main\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "wrapper.yaml"), []byte("^wrapper:\n  tag: div\n  content:\n    span: '$*'\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "index.yaml"), []byte("main:\n  wrapper:\n    - item one\n    - item two\n    - item three\n"), 0644)

	output, _ := renderYAMLPage(tmpDir, filepath.Join(tmpDir, "index.yaml"), false, 1, nil)

	if !strings.Contains(output, "<div>") {
		t.Errorf("Missing <div> tag from wrapper format.\nOutput:\n%s", output)
	}
	if !strings.Contains(output, "<span>") {
		t.Errorf("Missing <span> tag from singular content wrapper.\nOutput:\n%s", output)
	}
	// Count <span> occurrences - should be exactly 1 (singular wrapping)
	spanCount := strings.Count(output, "<span>")
	if spanCount != 1 {
		t.Errorf("Expected 1 <span> (singular content wrap), got %d.\nOutput:\n%s", spanCount, output)
	}
}

func TestPreTagNoIndent(t *testing.T) {
	// Test that <pre> tag content is not indented (whitespace is significant).
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, "html.yaml"), []byte("html:\n - body\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "body.yaml"), []byte("body:\n - main\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "index.yaml"), []byte("main:\n  pre: \"line one\\n  indented line\"\n"), 0644)

	output, _ := renderYAMLPage(tmpDir, filepath.Join(tmpDir, "index.yaml"), false, 1, nil)

	idx := strings.Index(output, "<pre>")
	if idx < 0 {
		t.Fatalf("Missing <pre> tag.\nOutput:\n%s", output)
	}
	endIdx := strings.Index(output[idx:], "</pre>")
	if endIdx < 0 {
		t.Fatalf("Missing </pre> tag.\nOutput:\n%s", output)
	}
	preBlock := output[idx : idx+endIdx+len("</pre>")]

	// The first content line after <pre>\n must NOT be indented
	lines := strings.Split(preBlock, "\n")
	if len(lines) < 2 {
		t.Fatalf("Expected multi-line <pre> block, got: %q", preBlock)
	}
	firstContentLine := lines[1]
	if strings.HasPrefix(firstContentLine, " ") {
		t.Errorf("First line of <pre> content is indented: %q\nFull pre block:\n%s", firstContentLine, preBlock)
	}

	// The </pre> closing tag must NOT be indented (would add trailing spaces to content)
	closeIdx := strings.Index(output, "</pre>")
	if closeIdx > 0 {
		// Walk back to find start of the line containing </pre>
		lineStart := strings.LastIndex(output[:closeIdx], "\n")
		if lineStart >= 0 {
			before := output[lineStart+1 : closeIdx]
			if strings.TrimSpace(before) == "" && len(before) > 0 {
				t.Errorf("</pre> closing tag is indented (would add spaces to content): %q", before)
			}
		}
	}
}

func TestContentsWrapPluralList(t *testing.T) {
	// Test that "contents:" (plural) wraps each list item individually.
	tmpDir := t.TempDir()

	os.WriteFile(filepath.Join(tmpDir, "html.yaml"), []byte("html:\n - body\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "body.yaml"), []byte("body:\n - main\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "items.yaml"), []byte("^items:\n  tag: div\n  contents:\n    span: '$*'\n"), 0644)
	os.WriteFile(filepath.Join(tmpDir, "index.yaml"), []byte("main:\n  items:\n    - first\n    - second\n    - third\n"), 0644)

	output, _ := renderYAMLPage(tmpDir, filepath.Join(tmpDir, "index.yaml"), false, 1, nil)

	if !strings.Contains(output, "<div>") {
		t.Errorf("Missing <div> tag from items format.\nOutput:\n%s", output)
	}
	spanCount := strings.Count(output, "<span>")
	if spanCount != 3 {
		t.Errorf("Expected 3 <span> tags (plural contents wrap), got %d.\nOutput:\n%s", spanCount, output)
	}
	if !strings.Contains(output, ">first</span>") {
		t.Errorf("Missing 'first' wrapped in span.\nOutput:\n%s", output)
	}
	if !strings.Contains(output, ">second</span>") {
		t.Errorf("Missing 'second' wrapped in span.\nOutput:\n%s", output)
	}
	if !strings.Contains(output, ">third</span>") {
		t.Errorf("Missing 'third' wrapped in span.\nOutput:\n%s", output)
	}
}

// --- Error path tests ---

func TestMissingHTMLYAMLProducesOutput(t *testing.T) {
	// A site without html.yaml should still produce some output (the
	// undefined name error box), not panic or produce empty output.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.yaml"), []byte("main:\n  p: hello\n"), 0644)

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if output == "" {
		t.Error("expected non-empty output even without html.yaml")
	}
	if !strings.Contains(output, "<!DOCTYPE html>") {
		t.Error("expected DOCTYPE even when html.yaml is missing")
	}
}

func TestMalformedYAMLDoesNotPanic(t *testing.T) {
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n  p: hello\n",
		"bad.yaml":   ":\n  invalid:\n\t\tbad",
	})

	// Should not panic when encountering malformed YAML
	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "hello") {
		t.Error("valid content should still render despite bad sibling YAML file")
	}
}

func TestYAMLParseErrorDisplayed(t *testing.T) {
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n - broken\n",
		"broken.yaml": "broken:\n\t- invalid yaml\n  mixed indent",
	})

	// When a YAML file has a parse error, it should show a visible error
	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "YAML error") {
		t.Error("expected visible YAML error message in output")
	}
	if !strings.Contains(output, "broken.yaml") {
		t.Error("expected error to mention the filename broken.yaml")
	}
}

func TestYAMLParseErrorPageFile(t *testing.T) {
	// When the PAGE FILE itself has a parse error, the error should still
	// appear even though the undefined name (main) doesn't match the
	// filename (index.yaml).
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n\t- invalid yaml\n  mixed indent",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "YAML error") {
		t.Errorf("expected YAML error block for page file parse error, got: %s", output)
	}
	if !strings.Contains(output, "index.yaml") {
		t.Errorf("expected error to mention index.yaml, got: %s", output)
	}
	// Should NOT show generic "Undefined name: main" — YAML error is more informative
	if strings.Contains(output, "Undefined name") {
		t.Error("should show YAML error, not generic 'Undefined name'")
	}
}

func TestYAMLParseErrorWithScript(t *testing.T) {
	// When a name has a script-based format (^name with script:) but
	// the data file has a YAML error, the error should be shown instead
	// of running the script with no data.
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n - items\n",
		"items.yaml": "items:\n\t- invalid yaml\n  mixed indent",
	})
	// Add a format with script for "items"
	os.WriteFile(filepath.Join(dir, "html.yaml"),
		[]byte("html:\n - body\n\n^items:\n  script: python\n  code: |\n    print('should not run')\n"), 0644)

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "YAML error") {
		t.Errorf("expected YAML error block instead of running script, got: %s", output)
	}
}

func TestYAMLParseErrorInDebug(t *testing.T) {
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n - broken\n",
		"broken.yaml": "broken:\n\t- invalid yaml\n  mixed indent",
	})

	// In debug mode, YAML parse errors should appear in HTML comments
	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), true, 1, nil)
	if !strings.Contains(output, "YAML parse error") {
		t.Error("expected YAML parse error in debug comment")
	}
}

func TestYAMLParseErrorDashPrefix(t *testing.T) {
	// When a page has unquoted text starting with "- " (which looks like
	// a YAML list entry), the parse error must be visible in HTML output.
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n  - h1: Service Information\n  - p: - To get on our schedule.\n",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "YAML error") {
		t.Errorf("expected visible YAML error for unquoted dash prefix, got: %s", output)
	}
}

func TestYAMLParseErrorDashPrefixSeparateFile(t *testing.T) {
	// Same as above, but the parse error is in a separate data file
	// referenced by the page.
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml":   "main:\n - service\n",
		"service.yaml": "service:\n  - h1: Service\n  - p: - To get on our schedule.\n",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "YAML error") {
		t.Errorf("expected visible YAML error for separate file, got: %s", output)
	}
}

func TestCircularReferenceHandled(t *testing.T) {
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n - alpha\n",
		"alpha.yaml": "alpha:\n - beta\n",
		"beta.yaml":  "beta:\n - alpha\n",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	// Should produce output without hanging
	if output == "" {
		t.Error("expected non-empty output for circular reference")
	}
	if strings.Contains(output, "render depth exceeded") {
		// This is acceptable behavior
		return
	}
	// Circular reference comment is also acceptable
	if strings.Contains(output, "circular reference") {
		return
	}
	// If neither marker is present, the rendering still completed which is fine
}

func TestDebugModeProducesComments(t *testing.T) {
	docRoot := defaultDocRoot(t)
	indexPath := filepath.Join(docRoot, "index.yaml")
	output, _ := renderYAMLPage(docRoot, indexPath, true, 1, nil)

	if !strings.Contains(output, "<!-- resolve") {
		t.Error("debug mode should produce resolve comments")
	}
}

func TestComputeRequestURI(t *testing.T) {
	tests := []struct {
		docRoot  string
		reqPath  string
		expected string
	}{
		{"/srv/default", "/srv/default/index.yaml", "/"},
		{"/srv/default", "/srv/default/about.yaml", "/about"},
		{"/srv/default", "/srv/default/sub/index.yaml", "/sub"},
		{"/srv/default", "/srv/default/sub/sub.yaml", "/sub"},
		{"/srv/default", "/srv/default/getting-started.md", "/getting-started"},
	}
	for _, tc := range tests {
		got := computeRequestURI(tc.docRoot, tc.reqPath)
		if got != tc.expected {
			t.Errorf("computeRequestURI(%q, %q) = %q, want %q", tc.docRoot, tc.reqPath, got, tc.expected)
		}
	}
}

func TestRenderStyleYAML(t *testing.T) {
	om := NewOrderedMap()
	inner := NewOrderedMap()
	inner.Set("color", "red")
	om.Set("body", inner)

	css := renderStyleYAML(om, 0)
	if !strings.Contains(css, "body") {
		t.Error("CSS missing selector 'body'")
	}
	if !strings.Contains(css, "color: red") {
		t.Error("CSS missing 'color: red' property")
	}
}

// --- Error page tests ---

func TestRenderErrorPageDefault(t *testing.T) {
	docRoot := defaultDocRoot(t)
	output, _ := renderErrorPage(docRoot, 404, "", false, 1, nil)

	if output == "" {
		t.Fatal("renderErrorPage returned empty string — error.yaml not found")
	}
	if !strings.Contains(output, "<!DOCTYPE html>") {
		t.Error("error page missing DOCTYPE")
	}
	if !strings.Contains(output, "404") {
		t.Error("error page missing status code 404")
	}
	if !strings.Contains(output, "Not Found") {
		t.Error("error page missing 'Not Found' description")
	}
	if !strings.Contains(output, "<h1>") {
		t.Error("error page missing <h1> tag from $errortitle")
	}
	if !strings.Contains(output, "<p>") {
		t.Error("error page missing <p> tag from $errormessage")
	}
	// Default message should include the request path
	if !strings.Contains(output, "requested page") {
		t.Error("default error message should describe the requested page")
	}
}

func TestRenderErrorPageWithMessage(t *testing.T) {
	docRoot := defaultDocRoot(t)
	output, _ := renderErrorPage(docRoot, 500, "database connection failed", false, 1, nil)

	if output == "" {
		t.Fatal("renderErrorPage returned empty string")
	}
	if !strings.Contains(output, "500") {
		t.Error("error page missing status code 500")
	}
	if !strings.Contains(output, "Internal Server Error") {
		t.Error("error page missing status description")
	}
	if !strings.Contains(output, "database connection failed") {
		t.Error("error page missing custom message")
	}
}

func TestRenderErrorPageSpecificOverride(t *testing.T) {
	// error404.yaml uses a ^error404 format with a marker class "specific-404"
	// error.yaml uses a ^error format with a marker class "generic-error"
	// This lets us distinguish which template was used.
	dir := setupMinimalSite(t, map[string]string{
		"error.yaml":    "^error:\n  tag: div\n  params:\n    class: generic-error\n  content:\n    - h1: $errortitle\n\nerror:\n",
		"error404.yaml": "^error404:\n  tag: div\n  params:\n    class: specific-404\n  content:\n    - p: $errormessage\n\nerror404:\n",
	})

	// 404 should use error404.yaml (specific): has class="specific-404"
	output, _ := renderErrorPage(dir, 404, "", false, 1, nil)
	if !strings.Contains(output, "specific-404") {
		t.Errorf("expected error404.yaml template for 404, got:\n%s", output)
	}

	// 500 should fall back to generic error.yaml: has class="generic-error"
	output500, _ := renderErrorPage(dir, 500, "", false, 1, nil)
	if !strings.Contains(output500, "generic-error") {
		t.Errorf("expected error.yaml template for 500, got:\n%s", output500)
	}
}

func TestRenderErrorPageNoTemplate(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "html.yaml"), []byte("html:\n - body\n"), 0644)
	os.WriteFile(filepath.Join(dir, "body.yaml"), []byte("body:\n - main\n"), 0644)
	// No error.yaml

	output, _ := renderErrorPage(dir, 404, "", false, 1, nil)
	if output != "" {
		t.Errorf("expected empty string when no error template exists, got:\n%s", output)
	}
}

func TestRenderErrorPageTitleOverridden(t *testing.T) {
	docRoot := defaultDocRoot(t)
	output, _ := renderErrorPage(docRoot, 404, "", false, 1, nil)

	// The page title should show the error, not "bserver" from title.yaml
	if !strings.Contains(output, "<title>") {
		t.Error("error page missing <title> tag")
	}
	if strings.Contains(output, "<title>bserver</title>") {
		t.Error("error page title should be overridden, not 'bserver'")
	}
}

func TestRenderErrorPageHasNavbar(t *testing.T) {
	docRoot := defaultDocRoot(t)
	output, _ := renderErrorPage(docRoot, 404, "", false, 1, nil)

	// Error page should still have the site chrome (navbar, footer)
	if !strings.Contains(output, "navbar") {
		t.Error("error page missing navbar from site structure")
	}
}

// --- Benchmarks ---

func BenchmarkRenderYAMLPage(b *testing.B) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	indexPath := filepath.Join(docRoot, "index.yaml")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		renderYAMLPage(docRoot, indexPath, false, 1, nil)
	}
}

func TestMarkupMarkdownNamed(t *testing.T) {
	// Test markup: markdown via a named definition (markdown: |)
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml":    "main:\n - markdown\n",
		"markdown.yaml": "^markdown:\n  markup: markdown\nmarkdown: |\n  # Hello World\n  This is **bold** text.\n",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "<h1>Hello World</h1>") {
		t.Errorf("expected <h1> from markdown heading, got: %s", output)
	}
	if !strings.Contains(output, "<strong>bold</strong>") {
		t.Errorf("expected <strong> from markdown bold, got: %s", output)
	}
}

func TestMarkupMarkdownInline(t *testing.T) {
	// Test markup: markdown via inline map entry {markdown: "text"}
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml":    "main:\n  - markdown: |\n      # Inline Heading\n      A paragraph with *emphasis*.\n",
		"markdown.yaml": "^markdown:\n  markup: markdown\n",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "<h1>Inline Heading</h1>") {
		t.Errorf("expected <h1> from inline markdown, got: %s", output)
	}
	if !strings.Contains(output, "<em>emphasis</em>") {
		t.Errorf("expected <em> from inline markdown, got: %s", output)
	}
}

func TestMarkupMarkdownWithTag(t *testing.T) {
	// Test markup: markdown with a tag wrapping the output
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml":   "main:\n  - article: |\n      ## Section\n      Some text.\n",
		"article.yaml": "^article:\n  tag: article\n  markup: markdown\n",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "<article>") {
		t.Errorf("expected <article> tag wrapper, got: %s", output)
	}
	if !strings.Contains(output, "<h2>Section</h2>") {
		t.Errorf("expected <h2> from markdown, got: %s", output)
	}
}

func TestPhpContentAsCode(t *testing.T) {
	// Test ^php: { script: php } where the content provides the code.
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n  - php: |\n      echo \"<p>Hello from PHP</p>\";\n",
		"php.yaml":   "^php:\n  script: php\n",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "<p>Hello from PHP</p>") {
		t.Errorf("expected PHP output, got: %s", output)
	}
}

func TestPhpContentAsCodeStripTags(t *testing.T) {
	// Test that <?php ?> tags are stripped from content-as-code.
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n  - php: |\n      <?php echo \"<p>tagged</p>\"; ?>\n",
		"php.yaml":   "^php:\n  script: php\n",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "<p>tagged</p>") {
		t.Errorf("expected PHP output from tagged code, got: %s", output)
	}
}

func BenchmarkRenderMarkdownPage(b *testing.B) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	mdPath := filepath.Join(docRoot, "getting-started.md")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		renderMarkdownPage(docRoot, mdPath, false, 1, nil)
	}
}
