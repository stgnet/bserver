package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image"
	"image/color"
	imagedraw "image/draw"
	_ "image/gif"
	_ "image/jpeg"
	"image/png"
	"log"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	"gopkg.in/yaml.v3"
)

// faviconSizes are the standard ICO image sizes for maximum browser compatibility.
var faviconSizes = []int{16, 32, 48}

// faviconConfig holds the parsed _favicon.yaml configuration.
//
// Text mode (default):
//
//	text: BS
//	color: yellow
//	background: black
//
// Image mode:
//
//	image: logo.png
//	fit: contain
//
// Fit modes for images:
//   - contain (default): scale to fit within the square, preserving aspect ratio;
//     gaps are transparent
//   - crop: scale to fill the square, center-cropping any overflow
//   - stretch: distort to fill the square exactly
type faviconConfig struct {
	Text       string `yaml:"text"`
	Color      string `yaml:"color"`
	Background string `yaml:"background"`
	Image      string `yaml:"image"`
	Fit        string `yaml:"fit"`
}

// faviconCacheEntry stores generated favicon data with timestamps for invalidation.
type faviconCacheEntry struct {
	data      []byte
	yamlTime  time.Time // _favicon.yaml mod time (zero if using defaults)
	imageTime time.Time // source image mod time (zero if text mode)
}

var faviconCache sync.Map // docRoot -> *faviconCacheEntry

// Parsed Go Bold font (singleton).
var (
	parsedFaviconFont *opentype.Font
	faviconFontOnce   sync.Once
)

func getFaviconFont() *opentype.Font {
	faviconFontOnce.Do(func() {
		f, err := opentype.Parse(gobold.TTF)
		if err != nil {
			log.Printf("Warning: cannot parse favicon font: %v", err)
			return
		}
		parsedFaviconFont = f
	})
	return parsedFaviconFont
}

// namedColors maps common color names to RGBA values.
var namedColors = map[string]color.RGBA{
	"black":   {0, 0, 0, 255},
	"white":   {255, 255, 255, 255},
	"red":     {255, 0, 0, 255},
	"green":   {0, 128, 0, 255},
	"blue":    {0, 0, 255, 255},
	"yellow":  {255, 255, 0, 255},
	"cyan":    {0, 255, 255, 255},
	"magenta": {255, 0, 255, 255},
	"orange":  {255, 165, 0, 255},
	"purple":  {128, 0, 128, 255},
	"gray":    {128, 128, 128, 255},
	"grey":    {128, 128, 128, 255},
	"brown":   {139, 69, 19, 255},
	"pink":    {255, 192, 203, 255},
	"navy":    {0, 0, 128, 255},
	"teal":    {0, 128, 128, 255},
	"lime":    {0, 255, 0, 255},
	"maroon":  {128, 0, 0, 255},
	"olive":   {128, 128, 0, 255},
	"silver":  {192, 192, 192, 255},
}

// parseColor converts a color string to color.RGBA.
// Supports named colors and hex (#RGB, #RRGGBB).
func parseColor(s string) color.RGBA {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return color.RGBA{0, 0, 0, 255}
	}
	if c, ok := namedColors[s]; ok {
		return c
	}
	hex := strings.TrimPrefix(s, "#")
	switch len(hex) {
	case 3:
		r, _ := strconv.ParseUint(string(hex[0])+string(hex[0]), 16, 8)
		g, _ := strconv.ParseUint(string(hex[1])+string(hex[1]), 16, 8)
		b, _ := strconv.ParseUint(string(hex[2])+string(hex[2]), 16, 8)
		return color.RGBA{uint8(r), uint8(g), uint8(b), 255}
	case 6:
		r, _ := strconv.ParseUint(hex[0:2], 16, 8)
		g, _ := strconv.ParseUint(hex[2:4], 16, 8)
		b, _ := strconv.ParseUint(hex[4:6], 16, 8)
		return color.RGBA{uint8(r), uint8(g), uint8(b), 255}
	}
	return color.RGBA{0, 0, 0, 255} // fallback to black
}

// docRootToFaviconText derives default favicon text from the virtual host directory name.
// Takes the first 3 characters before any dot, uppercased.
// Examples: "stg.net" → "STG", "default" → "DEF", "example.com" → "EXA"
func docRootToFaviconText(docRoot string) string {
	name := filepath.Base(docRoot)
	name = strings.ToUpper(name)
	if idx := strings.IndexByte(name, '.'); idx > 0 {
		name = name[:idx]
	}
	if len(name) > 3 {
		name = name[:3]
	}
	if name == "" {
		return "DEF"
	}
	return name
}

