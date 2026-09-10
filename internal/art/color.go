package art

import (
	"image/color"
	"math"
)

// Shading happens in linear light. Scaling sRGB values directly darkens the middle of a
// gradient more than the ends, which is exactly where banding shows.
func toLinear(v uint8) float64 {
	f := float64(v) / 255
	if f <= 0.04045 {
		return f / 12.92
	}
	return math.Pow((f+0.055)/1.055, 2.4)
}

func fromLinear(f float64) float64 {
	switch {
	case f <= 0:
		return 0
	case f >= 1:
		return 255
	case f <= 0.0031308:
		return f * 12.92 * 255
	}
	return (1.055*math.Pow(f, 1/2.4) - 0.055) * 255
}

// shade lightens (f > 0) or darkens (f < 0) a color by moving it that fraction of the way
// toward white or black in linear light.
func shade(c color.RGBA, f float64) color.RGBA {
	ch := func(v uint8) uint8 {
		l := toLinear(v)
		if f >= 0 {
			l += (1 - l) * f
		} else {
			l *= 1 + f
		}
		return uint8(math.Round(fromLinear(l)))
	}
	return color.RGBA{ch(c.R), ch(c.G), ch(c.B), 255}
}

// atLuminance scales a color in linear light until its relative luminance is target,
// keeping its hue. Shifting every brand color down by the same fraction does not work: it
// leaves a bright one bright and drives an already-dark one to black. Naming the darkness we
// want instead puts every league's ground at the same depth, whatever it started from.
func atLuminance(c color.RGBA, target float64) color.RGBA {
	l := luminance(c)
	if l <= 0 {
		return c
	}
	f := target / l
	ch := func(v uint8) uint8 { return clamp8(fromLinear(toLinear(v) * f)) }
	return color.RGBA{ch(c.R), ch(c.G), ch(c.B), 255}
}

// luminance is the relative luminance used to decide contrast, 0 for black and 1 for white.
func luminance(c color.RGBA) float64 {
	return 0.2126*toLinear(c.R) + 0.7152*toLinear(c.G) + 0.0722*toLinear(c.B)
}

func clamp8(f float64) uint8 {
	switch {
	case f <= 0:
		return 0
	case f >= 255:
		return 255
	}
	return uint8(f + 0.5)
}
