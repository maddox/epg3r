package titleparse

import (
	"maps"
	"slices"
	"strings"
	"sync"
	"time"
)

// tzZones maps the abbreviations providers write to IANA zones. "EST" year-round
// means New York, not a fixed offset, because providers never switch to "EDT".
var tzZones = map[string]string{
	"ET": "America/New_York", "EST": "America/New_York", "EDT": "America/New_York",
	"CT": "America/Chicago", "CST": "America/Chicago", "CDT": "America/Chicago",
	"MT": "America/Denver", "MST": "America/Denver", "MDT": "America/Denver",
	"PT": "America/Los_Angeles", "PST": "America/Los_Angeles", "PDT": "America/Los_Angeles",
	"GMT": "UTC", "UTC": "UTC", "BST": "Europe/London", "CET": "Europe/Berlin", "CEST": "Europe/Berlin",
}

// fragTZ matches an optional trailing zone abbreviation; built from tzZones so the
// list lives in one place.
var fragTZ = `(?:\s?\(?(?:` + strings.Join(slices.Sorted(maps.Keys(tzZones)), "|") + `)\)?)?`

var locCache sync.Map // zone name -> *time.Location

// LoadLocation is time.LoadLocation with a cache. The standard library reads and
// decompresses zoneinfo on every call, which dominated run time when done per title.
func LoadLocation(name string) (*time.Location, error) {
	if v, ok := locCache.Load(name); ok {
		return v.(*time.Location), nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, err
	}
	locCache.Store(name, loc)
	return loc, nil
}

// zoneFor resolves an abbreviation from a title, or nil when unknown.
func zoneFor(abbr string) *time.Location {
	name, ok := tzZones[abbr]
	if !ok {
		return nil
	}
	loc, err := LoadLocation(name)
	if err != nil {
		return nil
	}
	return loc
}
