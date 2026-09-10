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
// designed to read on their team's own kit, not on their league's color, so the two are
// often the same hue — the WNBA's figure is orange on orange, the Rams' monogram navy on
// navy. A ground this dark gives every one of them somewhere to sit.
const groundLuminance = 0.045

// bayer8 is an ordered dither matrix. A 900-row gradient over a dark color bands visibly
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

// ground fills the canvas with the league's color as a vertical gradient, interpolated in
// linear light and dithered.
//
// It is drawn well down from the brand color rather than at it. Crests are designed to be
// legible on their own kit, not on their league's color, so a mark and its ground are often
// the same hue: the WNBA's figure is orange, the Rams' monogram is navy, and each vanishes
// into a ground drawn at full strength. Taking it down keeps the league recognizable and
// gives every crest something to sit against.
func ground(dst *image.RGBA, base color.RGBA) {
	b := dst.Bounds()
	// Grounds are pulled to about the same darkness so the set holds together, but not to
	// exactly the same: a color that starts lighter is allowed to land a little lighter,
	// within a narrow band, or two leagues whose colors differ only in lightness come out
	// indistinguishable. Never brightened, so a league already darker than the band keeps
	// its own color.
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

// blend puts a translucent color over what is already there.
func blend(dst *image.RGBA, r image.Rectangle, col color.RGBA, alpha float64) {
	draw.Draw(dst, r.Intersect(dst.Bounds()), image.NewUniform(premul(col, alpha)), image.Point{}, draw.Over)
}

// premul is a color as image/color wants it: alpha already multiplied through. Handing the
// straight color to a Uniform makes white at 96% darker than white, not fainter.
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

// text draws a string centered on x, with its cap-height box centered on capMid.
func text(dst *image.RGBA, t tracked, x, capMid float64, col color.RGBA, alpha float64) {
	if t.text == "" || t.face == nil {
		return
	}
	d := font.Drawer{Dst: dst, Src: image.NewUniform(premul(col, alpha)), Face: t.face}
	// Glyph origins land on whole pixels: subpixel placement would make the same string
	// rasterize differently depending on where it happened to sit.
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

// grown is a mark's silhouette swollen by radius: a real dilation, taking the greatest
// coverage within reach of each pixel, rather than a blur thresholded back to solid. A blur
// spreads unevenly into corners and leaves the edge soft; a keyline should be as crisp as
// the mark's own edge.
//
// The reach is a disc, not a square. A square is quicker and keeps a right angle square, but
// it ends every acute point in a flat cut as wide as the window — a star loses its points
// outright. A disc ends one in an arc, which is what a drawn keyline does.
//
// Even a disc rounds a point it cannot fit inside, so the result is unioned with the
// silhouette scaled about its own center, which is a similarity transform and so keeps every
// angle exactly. Neither alone is right: the dilation is even along an edge and blunt at a
// point, the scaling is sharp at a point and thin along an edge.
func grown(a []float64, w, h, radius int) []float64 {
	out := append([]float64(nil), a...)
	within := disc(radius)
	// Only the edge is worth spreading. A pixel deep inside the mark, and every pixel its
	// disc would reach, is already covered by the copy above, so walking the whole canvas
	// with a window would do the same work a few hundred thousand more times.
	for y := range h {
		for x := range w {
			if !edge(a, w, h, x, y) {
				continue
			}
			v := a[y*w+x]
			for _, d := range within {
				px, py := x+d[0], y+d[1]
				if px < 0 || py < 0 || px >= w || py >= h {
					continue
				}
				if out[py*w+px] < v {
					out[py*w+px] = v
				}
			}
		}
	}
	// Even a disc rounds a point it cannot fit inside, so the result is unioned with the
	// silhouette scaled about its own center, which is a similarity transform and so keeps
	// every angle exactly. Neither alone is right: the dilation is even along an edge and
	// blunt at a point, the scaling is sharp at a point and thin along an edge.
	swollen := swelled(a, w, h, radius)
	for i := range out {
		out[i] = math.Max(out[i], swollen[i])
	}
	return out
}

// edge reports whether a pixel is covered and has something less covered beside it, which is
// the only place a dilation can reach anywhere new.
func edge(a []float64, w, h, x, y int) bool {
	v := a[y*w+x]
	if v == 0 {
		return false
	}
	for _, d := range [4][2]int{{-1, 0}, {1, 0}, {0, -1}, {0, 1}} {
		px, py := x+d[0], y+d[1]
		if px < 0 || py < 0 || px >= w || py >= h || a[py*w+px] < v {
			return true
		}
	}
	return false
}

// disc is every offset within radius of the origin, worked out once per call rather than
// per pixel.
func disc(radius int) [][2]int {
	var out [][2]int
	rr := float64(radius) * float64(radius)
	for dy := -radius; dy <= radius; dy++ {
		for dx := -radius; dx <= radius; dx++ {
			if float64(dx*dx+dy*dy) <= rr {
				out = append(out, [2]int{dx, dy})
			}
		}
	}
	return out
}

// swelled is the silhouette scaled about its center by enough to stand radius proud at the
// furthest point of the mark.
func swelled(a []float64, w, h, radius int) []float64 {
	out := make([]float64, len(a))
	var minX, minY, maxX, maxY = w, h, -1, -1
	for y := range h {
		for x := range w {
			if a[y*w+x] > 0 {
				minX, minY = min(minX, x), min(minY, y)
				maxX, maxY = max(maxX, x), max(maxY, y)
			}
		}
	}
	if maxX < minX {
		return out
	}
	cx, cy := float64(minX+maxX)/2, float64(minY+maxY)/2

	// How far the mark actually reaches, not how far its bounding box does. A star touches
	// its box at the tips and comes nowhere near it at the corners, so the box's diagonal
	// overstates the distance, understates the scale, and leaves the tips short of the
	// dilation — which is to say still flat.
	var reach float64
	for y := range h {
		for x := range w {
			if a[y*w+x] > 0 {
				reach = math.Max(reach, math.Hypot(float64(x)-cx, float64(y)-cy))
			}
		}
	}
	if reach == 0 {
		return out
	}
	scale := 1 + float64(radius)/reach
	for y := range h {
		for x := range w {
			out[y*w+x] = sample(a, w, h, cx+(float64(x)-cx)/scale, cy+(float64(y)-cy)/scale)
		}
	}
	return out
}

// sample reads a coverage buffer between pixels, so a scaled silhouette keeps a smooth edge
// rather than picking up the steps of the grid it came from.
func sample(a []float64, w, h int, x, y float64) float64 {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	at := func(px, py int) float64 {
		if px < 0 || py < 0 || px >= w || py >= h {
			return 0
		}
		return a[py*w+px]
	}
	return at(x0, y0)*(1-fx)*(1-fy) + at(x0+1, y0)*fx*(1-fy) +
		at(x0, y0+1)*(1-fx)*fy + at(x0+1, y0+1)*fx*fy
}

// stamp paints a coverage buffer onto the canvas in one color.
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
