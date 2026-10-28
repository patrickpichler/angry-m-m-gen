// Command angry-mm-generator is a stupid little tool that overlays meme
// text on an angry emoji image.
//
// GET /image?message=<text> returns a PNG of the base image with <text>
// overlaid in classic meme style: Impact, all caps, white with a black
// outline, centered horizontally and pinned near the bottom edge. A
// <br/> (or <br>) in the message starts a new line. An optional
// grain=<0-100> parameter degrades the base image with JPEG re-compression
// artifacts under the text. An optional invert flag flips the base
// image's colors.
package main

import (
	"bytes"
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"log"
	"math"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
)

//go:embed assets/Impact.ttf
var fontData []byte

//go:embed assets/base.jpg
var baseJPEG []byte

const (
	maxWidthFrac     = 0.92  // max text block width, fraction of image width
	bottomMarginFrac = 0.04  // gap between text and bottom edge, fraction of height
	maxSizeFrac      = 0.12  // starting font size, fraction of image height
	minSizeFrac      = 0.055 // smallest font size before wrapping kicks in
	lineGapFrac      = 0.10  // extra spacing between wrapped lines, fraction of font size
	maxLines         = 5     // hard cap on wrapped lines
)

// brRe matches <br>, <br/>, <br /> etc. (any casing) in messages.
var brRe = regexp.MustCompile(`(?i)<br\s*/?>`)

var (
	baseImg  image.Image
	memeFont *opentype.Font
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

// newRootCmd builds the command line interface.
func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "angry-mm-generator",
		Short: "Overlay meme text on the angry emoji via HTTP",
		Long: `A stupid little tool that overlays meme text on an angry emoji.

Serves GET /image?message=<text>, returning a PNG of the base image with
the message overlaid in classic meme style: Impact, all caps, white with
a black outline, centered horizontally near the bottom edge.`,
		Args:         cobra.NoArgs,
		SilenceUsage: true, // don't dump usage on runtime errors
		RunE:         run,
	}
	root.Flags().StringP("addr", "a", ":8080", "listen address")
	root.Flags().StringP("image", "i", "", "path to the base image (defaults to the embedded copy)")
	return root
}

func run(cmd *cobra.Command, args []string) error {
	addr, err := cmd.Flags().GetString("addr")
	if err != nil {
		return err
	}
	imagePath, err := cmd.Flags().GetString("image")
	if err != nil {
		return err
	}

	f, err := opentype.Parse(fontData)
	if err != nil {
		return fmt.Errorf("parse embedded font: %w", err)
	}
	memeFont = f

	switch {
	case imagePath != "":
		baseImg, err = loadImage(imagePath)
		if err != nil {
			return fmt.Errorf("load base image %q: %w", imagePath, err)
		}
	default:
		// No override given: use the base image embedded into the binary
		// (this is how the container image runs — no assets dir needed).
		baseImg, _, err = image.Decode(bytes.NewReader(baseJPEG))
		if err != nil {
			return fmt.Errorf("decode embedded base image: %w", err)
		}
	}
	b := baseImg.Bounds()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /image", imageHandler)
	mux.HandleFunc("/", usageHandler)

	srv := &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	log.Printf("angry-mm-generator listening on %s (base image %dx%d)", addr, b.Dx(), b.Dy())
	return srv.ListenAndServe()
}

func loadImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

func usageHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintln(w, "angry-mm-generator — GET /image?message=<text> returns a PNG meme (<br/> breaks lines, &grain=<0-100> deep-fries, &invert flips colors)")
}

func imageHandler(w http.ResponseWriter, r *http.Request) {
	raw := strings.TrimSpace(r.URL.Query().Get("message"))
	if raw == "" {
		http.Error(w, "missing required query parameter: message", http.StatusBadRequest)
		return
	}

	lines := splitMessage(strings.ToUpper(raw))
	if len(lines) == 0 {
		http.Error(w, "message has no renderable text", http.StatusBadRequest)
		return
	}

	grain := 0.0
	if s := r.URL.Query().Get("grain"); s != "" {
		var err error
		grain, err = strconv.ParseFloat(s, 64)
		if err != nil || grain < 0 || grain > 100 {
			http.Error(w, "grain must be a number between 0 and 100", http.StatusBadRequest)
			return
		}
	}

	invert := false
	if r.URL.Query().Has("invert") {
		switch strings.ToLower(r.URL.Query().Get("invert")) {
		case "", "1", "true", "yes", "on":
			invert = true
		case "0", "false", "no", "off":
			invert = false
		default:
			http.Error(w, "invert must be a boolean (true/false, 1/0, on/off, yes/no)", http.StatusBadRequest)
			return
		}
	}

	out := render(lines, grain, invert)
	w.Header().Set("Content-Type", "image/png")
	if err := png.Encode(w, out); err != nil {
		log.Printf("png encode: %v", err)
	}
}

// render returns a fresh copy of the base image with lines drawn on it.
// The base image is filtered first: invert flips its colors, grain > 0
// adds JPEG artifacts — the caption is drawn on top and stays crisp.
func render(lines []string, grain float64, invert bool) *image.RGBA {
	src := baseImg.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, src.Dx(), src.Dy()))
	draw.Draw(dst, dst.Bounds(), baseImg, src.Min, draw.Src)

	if invert {
		invertColors(dst)
	}
	jpegArtifacts(dst, grain)

	size, lines := layout(lines, dst.Bounds())
	drawText(dst, lines, size, dst.Bounds())
	return dst
}

