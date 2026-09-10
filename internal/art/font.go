package art

import (
	_ "embed"
	"strings"
	"sync"

	"golang.org/x/image/font"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"
	"golang.org/x/image/math/fixed"
)

//go:embed data/BarlowCondensed-Bold.ttf
var fontTTF []byte

// The parsed face is shared; a font.Face is not, because it caches glyphs as it draws. One
// is built per size per call, which is cheap, rather than serializing every piece of text
// in the app behind one lock.
var parsedFont = sync.OnceValues(func() (*sfnt.Font, error) { return opentype.Parse(fontTTF) })

// face builds a face at a size in pixels. DPI 72 makes one point one pixel, so every
// measurement in the layout is in the same units as the canvas.
func face(size float64) (font.Face, error) {
	f, err := parsedFont()
	if err != nil {
		return nil, err
	}
	// Hinting is off deliberately: it runs the TrueType interpreter, which would tie what
	// we draw to the rasterizer's version rather than to the outlines.
	return opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingNone})
}

// tracked is a string drawn with letterspacing, which uppercase at these sizes needs.
type tracked struct {
	text     string
	face     font.Face
	tracking float64 // extra advance per glyph, in pixels
}

// width is the advance the string occupies, without the trailing gap after the last glyph.
func (t tracked) width() float64 {
	if t.text == "" {
		return 0
	}
	w := float64(font.MeasureString(t.face, t.text)) / 64
	return w + t.tracking*float64(len([]rune(t.text))-1)
}

// capHeight is the height of an uppercase letter. Text is centered on this rather than on
// the font's ascent and descent, which include room for accents and tails that uppercase
// never uses — centering on those sits the text visibly high.
func (t tracked) capHeight() float64 { return float64(t.face.Metrics().CapHeight) / 64 }

// fit picks the largest size at which the text fits the width, stepping down a ladder. The
// same plate has to hold "BUF" and "SOUTHEASTERN LOUISIANA", so one size cannot serve.
func fit(text string, width, from, to, trackingRatio float64) (tracked, error) {
	var last tracked
	for size := from; size >= to; size -= 2 {
		f, err := face(size)
		if err != nil {
			return tracked{}, err
		}
		t := tracked{text: text, face: f, tracking: size * trackingRatio}
		if t.width() <= width {
			return t, nil
		}
		last = t
	}
	return last, nil // nothing fit; the floor of the ladder is the best on offer
}

func upper(s string) string { return strings.ToUpper(s) }

func fixp(v float64) fixed.Int26_6 { return fixed.Int26_6(v * 64) }
