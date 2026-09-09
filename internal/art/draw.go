package art

import (
	"image"
	"image/color"
	"image/draw"
	"math"

	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
)

// groundLuminance is how dark a placard's ground is drawn, as relative luminance. Crests are
// designed to read on their team's own kit, not on their league's colour, so the two are
// often the same hue — the WNBA's figure is orange on orange, the Rams' monogram navy on
// navy. A ground this dark gives every one of them somewhere to sit.
const groundLuminance = 0.045

// bayer8 is an ordered dither matrix. A 900-row gradient over a dark colour bands visibly
// without one, and banding is the thing that makes generated art look generated. Ordered
// rather than random so the same inputs always give the same picture, and 8x8 so the noise
// it adds costs almost nothing to compress.
var bayer8 = [8][8]float64{
	{0, 32, 8, 40, 2, 34, 10, 42},
	{48, 16, 56, 24, 50, 18, 58, 26},
	{12, 44, 4, 36, 14, 46, 6, 38},
	{60, 28, 52, 20, 62, 30, 54, 22},
	{3, 35, 11, 43, 1, 33, 9, 41},
	{51, 19, 59, 27, 49, 17, 57, 25},
	{15, 47, 7, 39, 13, 45, 5, 37},
	{63, 31, 55, 23, 61, 29, 53, 21},
}

// ground fills the canvas with the league's colour as a vertical gradient, interpolated in
// linear light and dithered.
//
// It is drawn well down from the brand colour rather than at it. Crests are designed to be
// legible on their own kit, not on their league's colour, so a mark and its ground are often
// the same hue: the WNBA's figure is orange, the Rams' monogram is navy, and each vanishes
// into a ground drawn at full strength. Taking it down keeps the league recognisable and
// gives every crest something to sit against.
func ground(dst *image.RGBA, base color.RGBA) {
	b := dst.Bounds()
	// Grounds are pulled to about the same darkness so the set holds together, but not to
	// exactly the same: a colour that starts lighter is allowed to land a little lighter,
	// within a narrow band, or two leagues whose colours differ only in lightness come out
	// indistinguishable. Never brightened, so a league already darker than the band keeps
	// its own colour.
	l := luminance(base)
	lit := math.Min(l, groundLuminance*(0.8+0.5*math.Min(l, 0.4)/0.4))
	top, bottom := atLuminance(base, lit), atLuminance(base, lit*0.38)
	tr, tg, tb := toLinear(top.R), toLinear(top.G), toLinear(top.B)
	br, bg, bb := toLinear(bottom.R), toLinear(bottom.G), toLinear(bottom.B)
	for y := b.Min.Y; y < b.Max.Y; y++ {
		f := float64(y-b.Min.Y) / float64(b.Dy()-1)
		r := fromLinear(tr + (br-tr)*f)
		g := fromLinear(tg + (bg-tg)*f)
		bl := fromLinear(tb + (bb-tb)*f)
		row := dst.Pix[dst.PixOffset(b.Min.X, y):]
		for x := b.Min.X; x < b.Max.X; x++ {
			t := bayer8[y&7][x&7] / 64
			p := row[(x-b.Min.X)*4:]
			p[0], p[1], p[2], p[3] = dither(r, t), dither(g, t), dither(bl, t), 255
		}
	}
}

// dither rounds a channel up or down according to where the pixel sits in the matrix, so a
// value between two representable ones is spread across neighbouring pixels instead of
// snapping a whole band of them the same way.
func dither(v, threshold float64) uint8 {
	fl := math.Floor(v)
	if v-fl > threshold {
		fl++
	}
	return clamp8(fl)
}

// blend puts a translucent colour over what is already there.
func blend(dst *image.RGBA, r image.Rectangle, col color.RGBA, alpha float64) {
	draw.Draw(dst, r.Intersect(dst.Bounds()), image.NewUniform(premul(col, alpha)), image.Point{}, draw.Over)
}

// premul is a colour as image/color wants it: alpha already multiplied through. Handing the
// straight colour to a Uniform makes white at 96% darker than white, not fainter.
func premul(col color.RGBA, alpha float64) color.RGBA {
	return color.RGBA{
		clamp8(float64(col.R) * alpha), clamp8(float64(col.G) * alpha),
		clamp8(float64(col.B) * alpha), clamp8(255 * alpha),
	}
}

// coverage is the antialiased fraction of a pixel inside a shape whose edge is d away, d
// being negative inside. One pixel of feathering either side of the edge.
func coverage(d float64) float64 {
	switch {
	case d <= -0.5:
		return 1
	case d >= 0.5:
		return 0
	}
	return 0.5 - d
}

// fillShape paints col wherever sdf says the pixel is inside, antialiased at the edge. One
// routine covers circles, rings and rounded rectangles, which is every shape drawn here.
func fillShape(dst *image.RGBA, bounds image.Rectangle, col color.RGBA, alpha float64, sdf func(x, y float64) float64) {
	b := bounds.Intersect(dst.Bounds())
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			c := coverage(sdf(float64(x)+0.5, float64(y)+0.5)) * alpha
			if c <= 0 {
				continue
			}
			over(dst, x, y, col, c)
		}
	}
}

// over composites one premultiplied pixel.
func over(dst *image.RGBA, x, y int, col color.RGBA, alpha float64) {
	p := dst.Pix[dst.PixOffset(x, y):]
	ia := 1 - alpha
	p[0] = clamp8(float64(col.R)*alpha + float64(p[0])*ia)
	p[1] = clamp8(float64(col.G)*alpha + float64(p[1])*ia)
	p[2] = clamp8(float64(col.B)*alpha + float64(p[2])*ia)
	p[3] = clamp8(255*alpha + float64(p[3])*ia)
}