// jpegArtifacts degrades m in place by re-compressing it as JPEG at a
// quality derived from grain (0-100): the higher the grain, the lower the
// quality, and the heavier the blocking, ringing and color mush. grain <= 0
// is a no-op. The caption drawn afterwards stays crisp.
func jpegArtifacts(m *image.RGBA, grain float64) {
	if grain <= 0 {
		return
	}
	if grain > 100 {
		grain = 100
	}
	// Map grain to JPEG quality on a curve that front-loads the damage:
	// ~96 (invisible) down to 1 (pure mush), so mid-range values are
	// already clearly crunchy.
	quality := int(math.Round(95*math.Pow(1-grain/100, 2.5))) + 1

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, m, &jpeg.Options{Quality: quality}); err != nil {
		log.Printf("jpeg encode: %v", err)
		return
	}
	degraded, err := jpeg.Decode(&buf)
	if err != nil {
		log.Printf("jpeg decode: %v", err)
		return
	}
	b := m.Bounds()
	draw.Draw(m, b, degraded, b.Min, draw.Src)
}

// invertColors inverts the RGB channels of m in place, preserving alpha.
// (The base image is fully opaque, so premultiplied vs. straight RGB
// doesn't matter here.)
func invertColors(m *image.RGBA) {
	for i := 0; i < len(m.Pix); i += 4 {
		m.Pix[i+0] = 255 - m.Pix[i+0]
		m.Pix[i+1] = 255 - m.Pix[i+1]
		m.Pix[i+2] = 255 - m.Pix[i+2]
	}
}

func faceFor(size float64) font.Face {
	face, err := opentype.NewFace(memeFont, &opentype.FaceOptions{
		Size: size,
		DPI:  72,
	})
	if err != nil {
		// Only possible with a broken embedded font; treat as fatal.
		log.Fatalf("create font face: %v", err)
	}
	return face
}

func measure(s string, size float64) int {
	face := faceFor(size)
	defer face.Close()
	d := &font.Drawer{Face: face}
	return d.MeasureString(s).Ceil()
}

// splitMessage turns an (uppercased) message into logical lines: <br>
// variants become hard line breaks, runs of whitespace within a line are
// collapsed, and empty lines are dropped.
func splitMessage(msg string) []string {
	var lines []string
	for _, part := range strings.Split(brRe.ReplaceAllString(msg, "\n"), "\n") {
		if line := strings.Join(strings.Fields(part), " "); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// layout picks a font size for the given logical lines so that they fit
// the image: the whole block shrinks until its widest line fits; if even
// the minimum size overflows, long lines wrap further at that size.
func layout(lines []string, bounds image.Rectangle) (float64, []string) {
	maxW := int(float64(bounds.Dx()) * maxWidthFrac)

	for size := maxSizeFrac * float64(bounds.Dy()); size >= minSizeFrac*float64(bounds.Dy()); size-- {
		if fits(lines, size, maxW) {
			return size, lines
		}
	}

	size := max(minSizeFrac*float64(bounds.Dy()), 12)
	var wrapped []string
	for _, line := range lines {
		wrapped = append(wrapped, wrap(line, size, maxW)...)
	}
	if len(wrapped) > maxLines {
		wrapped = wrapped[:maxLines]
	}
	return size, wrapped
}

// fits reports whether every line is at most maxW wide at the given size.
func fits(lines []string, size float64, maxW int) bool {
	d := &font.Drawer{Face: faceFor(size)}
	defer d.Face.Close()
	for _, line := range lines {
		if d.MeasureString(line).Ceil() > maxW {
			return false
		}
	}
	return true
}

// wrap splits msg into lines no wider than maxW at the given font size.
// A single word wider than maxW ends up on its own (overflowing) line.
func wrap(msg string, size float64, maxW int) []string {
	var lines []string
	cur := ""
	for _, word := range strings.Fields(msg) {
		cand := word
		if cur != "" {
			cand = cur + " " + word
		}
		if cur == "" || measure(cand, size) <= maxW {
			cur = cand
			continue
		}
		lines = append(lines, cur)
		cur = word
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// drawText draws lines centered horizontally, stacked bottom-up, in meme
// style: black outline (8-direction offset trick) with a white fill on top.
func drawText(dst *image.RGBA, lines []string, size float64, bounds image.Rectangle) {
	face := faceFor(size)
	defer face.Close()

	black := image.NewUniform(color.Black)
	white := image.NewUniform(color.White)
	d := &font.Drawer{Dst: dst, Face: face}

	metrics := face.Metrics()
	ascent := metrics.Ascent.Ceil()
	descent := metrics.Descent.Ceil()
	if descent < 0 {
		descent = -descent // sign convention differs between font packages
	}
	lineH := ascent + descent + int(size*lineGapFrac)
	margin := int(float64(bounds.Dy()) * bottomMarginFrac)
	r := max(int(size/16), 2)

	// Outline offsets: two concentric octagon rings.
	var offsets [][2]int
	for _, radius := range [2]int{r, (r + 1) / 2} {
		offsets = append(offsets,
			[2]int{radius, 0}, [2]int{-radius, 0},
			[2]int{0, radius}, [2]int{0, -radius},
			[2]int{radius, radius}, [2]int{radius, -radius},
			[2]int{-radius, radius}, [2]int{-radius, -radius},
		)
	}

	w := bounds.Dx()
	for i, line := range lines {
		lineW := d.MeasureString(line)
		x := fixed.I(w/2) - lineW/2
		// i counts from the top; the bottom line sits margin px above the edge.
		y := fixed.I(bounds.Max.Y - margin - descent - (len(lines)-1-i)*lineH)

		for _, off := range offsets {
			d.Src = black
			d.Dot = fixed.Point26_6{X: x + fixed.I(off[0]), Y: y + fixed.I(off[1])}
			d.DrawString(line)
		}
		d.Src = white
		d.Dot = fixed.Point26_6{X: x, Y: y}
		d.DrawString(line)
	}
}
