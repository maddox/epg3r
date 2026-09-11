package art

import (
	"image"
	"image/color"
	"math"
	"strings"
)

// The canvas is 4:3, which is the shape Channels DVR shows guide art in. 1200x900 is chosen
// against the source: crests arrive at 500x500, so a mark is always being scaled down,
// where the resampling filter is invisible. Going larger would start enlarging them, which
// it is not.
const (
	canvasW, canvasH = 1200, 900

	// A matchup's two cells are identical, and every mark is fitted to the same square, so
	// two crests either side of the seam read as the same size whatever shape they are.
	markCell = 410

	// logoSize is the square a drawn channel logo occupies. It matches the size crests
	// arrive at, so a drawn one and a real one sit the same in a guide's channel rail.
	logoSize = 500

	// keylineWidth is how far the white edge round a crest reaches. It is a keyline, not a
	// border: enough to separate a mark from its ground and no more.
	keylineWidth = 6

	// The whole sticker sits on a soft shadow dropped a little below it.
	shadowBlur = canvasW * 2 / 100
	shadowDrop = canvasH * 12 / 1000
)

// leagueArt is everything the compositor needs to know about a league.
type leagueArt struct {
	Name  string
	Sport string
	Color color.RGBA
	Mark  image.Image // the league's own mark, when it has one
}

// subject is one side of a placard: the crest when there is one, and what to draw in its place
// when there is not.
type subject struct {
	Mark    image.Image // nil when this team has no crest to fetch, or fetching it failed
	Letters string      // drawn in a roundel instead of the crest
}

func canvas() *image.RGBA { return image.NewRGBA(image.Rect(0, 0, canvasW, canvasH)) }

var (
	white = color.RGBA{255, 255, 255, 255}
	black = color.RGBA{0, 0, 0, 255}
)

// composeLeague draws a league's placard. It stands alone on a guide row whenever an
// airing's teams did not both resolve, and it is the channel logo for every slot channel,
// so it has to look deliberate rather than like a fallback.
func composeLeague(lg leagueArt, mark subject) (*image.RGBA, error) {
	dst := canvas()
	ground(dst, lg.Color)

	if mark.Mark != nil {
		// The mark alone, on the middle of the card. A league's own logo already says which
		// league it is; setting the name under it says it twice.
		return dst, paint(dst, lg, prepare(mark, canvasW/2, canvasH/2, 620))
	}

	// No mark to draw, so the league's name is the picture.
	name, err := fit(upper(lg.Name), 940, 260, 96, 0.02)
	if err != nil {
		return nil, err
	}
	text(dst, name, canvasW/2, 420, white, 0.96)
	blend(dst, image.Rect(canvasW/2-130, 527, canvasW/2+130, 533), white, 0.22)

	if lg.Sport != "" {
		sport, err := fit(upper(lg.Sport), 940, 40, 28, 0.12)
		if err != nil {
			return nil, err
		}
		text(dst, sport, canvasW/2, 600, white, 0.55)
	}
	return dst, nil
}

// composeLogo draws a channel's logo when there is no crest to serve: a roundel on
// transparency, which is self-contained enough to sit on any background a client puts
// behind it. A crest, when there is one, is served as it came and never reaches here.
func composeLogo(lg leagueArt, letters string) (*image.RGBA, error) {
	dst := image.NewRGBA(image.Rect(0, 0, logoSize, logoSize))
	return dst, roundel(dst, lg, logoSize/2, logoSize/2, logoSize, letters)
}

// composeMatchup draws one game: both crests either side of a seam. sep is the word between
// them, "AT" when the airing knows which side is home and "VS" when it does not.
func composeMatchup(lg leagueArt, away, home subject) (*image.RGBA, error) {
	dst := canvas()
	ground(dst, lg.Color)
	if err := strap(dst, lg, 176); err != nil {
		return nil, err
	}

	// The cells sit 12px in from where halving the canvas would put them, which gives the
	// seam air; marks centered by arithmetic read as crowded against it.
	left, right := prepare(away, 288, 580, markCell), prepare(home, 912, 580, markCell)
	for _, p := range []placed{left, right} {
		if err := paint(dst, lg, p); err != nil {
			return nil, err
		}
	}

	// A seam, and nothing else. Good guide data words a matchup itself, and better than a
	// placard can: "at" when it knows who is home and "vs" when it does not. A word drawn
	// between the crests can only repeat that or contradict it.
	blend(dst, image.Rect(canvasW/2-1, 440, canvasW/2+2, 720), white, 0.14)

	return dst, nil
}

// The strap says which league a placard belongs to. Its own mark when it has one, since that
// is what anyone recognizes at a glance, and its name set small when it does not.
const (
	strapHeight  = 220
	strapWidth   = 520
	strapKeyline = 4
)

func strap(dst *image.RGBA, lg leagueArt, capMid float64) error {
	if lg.Mark != nil {
		src := trimmed(lg.Mark)
		b := src.Bounds()
		scale := math.Min(strapHeight/float64(b.Dy()), strapWidth/float64(b.Dx()))
		w := max(int(math.Round(float64(b.Dx())*scale)), 1)
		h := max(int(math.Round(float64(b.Dy())*scale)), 1)
		sticker(dst, scaled(src, w, h),
			image.Pt(int(math.Round(canvasW/2-float64(w)/2)), int(math.Round(capMid-float64(h)/2))),
			strapKeyline)
		return nil
	}
	t, err := fit(upper(lg.Name), 900, 34, 24, 0.08)
	if err != nil {
		return err
	}
	text(dst, t, canvasW/2, capMid, white, 0.60)
	return nil
}

