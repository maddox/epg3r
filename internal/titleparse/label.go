package titleparse

import (
	"regexp"
	"strings"
	"sync"

	"github.com/jonmaddox/epg3r/internal/catalog"
)

// Slot label shapes seen in the wild, all of which mean "slot 4 of this league":
//
//	NFL 04: ...            NFL 04 | ...          NFL  | 04          NFL 04
//	USA | NFL 04: ...      US (WNBA 04) | ...    (NBA 04) | ...     WNBA (WNBA 04) | ...
//	(Apple) (MLS) 004 | .. USA | NHL Game 04: .. :MLS  04           ... :NBA 04   (trailing)
//
// The label itself comes from the catalog, so the regexes are built per label pattern
// and cached by that pattern; editing a league in the catalog changes the key.
type labelRes struct {
	leading  *regexp.Regexp
	trailing *regexp.Regexp
}

var labelCache sync.Map // label pattern -> labelRes

func labelRegexes(lg *catalog.League) labelRes {
	pattern := lg.LabelPattern()
	if v, ok := labelCache.Load(pattern); ok {
		return v.(labelRes)
	}
	r := labelRes{
		leading: regexp.MustCompile(`(?i)^` +
			`(?:[A-Z]{2,4}\s*[:|]\s*)?` + // country prefix "USA | " / "US: "
			`:?\s*` + // ":MLS  04"
			`(?:[A-Z]{2,6}\s+)?` + // "WNBA (WNBA 04)": a repeated league word before the parenthesised label
			`\(?\s*` + pattern + `\s*\)?` + // the label, optionally in parens
			`\s*[|:]?\s*` + // "NHL | 04"
			`(\d{1,3})` + // slot
			`\s*\)?\s*[:|]?\s*` + // closing paren / separator
			`(.*)$`),
		trailing: regexp.MustCompile(`(?i)^(.*?)\s*:\s*` + pattern + `\s+(\d{1,3})\s*$`),
	}
	labelCache.Store(pattern, r)
	return r
}

// splitLabel finds the league's slot label in a normalized title. It returns the slot
// number, the remaining text, and the label's shape, or ok=false when the title has
// no slot label.
//
// The shape is the label text with digits and spaces removed ("USA|NFL#:", "NFL#|",
// "(NBA#)|"). Every provider writes its labels one way, so the shape identifies the
// provider family a slot channel belongs to when several providers share slot numbers.
func splitLabel(lg *catalog.League, title string) (slot int, rest, labelShape string, ok bool) {
	res := labelRegexes(lg)
	if m := res.leading.FindStringSubmatch(title); m != nil {
		rest = strings.TrimSpace(m[2])
		return atoi(m[1]), rest, shape(title[:len(title)-len(m[2])], false), true
	}
	if m := res.trailing.FindStringSubmatch(title); m != nil {
		rest = strings.TrimSpace(m[1])
		return atoi(m[2]), rest, shape(title[len(m[1]):], false), true
	}
	return 0, "", "", false
}

var (
	reLetters = regexp.MustCompile(`[A-Za-z]+`)
	reDigits  = regexp.MustCompile(`\d+`)
)

// shape reduces text to its punctuation skeleton: digits become "#", spaces vanish,
// and, when letters is set, runs of letters become "A" ("#.##:#AA" for
// "09.01 6:40PM ET"). This is how a provider's format is recognised regardless of the
// particular slot, date, or time.
func shape(s string, letters bool) string {
	if letters {
		s = reLetters.ReplaceAllString(s, "A")
	}
	s = reDigits.ReplaceAllString(s, "#")
	s = strings.ReplaceAll(s, " ", "")
	return strings.ToUpper(strings.TrimSpace(s))
}