// loadFaviconConfig loads _favicon.yaml from docRoot.
// Returns a default config (domain initials, white on black) if no file exists.
func loadFaviconConfig(docRoot string) (faviconConfig, time.Time) {
	yamlPath := filepath.Join(docRoot, "_favicon.yaml")
	info, err := os.Stat(yamlPath)
	if err != nil {
		return faviconConfig{
			Text:       docRootToFaviconText(docRoot),
			Color:      "white",
			Background: "black",
		}, time.Time{}
	}

	data, err := os.ReadFile(yamlPath)
	if err != nil {
		log.Printf("Warning: cannot read %s: %v", yamlPath, err)
		return faviconConfig{
			Text:       docRootToFaviconText(docRoot),
			Color:      "white",
			Background: "black",
		}, time.Time{}
	}

	var cfg faviconConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		log.Printf("Warning: cannot parse %s: %v", yamlPath, err)
		return faviconConfig{
			Text:       docRootToFaviconText(docRoot),
			Color:      "white",
			Background: "black",
		}, time.Time{}
	}

	// Apply defaults for missing fields
	if cfg.Text == "" && cfg.Image == "" {
		cfg.Text = docRootToFaviconText(docRoot)
	}
	if cfg.Color == "" {
		cfg.Color = "white"
	}
	if cfg.Background == "" {
		cfg.Background = "black"
	}
	if cfg.Fit == "" {
		cfg.Fit = "contain"
	}

	return cfg, info.ModTime()
}

// generateFavicon creates a multi-size ICO favicon from the given config.
func generateFavicon(cfg faviconConfig, docRoot string) ([]byte, error) {
	if cfg.Image != "" {
		imgPath := cfg.Image
		if !filepath.IsAbs(imgPath) {
			imgPath = filepath.Join(docRoot, imgPath)
		}
		return generateImageFavicon(imgPath, cfg.Fit)
	}
	return generateTextFavicon(cfg.Text, cfg.Color, cfg.Background)
}

// generateTextFavicon renders bold text as a multi-size ICO file.
func generateTextFavicon(text, fgColor, bgColor string) ([]byte, error) {
	fg := parseColor(fgColor)
	bg := parseColor(bgColor)
	master := renderTextMaster(text, fg, bg)

	images := make([]image.Image, len(faviconSizes))
	for i, s := range faviconSizes {
		images[i] = resizeImage(master, s, s)
	}
	return encodeICO(images)
}

// renderTextMaster creates a 256×256 image with centered bold text.
func renderTextMaster(text string, fg, bg color.RGBA) *image.RGBA {
	const size = 256
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	imagedraw.Draw(img, img.Bounds(), &image.Uniform{bg}, image.Point{}, imagedraw.Src)

	f := getFaviconFont()
	if f == nil {
		return img
	}

	// Auto-size: start with a large font and scale to fit within 80% of the image.
	const marginFrac = 0.10
	maxW := float64(size) * (1 - 2*marginFrac)
	maxH := float64(size) * (1 - 2*marginFrac)

	trialSize := 200.0
	face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: trialSize, DPI: 72})
	if err != nil {
		return img
	}

	adv := font.MeasureString(face, text)
	textW := float64(adv.Ceil())
	metrics := face.Metrics()
	textH := float64((metrics.Ascent + metrics.Descent).Ceil())

	// Scale to fit
	scaleX := maxW / textW
	scaleY := maxH / textH
	scale := math.Min(scaleX, scaleY)

	finalSize := trialSize * scale
	face, err = opentype.NewFace(f, &opentype.FaceOptions{Size: finalSize, DPI: 72})
	if err != nil {
		return img
	}

	// Re-measure at final size
	adv = font.MeasureString(face, text)
	textW = float64(adv.Ceil())
	metrics = face.Metrics()
	ascent := metrics.Ascent.Ceil()
	descent := metrics.Descent.Ceil()
	textH = float64(ascent + descent)

	// Center the text
	x := (size - int(textW)) / 2
	y := (size-int(textH))/2 + ascent

	d := &font.Drawer{
		Dst:  img,
		Src:  &image.Uniform{fg},
		Face: face,
		Dot:  fixed.P(x, y),
	}
	d.DrawString(text)

	return img
}

// generateImageFavicon loads a source image and creates a multi-size ICO.
func generateImageFavicon(imgPath, fit string) ([]byte, error) {
	f, err := os.Open(imgPath)
	if err != nil {
		return nil, fmt.Errorf("open image %s: %w", imgPath, err)
	}
	defer f.Close()

	src, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("decode image %s: %w", imgPath, err)
	}

	images := make([]image.Image, len(faviconSizes))
	for i, s := range faviconSizes {
		images[i] = fitImage(src, s, s, fit)
	}
	return encodeICO(images)
}

// fitImage resizes a source image to width×height using the specified fit mode.
func fitImage(src image.Image, width, height int, fit string) *image.RGBA {
	switch strings.ToLower(fit) {
	case "crop":
		return cropToFit(src, width, height)
	case "stretch":
		return stretchToFit(src, width, height)
	default: // "contain"
		return containToFit(src, width, height)
	}
}

