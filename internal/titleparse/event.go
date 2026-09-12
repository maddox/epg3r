package titleparse

import (
	"regexp"
	"strings"
)

// eventText is what a slot title says once the label is removed and the pieces are
// classified. Team names are as written; matching happens in the pipeline.
type eventText struct {
	TeamA, TeamB string
	Sep          string // "vs" or "@"; "@" means TeamA is away
	Title        string // non-matchup title, e.g. "MLS 360"
	Schedule     Schedule
	ScheduleRaw  string // the text the schedule was read from, for provider fingerprinting
	Network      string
	Feed         string   // "Home", "Away", "Home Stream", "HOME RSN", "ESPN In Arena", ...
	Womens       bool     // "(W)" marker
	Notes        []string // neutral site, ranks stripped, unknown segments
}

// sepChars are the separator characters providers leave dangling around segments.
const sepChars = " :|-@"

const fragGender = `(W|M|WOMEN|MEN)`

// isWomens reads a gender marker.
func isWomens(marker string) bool { return strings.HasPrefix(strings.ToUpper(marker), "W") }

var (
	rePlaceholder = regexp.MustCompile(`(?i)^(?:` +
		`offline|off ?air|zzz offline|` +
		`no (?:scheduled )?(?:events?|games?|match(?:es)?|streams?)(?: scheduled| today)?|` +
		`not scheduled|no schedule|tba|tbd|to be announced|coming soon|stand ?by|placeholder|` +
		`(?:channel|event|stream) will be updated.*|will be updated.*` +
		`)\.?$`)
	reSentinelYear = regexp.MustCompile(`\b20[6-9]\d\b`) // "2098 12 31" means nothing scheduled
	reTrailParen   = regexp.MustCompile(`\s*\(([^()]*)\)\s*$`)
	reFeedWord     = regexp.MustCompile(`(?i)^(?:home|away|home stream|away stream|home rsn|away rsn|home [a-z]{2,6}|away [a-z]{2,6}|preview|in arena|.*in arena)$`)
	// The stop is matched so it can be stripped, but it is not captured: see parseEvent.
	reStartStop    = regexp.MustCompile(`(?i)^(.*?)\s*start:\s*(\d{4} \d{2} \d{2} \d{2}:\d{2}(?::\d{2})?)\s+stop:\s*(?:\d{4} \d{2} \d{2} \d{2}:\d{2}(?::\d{2})?)\s*$`)
	reMatchupSep   = regexp.MustCompile(`(?i)\s(vs|@|x)\s`)
	reRank         = regexp.MustCompile(`^\(?#?\d{1,2}\)?\s+`)
	reEventPrefix  = regexp.MustCompile(`(?i)^(?:[A-Z]{2,6}\s+)?(?:summer league|preseason|pre-season|exhibition|spring training)\s+`)
	reGenderWord   = regexp.MustCompile(`(?i)^` + fragGender + `$`)
	reGender       = regexp.MustCompile(`(?i)\s*\(` + fragGender + `\)\s*$`)
	reTeamAbbr     = regexp.MustCompile(`\s*\([A-Z]{2,4}\)\s*$`)
	reNeutralSite  = regexp.MustCompile(`(?i)\s*\((?:in|at) [^)]+\)\s*$`)
	reBareDateTime = regexp.MustCompile(`(?i)\s+@?\s*((?:` + fragMon + `\s+\d{1,2}(?:st|nd|rd|th)?|\d{1,2}[./]\d{1,2})\s+(?:` + fragWeekday + `\s+)?(?:` + fragTime12 + `|` + fragTime24 + `)` + fragTZ + `)$`)
	reBareDate     = regexp.MustCompile(`(?i)\s+@?\s*((?:` + fragMon + `\s+\d{1,2}(?:st|nd|rd|th)?|\d{1,2}[./]\d{1,2}))$`)
	reBareTime     = regexp.MustCompile(`(?i)\s+[-|@]?\s*((?:` + fragTime12 + `|` + fragTime24 + `)` + fragTZ + `)$`)
	reParenDateTZ  = regexp.MustCompile(`(?i)^(.*?)\s*\((?:` + fragWeekday + `\s+)?(\d{1,2}[./]\d{1,2})\)\s*(` + fragTime12 + fragTZ + `)$`)
)

// isPlaceholder reports whether the text after the label means "nothing on".
func isPlaceholder(rest string) bool {
	r := strings.Trim(rest, sepChars)
	if r == "" || rePlaceholder.MatchString(r) {
		return true
	}
	// "(2098 12 31 08:00:17)" with no title, or any far-future sentinel date.
	if m := reTrailParen.FindStringSubmatch(r); m != nil && reSentinelYear.MatchString(m[1]) {
		return true
	}
	return false
}

