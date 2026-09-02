package titleparse

import (
	"maps"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Schedule is the date and time found in a title, before resolution to an instant.
type Schedule struct {
	Year, Month, Day int // Year 0 means "infer"
	Hour, Minute     int // 24 hour clock
	HasDate, HasTime bool
	TZ               string // abbreviation as written, "" when absent
	Stop             *Schedule
}

var months = map[string]int{
	"jan": 1, "feb": 2, "mar": 3, "apr": 4, "may": 5, "jun": 6,
	"jul": 7, "aug": 8, "sep": 9, "sept": 9, "oct": 10, "nov": 11, "dec": 12,
}

const (
	fragMon     = `(?:Jan|Feb|Mar|Apr|May|Jun|Jul|Aug|Sep|Sept|Oct|Nov|Dec)[a-z]*\.?`
	fragWeekday = `(?:Mon|Tue|Tues|Wed|Thu|Thur|Thurs|Fri|Sat|Sun)[a-z]*\.?,?`
	fragTime12  = `\d{1,2}(?::\d{2})?\s?[AP]M`
	fragTime24  = `\d{1,2}:\d{2}(?::\d{2})?`
)

var (
	// "09.08", "09/08", "9/8", "09-08", optionally with a year.
	reNumericDate = regexp.MustCompile(`^(\d{1,2})[./-](\d{1,2})(?:[./-](\d{2,4}))?$`)
	// "Sep 8", "Sep 2nd", "September 8, 2026"
	reMonDay = regexp.MustCompile(`(?i)^(` + fragMon + `)\s+(\d{1,2})(?:st|nd|rd|th)?(?:,?\s+(\d{4}))?$`)
	// "8 Sep", "18th Sep", "01 Sep 2026"
	reDayMon = regexp.MustCompile(`(?i)^(\d{1,2})(?:st|nd|rd|th)?\s+(` + fragMon + `)(?:,?\s+(\d{4}))?$`)
	// "2026 09 09", "2026-09-09"
	reISOish = regexp.MustCompile(`^(\d{4})[ -](\d{2})[ -](\d{2})$`)

	reTime12   = regexp.MustCompile(`(?i)^(\d{1,2})(?::(\d{2}))?\s?([AP])M$`)
	reTime24   = regexp.MustCompile(`^(\d{1,2}):(\d{2})(?::\d{2})?$`)
	reTZSuffix = regexp.MustCompile(`(?i)\s*\(?\b(` + strings.Join(slices.Sorted(maps.Keys(tzZones)), "|") + `)\b\)?$`)
	reWeekday  = regexp.MustCompile(`(?i)^` + fragWeekday + `\s+|\s+` + fragWeekday + `$|^\(` + fragWeekday + `\s+|\s+` + fragWeekday + `\)$`)
	// "2026 09 09 19:25:15": a full stamp in one token.
	reFullStamp = regexp.MustCompile(`^(\d{4})[ -](\d{2})[ -](\d{2})\s+(\d{2}):(\d{2})(?::\d{2})?$`)
	// A trailing time token, 12 or 24 hour.
	reTrailTime = regexp.MustCompile(`(?i)\s*(` + fragTime12 + `|` + fragTime24 + `)$`)
)

// parseSchedule tries to read a whole segment as a date, a time, or both, in any of
// the arrangements providers use. Weekday names are ignored. It returns ok=false when
// the segment is not a schedule.
func parseSchedule(seg string) (Schedule, bool) {
	var sch Schedule
	s := strings.TrimSpace(seg)
	s = strings.Trim(s, "()")
	if s == "" {
		return sch, false
	}

	// Trailing timezone.
	if m := reTZSuffix.FindStringSubmatch(s); m != nil {
		sch.TZ = strings.ToUpper(m[1])
		s = strings.TrimSpace(s[:len(s)-len(m[0])])
	}
	// Weekday names anywhere at the edges.
	for {
		t := reWeekday.ReplaceAllString(s, "")
		t = strings.TrimSpace(strings.Trim(t, "()"))
		if t == s {
			break
		}
		s = t
	}

	if m := reFullStamp.FindStringSubmatch(s); m != nil {
		sch.Year, sch.Month, sch.Day = atoi(m[1]), atoi(m[2]), atoi(m[3])
		sch.Hour, sch.Minute = atoi(m[4]), atoi(m[5])
		sch.HasDate, sch.HasTime = true, true
		return sch, validDate(sch)
	}

	// Split into a date part and a time part: the time is the trailing token(s).
	if m := reTrailTime.FindStringSubmatch(s); m != nil {
		if h, mi, ok := parseClock(m[1]); ok {
			sch.Hour, sch.Minute, sch.HasTime = h, mi, true
			s = strings.TrimSpace(s[:len(s)-len(m[0])])
		}
	}
	if s == "" {
		return sch, sch.HasTime
	}
	// Weekday may sit between date and time: "Sep 2nd Tue 7:00PM".
	s = strings.TrimSpace(reWeekday.ReplaceAllString(s, ""))

	if y, mo, d, ok := parseDate(s); ok {
		sch.Year, sch.Month, sch.Day, sch.HasDate = y, mo, d, true
		return sch, validDate(sch)
	}
	return sch, false
}

func parseDate(s string) (year, month, day int, ok bool) {
	if m := reNumericDate.FindStringSubmatch(s); m != nil {
		month, day = atoi(m[1]), atoi(m[2])
		if month > 12 && day <= 12 { // a DMY feed
			month, day = day, month
		}
		if m[3] != "" {
			year = atoi(m[3])
			if year < 100 {
				year += 2000
			}
		}
		return year, month, day, true
	}
	if m := reMonDay.FindStringSubmatch(s); m != nil {
		return atoi(m[3]), monthNum(m[1]), atoi(m[2]), true
	}
	if m := reDayMon.FindStringSubmatch(s); m != nil {
		return atoi(m[3]), monthNum(m[2]), atoi(m[1]), true
	}
	if m := reISOish.FindStringSubmatch(s); m != nil {
		return atoi(m[1]), atoi(m[2]), atoi(m[3]), true
	}
	return 0, 0, 0, false
}

func parseClock(s string) (hour, minute int, ok bool) {
	if m := reTime12.FindStringSubmatch(s); m != nil {
		hour, minute = atoi(m[1]), atoi(m[2])
		pm := strings.EqualFold(m[3], "p")
		if hour == 12 {
			hour = 0
		}
		if pm {
			hour += 12
		}
		return hour, minute, hour < 24 && minute < 60
	}
	if m := reTime24.FindStringSubmatch(s); m != nil {
		hour, minute = atoi(m[1]), atoi(m[2])
		return hour, minute, hour < 24 && minute < 60
	}
	return 0, 0, false
}

func monthNum(s string) int {
	s = strings.ToLower(strings.TrimSuffix(s, "."))
	if len(s) > 4 {
		s = s[:3]
	}
	return months[s]
}

func validDate(s Schedule) bool {
	if !s.HasDate {
		return true
	}
	if s.Month < 1 || s.Month > 12 || s.Day < 1 || s.Day > 31 {
		return false
	}
	return true
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

// Resolve turns a Schedule into an instant. Missing pieces are filled from now and
// loc: no date means today, no time means midnight. The year, when absent, is the
// one that puts the date nearest to now (so a January game listed in December lands
// in the next year).
func (s Schedule) Resolve(now time.Time, loc *time.Location) (kickoff time.Time, dateAssumed bool) {
	if z := zoneFor(s.TZ); z != nil {
		loc = z
	}
	today := now.In(loc)
	y, m, d := today.Date()
	if s.HasDate {
		m, d = time.Month(s.Month), s.Day
		if s.Year != 0 {
			y = s.Year
		} else {
			y = inferYear(int(m), d, today)
		}
	} else {
		dateAssumed = true
	}
	return time.Date(y, m, d, s.Hour, s.Minute, 0, 0, loc), dateAssumed
}

func inferYear(month, day int, today time.Time) int {
	best, bestDiff := today.Year(), time.Duration(math.MaxInt64)
	for _, y := range []int{today.Year() - 1, today.Year(), today.Year() + 1} {
		t := time.Date(y, time.Month(month), day, 12, 0, 0, 0, today.Location())
		if int(t.Month()) != month { // Feb 29 in a non-leap year
			continue
		}
		diff := t.Sub(today).Abs()
		if diff < bestDiff {
			best, bestDiff = y, diff
		}
	}
	return best
}
