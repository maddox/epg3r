// Package titleparse turns an IPTV channel title into structured facts: what kind of
// channel it is, which slot it occupies, which teams are playing, and when.
//
// Titles arrive in dozens of provider-specific shapes. Rather than one regex per
// shape, the parser normalizes the text, peels off the slot label, splits the
// remainder into segments, and classifies each segment (matchup, date, time,
// network, feed note) independently. New provider quirks usually need a new
// normalizer rule or segment classifier, not a new end-to-end pattern.
package titleparse

import (
	"regexp"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

var (
	reSpaces       = regexp.MustCompile(`\s+`)
	reQualityParen = regexp.MustCompile(`(?i)\s*[\[(]\s*(?:HD|FHD|UHD|SD|4K|HEVC|H\.?265|50FPS|60FPS|S|P|A|D|F|Preview)\s*[\])]`)
	reQualityBare  = regexp.MustCompile(`(?i)\b(?:FHD|UHD|4K|HEVC|H\.?265|50FPS|60FPS)\b`)
	reVs           = regexp.MustCompile(`(?i)\s+(?:vs\.?|v\.?|versus)\s+`)
	reAt           = regexp.MustCompile(`(?i)\s+(?:@|at)\s+`)
	reAMPM         = regexp.MustCompile(`(?i)(\d)\s*([ap])\.?m\.?\b`)
	reSeparators   = regexp.MustCompile(`\s*[|]\s*`)
)

// Normalize makes a title predictable: Unicode folded, decorations removed, spacing
// and separators canonical. It returns the cleaned title and the tags it stripped
// (HD, S, P, ...), which callers may want as notes.
func Normalize(title string) (string, []string) {
	// Decorations go first: NFKC would fold "ⓧ" into a plain x.
	s := strings.Map(func(r rune) rune {
		switch r {
		case 'ⓧ', '\u2069', '\u200B', '\uFEFF':
			return -1
		}
		return r
	}, title)
	s = norm.NFKC.String(s)
	s = strings.Map(func(r rune) rune {
		switch {
		case r == ' ' || r == '\t':
			return ' '
		case r == '–' || r == '—' || r == '‐':
			return '-'
		case r == '’' || r == '‘':
			return '\''
		case unicode.IsControl(r):
			return -1
		}
		return r
	}, s)

	var tags []string
	s = reQualityParen.ReplaceAllStringFunc(s, func(m string) string {
		tags = append(tags, strings.Trim(strings.TrimSpace(m), "[]()"))
		return ""
	})
	s = reQualityBare.ReplaceAllStringFunc(s, func(m string) string {
		tags = append(tags, m)
		return ""
	})

	s = reVs.ReplaceAllString(s, " vs ")
	s = reAt.ReplaceAllString(s, " @ ")
	s = reAMPM.ReplaceAllString(s, "$1 ${2}M")
	s = reSeparators.ReplaceAllString(s, " | ")
	s = reSpaces.ReplaceAllString(s, " ")
	s = strings.TrimSpace(s)
	return s, tags
}
