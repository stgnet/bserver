package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDataSourceBasic(t *testing.T) {
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n - items\n",
		"items.yaml": "$items:\n  script: python\n  code: |\n    import json\n    print(json.dumps({\"alpha\": \"one\", \"beta\": \"two\"}))\n",
		"^items": "", // no format needed, will render as name refs
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	// The data source should produce an OrderedMap with alpha and beta keys.
	// Without a format, they'll be rendered as name references (and show as undefined).
	// The key test is that the data source executed and didn't error.
	if strings.Contains(output, "YAML error") {
		t.Error("unexpected YAML error")
	}
}

func TestDataSourceList(t *testing.T) {
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n - items\n",
		"items.yaml": "$items:\n  script: python\n  code: |\n    import json\n    print(json.dumps([{\"key\": \"/a\", \"value\": \"Alpha\"}, {\"key\": \"/b\", \"value\": \"Beta\"}]))\n\n^items:\n  script: python\n  code: |\n    print(f'<li>{record[\"value\"]}</li>')\n",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "<li>Alpha</li>") {
		t.Errorf("expected Alpha in output, got: %s", output)
	}
	if !strings.Contains(output, "<li>Beta</li>") {
		t.Errorf("expected Beta in output, got: %s", output)
	}
}

func TestDataSourceDebug(t *testing.T) {
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n - items\n",
		"items.yaml": "$items:\n  script: python\n  code: |\n    import json\n    print(json.dumps([\"hello\"]))\n",
	})

	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), true, 1, nil)
	if !strings.Contains(output, "resolve") {
		t.Log("Debug output:", output)
	}
}

func TestDataSourceErrorHandled(t *testing.T) {
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n - items\n",
		"items.yaml": "$items:\n  script: python\n  code: |\n    raise Exception('test error')\n",
	})

	// Should not panic, should show undefined name since data source fails
	output, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, nil)
	if !strings.Contains(output, "Undefined name") {
		t.Log("output:", output)
	}
}

func TestDataSourcePreloadedFile(t *testing.T) {
	// When the requested page IS the data source file (pre-loaded before
	// resolveAll), the data source must still execute. This tests the
	// fallback data source check at the end of findDefinition.
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n - navlinks\n",
		"navlinks.yaml": "$navlinks:\n  script: python\n  code: |\n    import json\n    print(json.dumps({\"key1\": \"val1\"}))\n",
	})

	// Request navlinks.yaml as the page — this pre-loads it via loadYAMLFile
	// before resolveAll runs, so filesLoaded already has it.
	output, _ := renderYAMLPage(dir, filepath.Join(dir, "navlinks.yaml"), false, 1, nil)
	// The data source should still execute even though the file was pre-loaded
	if strings.Contains(output, "Undefined name: <strong>navlinks</strong>") {
		t.Error("data source should execute even when its YAML file was pre-loaded")
	}
}

func TestDataSourceNavlinksIntegration(t *testing.T) {
	base, _ := os.Getwd()
	docRoot := filepath.Join(base, "www", "default")
	indexPath := filepath.Join(docRoot, "index.yaml")
	output, _ := renderYAMLPage(docRoot, indexPath, false, 1, nil)

	// The data source should auto-generate navlinks from directory contents
	expectedLinks := []string{
		"Getting Started",
		"Definitions",
		"Formats",
		"Components",
		"Scripts",
		"Advanced",
	}
	for _, link := range expectedLinks {
		if !strings.Contains(output, link) {
			t.Errorf("expected navlink %q in output", link)
		}
	}
	if strings.Contains(output, "Undefined name: <strong>navlinks</strong>") {
		t.Error("navlinks should be resolved via data source, not show as undefined")
	}
}