// placed is a subject sized and positioned, before anything is drawn.
type placed struct {
	img         *image.RGBA // nil for a lettermark
	at          image.Point
	sub         subject
	cx, cy, box float64
}

func prepare(s subject, cx, cy, box float64) placed {
	p := placed{sub: s, cx: cx, cy: cy, box: box}
	if s.Mark == nil {
		return p
	}
	src := trimmed(s.Mark)
	b := src.Bounds()
	if b.Dx() == 0 || b.Dy() == 0 {
		p.sub.Mark = nil
		return p
	}
	scale := math.Min(box/float64(b.Dx()), box/float64(b.Dy()))
	w := max(int(math.Round(float64(b.Dx())*scale)), 1)
	h := max(int(math.Round(float64(b.Dy())*scale)), 1)
	p.img = scaled(src, w, h)
	p.at = image.Pt(int(math.Round(cx-float64(w)/2)), int(math.Round(cy-float64(h)/2)))
	return p
}

func paint(dst *image.RGBA, lg leagueArt, p placed) error {
	if p.img == nil {
		return roundel(dst, lg, p.cx, p.cy, p.box, p.sub.Letters)
	}
	// Every crest sits in a white keyline traced round its own silhouette, the way a
	// broadcast placard does. It is what makes a mark read on any ground at all, rather
	// than only on the ones it happens not to share a color with — a crest is drawn to be
	// legible on its team's kit, not on its league's color.
	sticker(dst, p.img, p.at, keylineWidth)
	return nil
}

// sticker draws a mark in a white keyline traced round its own silhouette, on a soft shadow.
func sticker(dst *image.RGBA, mark *image.RGBA, at image.Point, keyline int) {
	pad := keyline*3 + shadowBlur*3
	a, w, h := alphaOf(mark, pad)
	edge := grown(a, w, h, keyline)
	corner := at.Sub(image.Pt(pad, pad))

	stamp(dst, boxBlur(append([]float64(nil), edge...), w, h, shadowBlur), w, h, corner, shadowDrop, black, 0.34)
	stamp(dst, edge, w, h, corner, 0, white, 1)
	drawInto(dst, mark, at)
}

func drawInto(dst *image.RGBA, src *image.RGBA, at image.Point) {
	b := src.Bounds()
	for y := range b.Dy() {
		for x := range b.Dx() {
			px := src.Pix[src.PixOffset(b.Min.X+x, b.Min.Y+y):]
			a := float64(px[3]) / 255
			if a == 0 {
				continue
			}
			dx, dy := at.X+x, at.Y+y
			if !image.Pt(dx, dy).In(dst.Bounds()) {
				continue
			}
			// The scaled mark is premultiplied; over wants a straight color.
			over(dst, dx, dy, color.RGBA{
				clamp8(float64(px[0]) / a), clamp8(float64(px[1]) / a), clamp8(float64(px[2]) / a), 255,
			}, a)
		}
	}
}

// roundel draws a disc with letters in it. A circle reads as a crest and carries two
// letters and five equally well, which a rectangle does not.
func roundel(dst *image.RGBA, lg leagueArt, cx, cy, box float64, marks string) error {
	p := placed{cx: cx, cy: cy, box: box, sub: subject{Letters: marks}}
	d := p.box * 0.86
	tile := shade(lg.Color, 0.34)
	fillShape(dst, image.Rect(int(p.cx-d), int(p.cy-d), int(p.cx+d+1), int(p.cy+d+1)),
		tile, 1, circleSDF(p.cx, p.cy, d/2))
	fillShape(dst, image.Rect(int(p.cx-d), int(p.cy-d), int(p.cx+d+1), int(p.cy+d+1)),
		white, 0.22, ringSDF(p.cx, p.cy, d/2, 6))

	// Sized to the letters rather than set at a constant: "BUF" and "ALSGA" at one size
	// look like two different designs.
	var t tracked
	for size := d * 0.55; size >= d*0.16; size -= 2 {
		f, err := face(size)
		if err != nil {
			return err
		}
		t = tracked{text: p.sub.Letters, face: f, tracking: size * 0.04}
		if t.width() <= d*0.66 && t.capHeight() <= d*0.42 {
			break
		}
	}
	ink, alpha := white, 0.95
	if luminance(tile) >= 0.45 {
		ink, alpha = shade(lg.Color, -0.55), 1
	}
	text(dst, t, p.cx, p.cy, ink, alpha)
	return nil
}

// letters is what a roundel says: the abbreviation when the roster has one, else the
// initials of the words that carry the name, else the first of it.
func letters(abbr, name string) string {
	if a := strings.TrimSpace(abbr); a != "" {
		return upper(a)
	}
	var initials []rune
	for _, w := range strings.Fields(name) {
		switch strings.ToLower(strings.Trim(w, ".")) {
		case "fc", "sc", "cf", "af", "united", "city", "of", "the", "at", "and", "state":
			continue
		}
		for _, r := range w {
			initials = append(initials, r)
			break
		}
		if len(initials) == 4 {
			break
		}
	}
	if len(initials) >= 2 {
		return upper(string(initials))
	}
	// Almost every word was noise ("FC Dallas"), so fall back to the name's own letters
	// rather than a slice of the raw string, which would carry the spaces with it.
	var plain []rune
	for _, r := range name {
		if r != ' ' && r != '.' && r != '-' {
			plain = append(plain, r)
		}
		if len(plain) == 3 {
			break
		}
	}
	return upper(string(plain))
}