func circleSDF(cx, cy, radius float64) func(x, y float64) float64 {
	return func(x, y float64) float64 { return math.Hypot(x-cx, y-cy) - radius }
}

func ringSDF(cx, cy, radius, width float64) func(x, y float64) float64 {
	return func(x, y float64) float64 {
		return math.Abs(math.Hypot(x-cx, y-cy)-radius+width/2) - width/2
	}
}

// text draws a string centred on x, with its cap-height box centred on capMid.
func text(dst *image.RGBA, t tracked, x, capMid float64, col color.RGBA, alpha float64) {
	if t.text == "" || t.face == nil {
		return
	}
	d := font.Drawer{Dst: dst, Src: image.NewUniform(premul(col, alpha)), Face: t.face}
	// Glyph origins land on whole pixels: subpixel placement would make the same string
	// rasterise differently depending on where it happened to sit.
	penX := math.Round(x - t.width()/2)
	baseline := math.Round(capMid + t.capHeight()/2)
	for _, r := range t.text {
		d.Dot = fixed.Point26_6{X: fixp(penX), Y: fixp(baseline)}
		d.DrawString(string(r))
		adv, _ := t.face.GlyphAdvance(r)
		penX += float64(adv)/64 + t.tracking
	}
}

// scaled resamples an image to fit a box, keeping its proportions.
func scaled(src image.Image, w, h int) *image.RGBA {
	out := image.NewRGBA(image.Rect(0, 0, w, h))
	xdraw.CatmullRom.Scale(out, out.Bounds(), src, src.Bounds(), xdraw.Over, nil)
	return out
}

// trimmed is the source cropped to the part that is actually drawn on. The marks arrive
// with inconsistent transparent padding, and without this one team's crest comes out a
// fifth smaller than its opponent's for no reason anyone can see.
func trimmed(src image.Image) image.Image {
	b := src.Bounds()
	minX, minY, maxX, maxY := b.Max.X, b.Max.Y, b.Min.X, b.Min.Y
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			if _, _, _, a := src.At(x, y).RGBA(); a > 0x2000 {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x+1), max(maxY, y+1)
			}
		}
	}
	if minX >= maxX || minY >= maxY {
		return src
	}
	type subImager interface {
		SubImage(image.Rectangle) image.Image
	}
	if s, ok := src.(subImager); ok {
		return s.SubImage(image.Rect(minX, minY, maxX, maxY))
	}
	return src
}

// alphaOf copies a mark's coverage into a buffer padded on every side, so anything grown or
// blurred from it has somewhere to go. Working within the mark's own bounds would smear its
// edge out to them and leave a faint rectangle around every crest.
func alphaOf(mark *image.RGBA, pad int) (a []float64, w, h int) {
	b := mark.Bounds()
	w, h = b.Dx()+2*pad, b.Dy()+2*pad
	a = make([]float64, w*h)
	for y := range b.Dy() {
		for x := range b.Dx() {
			a[(y+pad)*w+x+pad] = float64(mark.Pix[mark.PixOffset(b.Min.X+x, b.Min.Y+y)+3]) / 255
		}
	}
	return a, w, h
}

// grown is a mark's silhouette swollen by radius and hardened back to a solid shape with a
// soft edge: blur spreads the coverage outwards, and the threshold turns the faint result
// into something with a definite edge rather than a fog.
func grown(a []float64, w, h, radius int) []float64 {
	out := boxBlur(append([]float64(nil), a...), w, h, radius)
	for i, v := range out {
		out[i] = smoothstep(0.06, 0.22, v)
	}
	return out
}

func smoothstep(lo, hi, v float64) float64 {
	t := (v - lo) / (hi - lo)
	switch {
	case t <= 0:
		return 0
	case t >= 1:
		return 1
	}
	return t * t * (3 - 2*t)
}

// stamp paints a coverage buffer onto the canvas in one colour.
func stamp(dst *image.RGBA, a []float64, w, h int, at image.Point, offsetY int, col color.RGBA, alpha float64) {
	for y := range h {
		for x := range w {
			v := a[y*w+x] * alpha
			if v <= 0.004 {
				continue
			}
			px, py := at.X+x, at.Y+y+offsetY
			if image.Pt(px, py).In(dst.Bounds()) {
				over(dst, px, py, col, v)
			}
		}
	}
}

// boxBlur approximates a gaussian in three passes, which is enough for a shadow and is
// linear in the radius rather than quadratic.
func boxBlur(a []float64, w, h, radius int) []float64 {
	if radius < 1 {
		return a
	}
	for range 3 {
		a = blurPass(a, w, h, radius)
		a = transpose(a, w, h)
		w, h = h, w
		a = blurPass(a, w, h, radius)
		a = transpose(a, w, h)
		w, h = h, w
	}
	return a
}

func blurPass(a []float64, w, h, radius int) []float64 {
	out := make([]float64, len(a))
	n := float64(2*radius + 1)
	for y := range h {
		row := a[y*w : (y+1)*w]
		var sum float64
		for x := -radius; x <= radius; x++ {
			sum += row[clampi(x, 0, w-1)]
		}
		for x := range w {
			out[y*w+x] = sum / n
			sum += row[clampi(x+radius+1, 0, w-1)] - row[clampi(x-radius, 0, w-1)]
		}
	}
	return out
}

func transpose(a []float64, w, h int) []float64 {
	out := make([]float64, len(a))
	for y := range h {
		for x := range w {
			out[x*h+y] = a[y*w+x]
		}
	}
	return out
}

func clampi(v, lo, hi int) int { return min(max(v, lo), hi) }
