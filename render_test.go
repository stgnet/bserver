package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHomepageContent(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output := renderYAMLPage(docRoot, indexPath, false, 1, nil)

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
	if !strings.Contains(output, "Undefined name") {
		// This is intentionally inverted - we want NO undefined names
	} else {
		// But if there are, that's a real error
	}
	// Verify no undefined name errors
	if strings.Contains(output, "Undefined name") {
		t.Errorf("Homepage has undefined name errors.\nOutput:\n%s", output)
	}
}

func TestFooterContent(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output := renderYAMLPage(docRoot, indexPath, false, 1, nil)

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
	docRoot := filepath.Join(base, "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output := renderYAMLPage(docRoot, indexPath, false, 1, nil)

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
	docRoot := filepath.Join(base, "default")
	mdPath := filepath.Join(docRoot, "getting-started.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("getting-started.md not found")
	}
	output := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

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
	docRoot := filepath.Join(base, "default")
	mdPath := filepath.Join(docRoot, "definitions.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("definitions.md not found")
	}
	output := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

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
	docRoot := filepath.Join(base, "default")
	mdPath := filepath.Join(docRoot, "formats.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("formats.md not found")
	}
	output := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

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
	docRoot := filepath.Join(base, "default")
	mdPath := filepath.Join(docRoot, "components.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("components.md not found")
	}
	output := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

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
	docRoot := filepath.Join(base, "default")
	mdPath := filepath.Join(docRoot, "scripts.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("scripts.md not found")
	}
	output := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

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
	docRoot := filepath.Join(base, "default")
	mdPath := filepath.Join(docRoot, "advanced.md")
	if _, err := os.Stat(mdPath); err != nil {
		t.Fatal("advanced.md not found")
	}
	output := renderMarkdownPage(docRoot, mdPath, false, 1, nil)

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
	docRoot := filepath.Join(base, "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output := renderYAMLPage(docRoot, indexPath, false, 1, nil)

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

	output := renderYAMLPage(tmpDir, filepath.Join(tmpDir, "index.yaml"), false, 1, nil)

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

	output := renderYAMLPage(tmpDir, filepath.Join(tmpDir, "index.yaml"), false, 1, nil)

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
	docRoot := filepath.Join(base, "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output := renderYAMLPage(docRoot, indexPath, false, 1, nil)

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

	output := renderYAMLPage(tmpDir, filepath.Join(tmpDir, "index.yaml"), false, 1, nil)

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

	output := renderYAMLPage(tmpDir, filepath.Join(tmpDir, "index.yaml"), false, 1, nil)

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

	output := renderYAMLPage(tmpDir, filepath.Join(tmpDir, "index.yaml"), false, 1, nil)

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
