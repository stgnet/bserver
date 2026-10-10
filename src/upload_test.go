package main

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// buildMultipart builds a multipart/form-data request body. fields are plain
// form values; files maps a field name to {filename, content} entries — more
// than one entry under a field name exercises the array-of-files case.
func buildMultipart(t *testing.T, fields map[string]string, files map[string][][2]string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := mw.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	for field, entries := range files {
		for _, e := range entries {
			fw, err := mw.CreateFormFile(field, e[0])
			if err != nil {
				t.Fatal(err)
			}
			if _, err := fw.Write([]byte(e[1])); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("POST", "/", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	return req
}

// indentPHP indents each line of PHP code by six spaces so it nests under the
// YAML block scalar `- php: |`.
func indentPHP(code string) string {
	var sb strings.Builder
	for _, line := range strings.Split(strings.TrimRight(code, "\n"), "\n") {
		sb.WriteString("      ")
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	return sb.String()
}

// renderUploadPage renders a YAML page with an embedded PHP dumper against the
// given request and returns the produced HTML. It skips when PHP is absent.
func renderUploadPage(t *testing.T, phpDumper string, req *http.Request) string {
	t.Helper()
	if findScriptInterpreter("php") == "" {
		t.Skip("php interpreter not found")
	}
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n  - php: |\n" + indentPHP(phpDumper),
		"php.yaml":   "^php:\n  script: php\n",
	})
	output, _, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, req)
	return output
}

func TestUploadSingleFile(t *testing.T) {
	dumper := `
if (isset($_FILES['photo'])) {
  echo 'NAME=' . $_FILES['photo']['name'] . ';';
  echo 'TYPE=' . $_FILES['photo']['type'] . ';';
  echo 'SIZE=' . $_FILES['photo']['size'] . ';';
  echo 'ERROR=' . $_FILES['photo']['error'] . ';';
  echo 'CONTENT=' . file_get_contents($_FILES['photo']['tmp_name']) . ';';
} else {
  echo 'NO_FILE;';
}
`
	req := buildMultipart(t,
		map[string]string{"caption": "my picture"},
		map[string][][2]string{"photo": {{"cat.png", "PNGDATA"}}},
	)
	out := renderUploadPage(t, dumper, req)
	for _, want := range []string{
		"NAME=cat.png;",
		"TYPE=application/octet-stream;", // multipart.CreateFormFile sets octet-stream
		"SIZE=7;",
		"ERROR=0;",
		"CONTENT=PNGDATA;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got: %s", want, out)
		}
	}
}

func TestUploadPopulatesPost(t *testing.T) {
	req := buildMultipart(t,
		map[string]string{"caption": "hello world"},
		map[string][][2]string{"photo": {{"cat.png", "PNGDATA"}}},
	)
	out := renderUploadPage(t, `echo 'CAPTION=' . ($_POST['caption'] ?? 'MISSING') . ';';`, req)
	if !strings.Contains(out, "CAPTION=hello world;") {
		t.Errorf("expected multipart form field in $_POST, got: %s", out)
	}
}

func TestUploadArrayOfFiles(t *testing.T) {
	dumper := `
$n = count($_FILES['docs']['name']);
echo 'COUNT=' . $n . ';';
for ($i = 0; $i < $n; $i++) {
  echo 'F' . $i . '=' . $_FILES['docs']['name'][$i] . ':' . file_get_contents($_FILES['docs']['tmp_name'][$i]) . ';';
}
`
	req := buildMultipart(t, nil,
		map[string][][2]string{"docs[]": {{"a.txt", "AAA"}, {"b.txt", "BBB"}}},
	)
	out := renderUploadPage(t, dumper, req)
	for _, want := range []string{"COUNT=2;", "F0=a.txt:AAA;", "F1=b.txt:BBB;"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got: %s", want, out)
		}
	}
}

func TestUploadTempFileCleanedUp(t *testing.T) {
	// tmp_name must point at a real file during the request; after the request
	// the shutdown handler unlinks it (unless user code moved it).
	dumper := `
$p = $_FILES['photo']['tmp_name'];
echo 'EXISTS=' . (is_file($p) ? '1' : '0') . ';';
echo 'PATH=' . $p . ';';
`
	req := buildMultipart(t, nil,
		map[string][][2]string{"photo": {{"cat.png", "DATA"}}},
	)
	out := renderUploadPage(t, dumper, req)
	if !strings.Contains(out, "EXISTS=1;") {
		t.Errorf("expected tmp file to exist during request, got: %s", out)
	}
	marker := "PATH="
	idx := strings.Index(out, marker)
	if idx < 0 {
		t.Fatalf("no PATH marker in output: %s", out)
	}
	rest := out[idx+len(marker):]
	end := strings.Index(rest, ";")
	if end < 0 {
		t.Fatalf("malformed PATH marker: %s", out)
	}
	tmpPath := rest[:end]
	if tmpPath == "" {
		t.Fatalf("empty tmp path: %s", out)
	}
	if _, err := os.Stat(tmpPath); err == nil {
		t.Errorf("temp upload file %q should have been cleaned up after request", tmpPath)
	}
}

func TestUploadNoFileSelected(t *testing.T) {
	// An empty file input (filename="") is reported as UPLOAD_ERR_NO_FILE (4).
	if findScriptInterpreter("php") == "" {
		t.Skip("php interpreter not found")
	}
	dir := setupMinimalSite(t, map[string]string{
		"index.yaml": "main:\n  - php: |\n" + indentPHP(
			`echo 'ERROR=' . $_FILES['photo']['error'] . ';';`),
		"php.yaml": "^php:\n  script: php\n",
	})
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	// A file part with an empty filename, mimicking a form submitted with no
	// file chosen.
	part, err := mw.CreatePart(map[string][]string{
		"Content-Disposition": {`form-data; name="photo"; filename=""`},
	})
	if err != nil {
		t.Fatal(err)
	}
	part.Write(nil)
	mw.Close()
	req := httptest.NewRequest("POST", "/", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	out, _, _ := renderYAMLPage(dir, filepath.Join(dir, "index.yaml"), false, 1, req)
	if !strings.Contains(out, "ERROR=4;") {
		t.Errorf("expected UPLOAD_ERR_NO_FILE (4) for empty filename, got: %s", out)
	}
}
