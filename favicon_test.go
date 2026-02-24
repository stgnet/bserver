package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestParseColor(t *testing.T) {
	tests := []struct {
		input string
		want  color.RGBA
	}{
		{"black", color.RGBA{0, 0, 0, 255}},
		{"white", color.RGBA{255, 255, 255, 255}},
		{"red", color.RGBA{255, 0, 0, 255}},
		{"yellow", color.RGBA{255, 255, 0, 255}},
		{"#ff0000", color.RGBA{255, 0, 0, 255}},
		{"#f00", color.RGBA{255, 0, 0, 255}},
		{"#00ff00", color.RGBA{0, 255, 0, 255}},
		{"", color.RGBA{0, 0, 0, 255}},
		{"  White  ", color.RGBA{255, 255, 255, 255}},
		{"unknown", color.RGBA{0, 0, 0, 255}},
		{"#abc", color.RGBA{0xaa, 0xbb, 0xcc, 255}},
	}
	for _, tt := range tests {
		got := parseColor(tt.input)
		if got != tt.want {
			t.Errorf("parseColor(%q) = %v, want %v", tt.input, got, tt.want)
		}
	}
}

func TestDocRootToFaviconText(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/srv/stg.net", "STG"},
		{"/srv/default", "DEF"},
		{"/srv/example.com", "EXA"},
		{"/srv/ab", "AB"},
		{"/srv/a", "A"},
		{"/srv/longdomainname.org", "LON"},
	}
	for _, tt := range tests {
		got := docRootToFaviconText(tt.input)
		if got != tt.want {
			t.Errorf("docRootToFaviconText(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestLoadFaviconConfigDefaults(t *testing.T) {
	dir := t.TempDir()
	// Create a "stg.net" subdirectory to simulate docRoot
	docRoot := filepath.Join(dir, "stg.net")
	os.Mkdir(docRoot, 0755)

	cfg, yamlTime := loadFaviconConfig(docRoot)
	if cfg.Text != "STG" {
		t.Errorf("default text = %q, want %q", cfg.Text, "STG")
	}
	if cfg.Color != "white" {
		t.Errorf("default color = %q, want %q", cfg.Color, "white")
	}
	if cfg.Background != "black" {
		t.Errorf("default background = %q, want %q", cfg.Background, "black")
	}
	if !yamlTime.IsZero() {
		t.Errorf("yaml time should be zero when no file exists")
	}
}

func TestLoadFaviconConfigFromFile(t *testing.T) {
	dir := t.TempDir()
	docRoot := filepath.Join(dir, "mysite.com")
	os.Mkdir(docRoot, 0755)
	os.WriteFile(filepath.Join(docRoot, "_favicon.yaml"), []byte(
		"text: BS\ncolor: yellow\nbackground: black\n"), 0644)

	cfg, yamlTime := loadFaviconConfig(docRoot)
	if cfg.Text != "BS" {
		t.Errorf("text = %q, want %q", cfg.Text, "BS")
	}
	if cfg.Color != "yellow" {
		t.Errorf("color = %q, want %q", cfg.Color, "yellow")
	}
	if cfg.Background != "black" {
		t.Errorf("background = %q, want %q", cfg.Background, "black")
	}
	if yamlTime.IsZero() {
		t.Error("yaml time should not be zero when file exists")
	}
}

func TestLoadFaviconConfigImageMode(t *testing.T) {
	dir := t.TempDir()
	docRoot := filepath.Join(dir, "site.com")
	os.Mkdir(docRoot, 0755)
	os.WriteFile(filepath.Join(docRoot, "_favicon.yaml"), []byte(
		"image: logo.png\nfit: crop\n"), 0644)

	cfg, _ := loadFaviconConfig(docRoot)
	if cfg.Image != "logo.png" {
		t.Errorf("image = %q, want %q", cfg.Image, "logo.png")
	}
	if cfg.Fit != "crop" {
		t.Errorf("fit = %q, want %q", cfg.Fit, "crop")
	}
}

func TestGenerateTextFavicon(t *testing.T) {
	data, err := generateTextFavicon("BS", "yellow", "black")
	if err != nil {
		t.Fatalf("generateTextFavicon: %v", err)
	}
	validateICO(t, data, len(faviconSizes))
}

func TestGenerateTextFaviconSingleChar(t *testing.T) {
	data, err := generateTextFavicon("X", "white", "blue")
	if err != nil {
		t.Fatalf("generateTextFavicon: %v", err)
	}
	validateICO(t, data, len(faviconSizes))
}

func TestGenerateTextFaviconThreeChars(t *testing.T) {
	data, err := generateTextFavicon("DEF", "white", "black")
	if err != nil {
		t.Fatalf("generateTextFavicon: %v", err)
	}
	validateICO(t, data, len(faviconSizes))
}

func TestGenerateImageFavicon(t *testing.T) {
	// Create a test PNG image (100x50 rectangle)
	dir := t.TempDir()
	imgPath := filepath.Join(dir, "test.png")
	img := image.NewRGBA(image.Rect(0, 0, 100, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 100; x++ {
			img.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	f, _ := os.Create(imgPath)
	png.Encode(f, img)
	f.Close()

	for _, fit := range []string{"contain", "crop", "stretch"} {
		t.Run(fit, func(t *testing.T) {
			data, err := generateImageFavicon(imgPath, fit)
			if err != nil {
				t.Fatalf("generateImageFavicon(%s): %v", fit, err)
			}
			validateICO(t, data, len(faviconSizes))
		})
	}
}

func TestContainToFit(t *testing.T) {
	// 200x100 source into 32x32 contain: should produce centered with padding
	src := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			src.Set(x, y, color.RGBA{255, 0, 0, 255})
		}
	}
	dst := containToFit(src, 32, 32)
	b := dst.Bounds()
	if b.Dx() != 32 || b.Dy() != 32 {
		t.Errorf("containToFit size = %dx%d, want 32x32", b.Dx(), b.Dy())
	}
	// Top-left corner should be transparent (padding area)
	r, _, _, a := dst.At(0, 0).RGBA()
	if a != 0 {
		t.Errorf("corner should be transparent, got RGBA(%d,_,_,%d)", r, a)
	}
}

func TestCropToFit(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			src.Set(x, y, color.RGBA{0, 255, 0, 255})
		}
	}
	dst := cropToFit(src, 32, 32)
	b := dst.Bounds()
	if b.Dx() != 32 || b.Dy() != 32 {
		t.Errorf("cropToFit size = %dx%d, want 32x32", b.Dx(), b.Dy())
	}
	// Center should be non-transparent (filled)
	_, _, _, a := dst.At(16, 16).RGBA()
	if a == 0 {
		t.Error("center should be opaque after crop")
	}
}

func TestStretchToFit(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for y := 0; y < 100; y++ {
		for x := 0; x < 200; x++ {
			src.Set(x, y, color.RGBA{0, 0, 255, 255})
		}
	}
	dst := stretchToFit(src, 48, 48)
	b := dst.Bounds()
	if b.Dx() != 48 || b.Dy() != 48 {
		t.Errorf("stretchToFit size = %dx%d, want 48x48", b.Dx(), b.Dy())
	}
}

func TestEncodeICO(t *testing.T) {
	images := make([]image.Image, 3)
	for i, s := range []int{16, 32, 48} {
		images[i] = image.NewRGBA(image.Rect(0, 0, s, s))
	}
	data, err := encodeICO(images)
	if err != nil {
		t.Fatalf("encodeICO: %v", err)
	}
	validateICO(t, data, 3)
}

func TestGetCachedFavicon(t *testing.T) {
	dir := t.TempDir()
	docRoot := filepath.Join(dir, "test.site")
	os.Mkdir(docRoot, 0755)

	// First call generates
	data1, err := getCachedFavicon(docRoot)
	if err != nil {
		t.Fatalf("getCachedFavicon: %v", err)
	}
	if len(data1) == 0 {
		t.Fatal("generated favicon is empty")
	}

	// Second call should return cached data
	data2, err := getCachedFavicon(docRoot)
	if err != nil {
		t.Fatalf("getCachedFavicon (cached): %v", err)
	}
	if !bytes.Equal(data1, data2) {
		t.Error("cached data differs from first generation")
	}

	// Clean up the sync.Map entry
	faviconCache.Delete(docRoot)
}

func TestServeFaviconHTTP(t *testing.T) {
	dir := t.TempDir()
	docRoot := filepath.Join(dir, "stg.net")
	os.Mkdir(docRoot, 0755)

	cfg := &config{
		Base:         dir,
		MaxStaticAge: 86400,
	}
	mux := &virtualHostMux{cfg: cfg}

	req := httptest.NewRequest("GET", "http://stg.net/favicon.ico", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	ct := rec.Header().Get("Content-Type")
	if ct != "image/x-icon" {
		t.Errorf("Content-Type = %q, want %q", ct, "image/x-icon")
	}
	validateICO(t, rec.Body.Bytes(), len(faviconSizes))

	// Clean up cache
	faviconCache.Delete(docRoot)
}

func TestServeFaviconRealFilePreferred(t *testing.T) {
	dir := t.TempDir()
	docRoot := filepath.Join(dir, "myhost")
	os.Mkdir(docRoot, 0755)

	// Write a fake favicon.ico file
	fakeICO := []byte("this is a real favicon.ico")
	os.WriteFile(filepath.Join(docRoot, "favicon.ico"), fakeICO, 0644)

	cfg := &config{
		Base:         dir,
		MaxStaticAge: 86400,
	}
	mux := &virtualHostMux{cfg: cfg}

	req := httptest.NewRequest("GET", "http://myhost/favicon.ico", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	// Should serve the real file, not a generated one
	if !bytes.Contains(rec.Body.Bytes(), []byte("this is a real favicon.ico")) {
		t.Error("expected real favicon.ico to be served, got generated content")
	}
}

// validateICO checks that data is a valid ICO file with the expected image count.
func validateICO(t *testing.T, data []byte, expectedCount int) {
	t.Helper()
	if len(data) < 6 {
		t.Fatalf("ICO too short: %d bytes", len(data))
	}

	r := bytes.NewReader(data)
	var reserved, icoType, count uint16
	binary.Read(r, binary.LittleEndian, &reserved)
	binary.Read(r, binary.LittleEndian, &icoType)
	binary.Read(r, binary.LittleEndian, &count)

	if reserved != 0 {
		t.Errorf("ICO reserved = %d, want 0", reserved)
	}
	if icoType != 1 {
		t.Errorf("ICO type = %d, want 1", icoType)
	}
	if int(count) != expectedCount {
		t.Errorf("ICO count = %d, want %d", count, expectedCount)
	}

	// Verify each directory entry points to valid PNG data
	for i := 0; i < int(count); i++ {
		var width, height, colors, resv uint8
		var planes, bpp uint16
		var size, offset uint32
		binary.Read(r, binary.LittleEndian, &width)
		binary.Read(r, binary.LittleEndian, &height)
		binary.Read(r, binary.LittleEndian, &colors)
		binary.Read(r, binary.LittleEndian, &resv)
		binary.Read(r, binary.LittleEndian, &planes)
		binary.Read(r, binary.LittleEndian, &bpp)
		binary.Read(r, binary.LittleEndian, &size)
		binary.Read(r, binary.LittleEndian, &offset)

		if int(offset)+int(size) > len(data) {
			t.Errorf("entry %d: offset+size (%d+%d) exceeds data length (%d)",
				i, offset, size, len(data))
			continue
		}

		// Check PNG magic bytes
		pngMagic := []byte{0x89, 'P', 'N', 'G'}
		entryData := data[offset : offset+size]
		if len(entryData) < 4 || !bytes.Equal(entryData[:4], pngMagic) {
			t.Errorf("entry %d: not valid PNG data", i)
		}
	}
}