// parseEvent classifies the text after a slot label.
func parseEvent(rest string) eventText {
	var ev eventText
	segs := strings.Split(rest, " | ")
	body := strings.TrimSpace(segs[0])

	// Remaining pipe segments are schedule, feed, or network, in any order.
	for _, seg := range segs[1:] {
		seg = strings.TrimSpace(seg)
		if seg == "" {
			continue
		}
		ev.classifySegment(seg)
	}

	// "start:... stop:..." states a window in no stated zone, so neither end is read.
	//
	// The start is a real one, but the numbers alone do not say which clock they are on,
	// and the format carries nothing that would. One provider writing it is five hours
	// ahead of the times other channels give for the same games; another provider using
	// the same format need not be. Reading it as the viewer's own zone is what put these
	// games five hours late, and any other fixed choice would be a guess that happens to
	// suit one playlist.
	//
	// The stop is not a real end in any zone. Every channel in this family stamps the same
	// span to the second — 7h13m20s on every baseball game, 4h10m on every basketball one
	// — so it describes the provider's block rather than the game.
	//
	// What is left is solid: the teams, and the shape, which fingerprints the provider
	// style and is how these channels keep the numbers they have. With no time the title
	// is in the same position as one naming a game but no day, and the game is placed by a
	// channel that states a real one.
	if m := reStartStop.FindStringSubmatch(body); m != nil {
		body = strings.TrimSpace(m[1])
		ev.ScheduleRaw = "start:" + m[2]
	}

	// Peel trailing parenthesized groups off the body: "(09.08 1:00 PM ET) (FOX)",
	// "(Home)", "(ESPN In Arena)", "(2026 09 09 19:25:15)", "(IN LITTLE ROCK, AR)".
	for {
		m := reTrailParen.FindStringSubmatch(body)
		if m == nil {
			break
		}
		inner := strings.TrimSpace(m[1])
		body = strings.TrimSpace(body[:len(body)-len(m[0])])
		switch {
		case inner == "":
		case reGenderWord.MatchString(inner):
			ev.Womens = isWomens(inner)
		case reNeutralSite.MatchString("(" + inner + ")"):
			ev.Notes = append(ev.Notes, inner)
		default:
			ev.classifySegment(inner)
		}
	}

	// "(Sunday 09/13) 1:00PM ET" – date in parens followed by a bare time.
	if m := reParenDateTZ.FindStringSubmatch(body); m != nil {
		if sch, ok := parseSchedule(m[2] + " " + m[3]); ok {
			ev.Schedule = sch
			ev.ScheduleRaw = "(" + m[2] + ") " + m[3]
			body = strings.TrimSpace(m[1])
		}
	}
	// Bare schedules at the end of the body: "Aug 30 4:30 PM", "09/03 07:00PM", a lone
	// "8:05 PM ET", or a date with no time at all ("Mavericks vs Thunder Jul 16").
	if !ev.Schedule.HasTime {
		_ = ev.takeTail(&body, reBareDateTime, func(Schedule) bool { return true }) ||
			ev.takeTail(&body, reBareTime, func(s Schedule) bool { return s.HasTime }) ||
			(!ev.Schedule.HasDate && ev.takeTail(&body, reBareDate, func(s Schedule) bool { return s.HasDate }))
	}
	body = strings.TrimRight(body, sepChars)

	// Gender marker applies to the whole matchup: "NORTHWESTERN @ 17 ILLINOIS (M)".
	if m := reGender.FindStringSubmatch(body); m != nil {
		ev.Womens = isWomens(m[1])
		body = strings.TrimSpace(body[:len(body)-len(m[0])])
	}

	// Matchup or plain title.
	if loc := reMatchupSep.FindStringSubmatchIndex(body); loc != nil {
		ev.TeamA = cleanTeam(body[:loc[0]], &ev)
		ev.TeamB = cleanTeam(body[loc[1]:], &ev)
		sep := strings.ToLower(body[loc[2]:loc[3]])
		if sep == "x" {
			sep = "vs"
		}
		ev.Sep = sep
	} else {
		ev.Title = body
	}
	return ev
}

// takeTail peels a schedule matched by re off the end of body when accept approves it.
func (ev *eventText) takeTail(body *string, re *regexp.Regexp, accept func(Schedule) bool) bool {
	m := re.FindStringSubmatch(*body)
	if m == nil {
		return false
	}
	sch, ok := parseSchedule(m[1])
	if !ok || !accept(sch) {
		return false
	}
	ev.Schedule, ev.ScheduleRaw = sch, m[1]
	*body = strings.TrimSpace((*body)[:len(*body)-len(m[0])])
	return true
}

// classifySegment files one pipe-separated segment (or parenthesized group).
func (ev *eventText) classifySegment(seg string) {
	if sch, ok := parseSchedule(seg); ok {
		if !ev.Schedule.HasTime || sch.HasDate {
			if !sch.HasDate && ev.Schedule.HasDate { // keep a date we already have
				sch.Year, sch.Month, sch.Day, sch.HasDate = ev.Schedule.Year, ev.Schedule.Month, ev.Schedule.Day, true
			}
			ev.Schedule = sch
			ev.ScheduleRaw = "|" + seg
		}
		return
	}
	if reFeedWord.MatchString(seg) {
		ev.Feed = seg
		return
	}
	if reGenderWord.MatchString(seg) {
		ev.Womens = isWomens(seg)
		return
	}
	if ev.Network == "" {
		ev.Network = seg
		return
	}
	ev.Notes = append(ev.Notes, seg)
}

// cleanTeam strips what providers attach to a team name: rankings ("17 ILLINOIS"),
// abbreviations ("Dallas Wings (DAL)"), gender markers, and neutral-site notes.
func cleanTeam(s string, ev *eventText) string {
	s = strings.TrimSpace(s)
	if m := reNeutralSite.FindStringSubmatch(s); m != nil {
		ev.Notes = append(ev.Notes, strings.Trim(strings.TrimSpace(m[0]), "()"))
		s = strings.TrimSpace(s[:len(s)-len(m[0])])
	}
	if m := reGender.FindStringSubmatch(s); m != nil {
		ev.Womens = ev.Womens || isWomens(m[1])
		s = strings.TrimSpace(s[:len(s)-len(m[0])])
	}
	s = reTeamAbbr.ReplaceAllString(s, "")
	if m := reEventPrefix.FindString(s); m != "" {
		ev.Notes = append(ev.Notes, strings.TrimSpace(m))
		s = s[len(m):]
	}
	if reRank.MatchString(s) {
		ev.Notes = append(ev.Notes, "ranked")
		s = reRank.ReplaceAllString(s, "")
	}
	return strings.Trim(s, sepChars)
}