// containToFit scales the image to fit within width×height, preserving aspect ratio.
// The result is centered with transparent padding where the image doesn't fill.
func containToFit(src image.Image, width, height int) *image.RGBA {
	bounds := src.Bounds()
	srcW := float64(bounds.Dx())
	srcH := float64(bounds.Dy())

	scaleX := float64(width) / srcW
	scaleY := float64(height) / srcH
	scale := math.Min(scaleX, scaleY)

	newW := int(math.Round(srcW * scale))
	newH := int(math.Round(srcH * scale))
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	// Transparent background
	dst := image.NewRGBA(image.Rect(0, 0, width, height))

	resized := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(resized, resized.Bounds(), src, bounds, draw.Over, nil)

	offsetX := (width - newW) / 2
	offsetY := (height - newH) / 2
	imagedraw.Draw(dst, image.Rect(offsetX, offsetY, offsetX+newW, offsetY+newH),
		resized, image.Point{}, imagedraw.Over)

	return dst
}

// cropToFit scales to cover and center-crops the overflow.
func cropToFit(src image.Image, width, height int) *image.RGBA {
	bounds := src.Bounds()
	srcW := float64(bounds.Dx())
	srcH := float64(bounds.Dy())

	scaleX := float64(width) / srcW
	scaleY := float64(height) / srcH
	scale := math.Max(scaleX, scaleY)

	newW := int(math.Round(srcW * scale))
	newH := int(math.Round(srcH * scale))
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	resized := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(resized, resized.Bounds(), src, bounds, draw.Over, nil)

	offsetX := (newW - width) / 2
	offsetY := (newH - height) / 2
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	imagedraw.Draw(dst, dst.Bounds(), resized, image.Point{X: offsetX, Y: offsetY}, imagedraw.Src)

	return dst
}

// stretchToFit resizes ignoring aspect ratio to fill width×height exactly.
func stretchToFit(src image.Image, width, height int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

// resizeImage scales an image to exact dimensions (used for square text masters).
func resizeImage(src image.Image, width, height int) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

// encodeICO writes multiple images into a single ICO file with PNG-encoded entries.
func encodeICO(images []image.Image) ([]byte, error) {
	// Pre-encode each image as PNG
	pngData := make([][]byte, len(images))
	for i, img := range images {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, fmt.Errorf("encode PNG for size %dx%d: %w",
				img.Bounds().Dx(), img.Bounds().Dy(), err)
		}
		pngData[i] = buf.Bytes()
	}

	var ico bytes.Buffer

	// ICO header (6 bytes)
	binary.Write(&ico, binary.LittleEndian, uint16(0))           // reserved
	binary.Write(&ico, binary.LittleEndian, uint16(1))           // type: ICO
	binary.Write(&ico, binary.LittleEndian, uint16(len(images))) // image count

	// Calculate data start offset: header (6) + directory entries (16 each)
	dataOffset := 6 + 16*len(images)

	// Directory entries
	for i, img := range images {
		b := img.Bounds()
		w := b.Dx()
		h := b.Dy()
		bw := uint8(w)
		bh := uint8(h)
		if w >= 256 {
			bw = 0
		}
		if h >= 256 {
			bh = 0
		}

		ico.WriteByte(bw)                                                       // width
		ico.WriteByte(bh)                                                       // height
		ico.WriteByte(0)                                                        // color count (0 = truecolor)
		ico.WriteByte(0)                                                        // reserved
		binary.Write(&ico, binary.LittleEndian, uint16(1))                      // color planes
		binary.Write(&ico, binary.LittleEndian, uint16(32))                     // bits per pixel
		binary.Write(&ico, binary.LittleEndian, uint32(len(pngData[i])))        // image data size
		binary.Write(&ico, binary.LittleEndian, uint32(dataOffset)) // data offset

		dataOffset += len(pngData[i])
	}

	// Image data
	for _, pd := range pngData {
		ico.Write(pd)
	}

	return ico.Bytes(), nil
}

// getCachedFavicon returns favicon bytes for docRoot, regenerating if config changed.
func getCachedFavicon(docRoot string) ([]byte, error) {
	cfg, yamlTime := loadFaviconConfig(docRoot)

	// Check source image mtime for cache invalidation
	var imageTime time.Time
	if cfg.Image != "" {
		imgPath := cfg.Image
		if !filepath.IsAbs(imgPath) {
			imgPath = filepath.Join(docRoot, imgPath)
		}
		if info, err := os.Stat(imgPath); err == nil {
			imageTime = info.ModTime()
		}
	}

	// Serve from cache if timestamps match
	if cached, ok := faviconCache.Load(docRoot); ok {
		entry := cached.(*faviconCacheEntry)
		if entry.yamlTime.Equal(yamlTime) && entry.imageTime.Equal(imageTime) {
			return entry.data, nil
		}
	}

	// Generate fresh favicon
	data, err := generateFavicon(cfg, docRoot)
	if err != nil {
		return nil, err
	}

	faviconCache.Store(docRoot, &faviconCacheEntry{
		data:      data,
		yamlTime:  yamlTime,
		imageTime: imageTime,
	})

	return data, nil
}
