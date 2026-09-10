// Package catalog is the built-in knowledge of leagues and teams: how to recognise a
// league from a playlist group, what an airing should be called, how long a game
// lasts, which channel numbers a league owns, and who the teams are (with their
// Gracenote ids). Defaults are embedded; the store layers user overrides on top.
package catalog

import (
	"cmp"
	"embed"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

//go:embed data/catalog.yaml
var dataFS embed.FS

// League describes one league.
type League struct {
	Key         string        `yaml:"key"`          // stable lowercase id, never changes
	Name        string        `yaml:"name"`         // "NFL"
	AiringTitle string        `yaml:"airing_title"` // XMLTV <title>, e.g. "NFL Football"
	Sport       string        `yaml:"sport"`
	Genre       string        `yaml:"genre"`      // XMLTV <category>, e.g. "Football"
	Categories  []string      `yaml:"categories"` // extra categories; default ["Sports event","Sports"]
	Duration    time.Duration `yaml:"duration"`
	StartPad    time.Duration `yaml:"start_pad"`
	EndPad      time.Duration `yaml:"end_pad"`
	SeriesID    string        `yaml:"series_id"`

	// ChannelSlot is the league's position on the shelf, counted in blocks from wherever
	// the user's channel numbers start. The manifest fixes the order; the user picks the
	// one number the whole shelf hangs off, so every league moves together.
	ChannelSlot int `yaml:"channel_slot"`

	// Each league owns BlockSize channel numbers starting at ChannelBase, which is derived
	// from ChannelSlot and never read from the manifest. Slot channels occupy the first
	// TeamOffset numbers, split into families of SlotSpan (one family per provider style,
	// since several providers may all have an "NFL 04"); team channels take the rest.
	ChannelBase  int      `yaml:"-"`
	SlotSpan     int      `yaml:"slot_span"`     // numbers per slot family; default 100
	LabelPrefix  string   `yaml:"label_prefix"`  // the word before the slot number, "NFL" in "NFL 03"
	LabelAliases []string `yaml:"label_aliases"` // regexes that also count as the label, e.g. "NHL Game"
	NameTokens   []string `yaml:"name_tokens"`   // other league words found in channel names, e.g. "NBALP"

	Timezone      string   `yaml:"timezone"`       // overrides the source zone when set
	GroupPatterns []string `yaml:"group_patterns"` // regexes (case-insensitive) matched against group-title
	NamePatterns  []string `yaml:"name_patterns"`  // fallback regexes matched against the channel name
	Exclude       []string `yaml:"exclude"`        // regexes; a name matching one is not this league

	Roster            string `yaml:"roster"`              // league column in tms_league_teams.csv
	WomensRoster      string `yaml:"womens_roster"`       // roster used when a title carries a (W) marker
	WomensAiringTitle string `yaml:"womens_airing_title"` // airing title for those games
	WomensSeriesID    string `yaml:"womens_series_id"`    // series id for those games; defaults to SeriesID

	// Color is the league's brand hex, "#013369". Art grounds every image it draws for
	// this league in it. Unset falls back to DefaultColor; every shipped league sets one.
	Color string `yaml:"color"`

	// LogoPath is the league's segment in the mark source's layout, "nfl" or "soccer" or
	// "ncaa". Teams carry the id within it. Empty means this league's teams have no marks
	// to fetch and are drawn from their names instead.
	LogoPath string `yaml:"logo_path"`

	// LogoID is the league's own mark: an id on the same source its teams come from, or a
	// whole URL when the league's mark lives somewhere else. Empty means there is none to
	// fetch and the league is drawn from its name instead.
	LogoID string `yaml:"logo_id"`

	// Logo and Placard override the art epg3r generates for this league.
	Logo    string `yaml:"logo"`
	Placard string `yaml:"placard"`

	// Loc is Timezone resolved, or nil when the league defers to the source.
	Loc *time.Location

	groupRes   []*regexp.Regexp
	nameRes    []*regexp.Regexp
	excludeRes []*regexp.Regexp
}

// Channel number layout within a league's block.
const (
	BlockSize       = 1000 // numbers per league
	TeamOffset      = 800  // team channels start here within the block
	DefaultSlotSpan = 100
)

// DefaultChannelStart is where the shelf sits until the user moves it: high enough to clear
// the numbers other providers hand out, and on a round thousand so a block is readable.
const DefaultChannelStart = 10000

// DefaultColor grounds the art of a league that names no brand colour. Every league in the
// shipped manifest names one; this is for a league defined somewhere else.
const DefaultColor = "#334155"

// DefaultCategories are appended to every sports airing so Channels DVR files it as a
// sports event and its guide filters find it.
var DefaultCategories = []string{"Sports event", "Sports"}

// Catalog is the loaded set of leagues and rosters.
type Catalog struct {
	Leagues []League // in manifest order
	byKey   map[string]*League
	rosters map[string]*TeamIndex // by roster name
	tokens  string                // regex alternation of every league word, see TokenPattern
}

// manifest is the shape of data/catalog.yaml.
type manifest struct {
	Leagues []League     `yaml:"leagues"`
	Rosters []rosterFile `yaml:"rosters"`
}

// Load reads the embedded catalog.
func Load() (*Catalog, error) {
	body, err := dataFS.ReadFile("data/catalog.yaml")
	if err != nil {
		return nil, err
	}
	return Parse(body)
}

// Parse loads a catalog manifest.
func Parse(body []byte) (*Catalog, error) {
	var m manifest
	if err := yaml.Unmarshal(body, &m); err != nil {
		return nil, fmt.Errorf("catalog.yaml: %w", err)
	}
	c := &Catalog{Leagues: m.Leagues, byKey: map[string]*League{}}
	rosters, err := buildRosters(m.Rosters)
	if err != nil {
		return nil, err
	}
	c.rosters = rosters

	var tokens []string
	for i := range c.Leagues {
		lg := &c.Leagues[i]
		if err := lg.compile(); err != nil {
			return nil, err
		}
		c.byKey[lg.Key] = lg
		lg.ChannelBase = DefaultChannelStart + lg.ChannelSlot*BlockSize
		tokens = append(tokens, regexp.QuoteMeta(lg.LabelPrefix))
		tokens = append(tokens, lg.LabelAliases...)
		for _, t := range lg.NameTokens {
			tokens = append(tokens, regexp.QuoteMeta(t))
		}
	}
	c.tokens = "(?:" + strings.Join(tokens, "|") + ")"
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// TokenPattern is a regex alternation matching any league's label, label alias, or
// name token, so parsers can strip league words from channel names without knowing
// the leagues.
func (c *Catalog) TokenPattern() string { return c.tokens }

func (lg *League) compile() error {
	var err error
	if lg.groupRes, err = compileAll(lg.GroupPatterns); err != nil {
		return fmt.Errorf("league %s group_patterns: %w", lg.Key, err)
	}
	if lg.nameRes, err = compileAll(lg.NamePatterns); err != nil {
		return fmt.Errorf("league %s name_patterns: %w", lg.Key, err)
	}
	if lg.excludeRes, err = compileAll(lg.Exclude); err != nil {
		return fmt.Errorf("league %s exclude: %w", lg.Key, err)
	}
	if _, err := regexp.Compile(lg.LabelPattern()); err != nil {
		return fmt.Errorf("league %s label_aliases: %w", lg.Key, err)
	}
	if lg.Timezone != "" {
		if lg.Loc, err = time.LoadLocation(lg.Timezone); err != nil {
			return fmt.Errorf("league %s timezone: %w", lg.Key, err)
		}
	}
	if len(lg.Categories) == 0 {
		lg.Categories = append([]string(nil), DefaultCategories...)
	}
	if lg.SlotSpan == 0 {
		lg.SlotSpan = DefaultSlotSpan
	}
	if lg.Color == "" {
		lg.Color = DefaultColor
	}
	return nil
}

func compileAll(pats []string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(pats))
	for _, p := range pats {
		re, err := regexp.Compile("(?i)" + p)
		if err != nil {
			return nil, fmt.Errorf("%q: %w", p, err)
		}
		out = append(out, re)
	}
	return out, nil
}

// RGB reads Color. ok is false when it is unset or malformed, which validate rules out for
// a league from the manifest but not for one built in a test.
func (lg *League) RGB() (r, g, b uint8, ok bool) {
	if len(lg.Color) != 7 || lg.Color[0] != '#' {
		return 0, 0, 0, false
	}
	var v uint32
	for _, ch := range []byte(lg.Color[1:]) {
		switch {
		case ch >= '0' && ch <= '9':
			v = v<<4 | uint32(ch-'0')
		case ch >= 'a' && ch <= 'f':
			v = v<<4 | uint32(ch-'a'+10)
		case ch >= 'A' && ch <= 'F':
			v = v<<4 | uint32(ch-'A'+10)
		default:
			return 0, 0, 0, false
		}
	}
	return uint8(v >> 16), uint8(v >> 8), uint8(v), true
}

func (c *Catalog) validate() error {
	seenSeries := map[string]string{}
	seenSlot := map[int]string{}
	for _, lg := range c.Leagues {
		switch {
		case lg.Key == "" || strings.ToLower(lg.Key) != lg.Key:
			return fmt.Errorf("league key %q must be lowercase and non-empty", lg.Key)
		case lg.LabelPrefix == "":
			return fmt.Errorf("league %s: label_prefix is required", lg.Key)
		case lg.Duration <= 0:
			return fmt.Errorf("league %s: duration is required", lg.Key)
		case lg.SeriesID == "":
			return fmt.Errorf("league %s: series_id is required", lg.Key)
		case lg.ChannelSlot < 0:
			return fmt.Errorf("league %s: channel_slot must not be negative", lg.Key)
		case TeamOffset%lg.SlotSpan != 0:
			return fmt.Errorf("league %s: slot_span %d must divide %d", lg.Key, lg.SlotSpan, TeamOffset)
		}
		if _, _, _, ok := lg.RGB(); !ok {
			return fmt.Errorf("league %s: color %q is not a #rrggbb hex", lg.Key, lg.Color)
		}
		if err := c.checkLogoIDs(&lg); err != nil {
			return err
		}
		if other, dup := seenSeries[lg.SeriesID]; dup {
			return fmt.Errorf("leagues %s and %s share series_id %s", other, lg.Key, lg.SeriesID)
		}
		seenSeries[lg.SeriesID] = lg.Key
		if other, dup := seenSlot[lg.ChannelSlot]; dup {
			return fmt.Errorf("leagues %s and %s share channel_slot %d", other, lg.Key, lg.ChannelSlot)
		}
		seenSlot[lg.ChannelSlot] = lg.Key
		if lg.WomensSeriesID != "" {
			if other, dup := seenSeries[lg.WomensSeriesID]; dup {
				return fmt.Errorf("leagues %s and %s share series_id %s", other, lg.Key, lg.WomensSeriesID)
			}
			seenSeries[lg.WomensSeriesID] = lg.Key + " (women's)"
		}
		if lg.Roster != "" {
			if _, ok := c.rosters[lg.Roster]; !ok {
				return fmt.Errorf("league %s: roster %q not found in the manifest", lg.Key, lg.Roster)
			}
		}
		if lg.WomensRoster != "" {
			if _, ok := c.rosters[lg.WomensRoster]; !ok {
				return fmt.Errorf("league %s: womens_roster %q not found", lg.Key, lg.WomensRoster)
			}
		}
	}
	return c.checkBlocks()
}

// ValidateOverrides reports the first problem with a set of overrides taken together — the
// kind no single override can answer for, because it is about two leagues at once. Every
// league is read as its override leaves it, so a start the user chose is checked against the
// blocks other leagues actually occupy rather than the ones they shipped with.
func (c *Catalog) ValidateOverrides(overrides map[string]Override) error {
	for key, o := range overrides {
		if err := o.Validate(); err != nil {
			return fmt.Errorf("%s: %w", key, err)
		}
	}
	return c.WithOverrides(overrides).checkBlocks()
}

// checkBlocks rejects two leagues whose thousand-wide number blocks would share a number.
// It is separate from validate so the same rule can be applied to a set of user overrides,
// where a league's start is chosen rather than shipped: one algorithm, one message, and no
// way for a hand-picked start to be held to a laxer standard than the manifest is.
func (c *Catalog) checkBlocks() error {
	type block struct {
		lo, hi int
		key    string
	}
	blocks := make([]block, 0, len(c.Leagues))
	for _, lg := range c.Leagues {
		blocks = append(blocks, block{lg.ChannelBase, lg.ChannelBase + BlockSize, lg.Key})
	}
	slices.SortFunc(blocks, func(a, b block) int { return cmp.Compare(a.lo, b.lo) })
	for i := 1; i < len(blocks); i++ {
		if blocks[i].lo < blocks[i-1].hi {
			return fmt.Errorf("leagues %s and %s have overlapping channel number blocks", blocks[i-1].key, blocks[i].key)
		}
	}
	return nil
}

// checkLogoIDs rejects a team addressed within a league that never says where its marks
// live, which would leave the id pointing at nothing.
func (c *Catalog) checkLogoIDs(lg *League) error {
	if lg.LogoPath != "" {
		return nil
	}
	for _, name := range []string{lg.Roster, lg.WomensRoster} {
		ti := c.rosters[name]
		if ti == nil {
			continue
		}
		for _, t := range ti.Teams {
			if t.LogoID != "" {
				return fmt.Errorf("league %s: %s has a logo_id but the league has no logo_path", lg.Key, t.Name)
			}
		}
	}
	return nil
}

// League returns a league by key.
func (c *Catalog) League(key string) (*League, bool) {
	lg, ok := c.byKey[key]
	return lg, ok
}

// MatchLeague resolves a playlist entry to a league using its group title, then the
// channel name as a fallback. Exclude patterns are checked against both.
func (c *Catalog) MatchLeague(group, name string) (*League, bool) {
	for i := range c.Leagues {
		lg := &c.Leagues[i]
		if lg.excluded(group) || lg.excluded(name) {
			continue
		}
		if matchAny(lg.groupRes, group) {
			return lg, true
		}
	}
	for i := range c.Leagues {
		lg := &c.Leagues[i]
		if lg.excluded(name) {
			continue
		}
		if matchAny(lg.nameRes, name) {
			return lg, true
		}
	}
	return nil, false
}

func (lg *League) excluded(s string) bool { return matchAny(lg.excludeRes, s) }

// LabelPattern is a regex alternation matching this league's slot label as providers
// write it: the prefix ("NFL") or any label alias ("NHL Game").
func (lg *League) LabelPattern() string {
	alts := append([]string{regexp.QuoteMeta(lg.LabelPrefix)}, lg.LabelAliases...)
	return "(?:" + strings.Join(alts, "|") + ")"
}

// Location is the zone for this league's schedule text: its own when set, else fallback.
func (lg *League) Location(fallback *time.Location) *time.Location {
	if lg.Loc != nil {
		return lg.Loc
	}
	return fallback
}

// MaxFamilies is how many provider styles a league can hold.
func (lg *League) MaxFamilies() int { return TeamOffset / lg.SlotSpan }

// SlotChannelNumber is the number a slot would like in a provider family (0-based).
// Family 0 is the plain numbering: NFL 03 is 10003. A slot beyond the league's span has
// no number of its own to ask for, and takes whatever the block has free.
func (lg *League) SlotChannelNumber(family, slot int) int {
	if slot >= lg.SlotSpan {
		return 0
	}
	return lg.ChannelBase + family*lg.SlotSpan + slot
}

// TeamChannelBase is where this league's team channels start.
func (lg *League) TeamChannelBase() int { return lg.ChannelBase + TeamOffset }

// SlotRange and TeamRange are the halves of this league's block: the numbers its event
// channels draw from, and the numbers its team channels draw from.
func (lg *League) SlotRange() (from, to int) { return lg.ChannelBase, lg.TeamChannelBase() }
func (lg *League) TeamRange() (from, to int) { return lg.TeamChannelBase(), lg.ChannelBase + BlockSize }

// ChannelID is the channel id for a slot: "NFL 03" for the first provider family,
// "NFL 03 B" for the second, and so on.
func (lg *League) ChannelID(family, slot int) string {
	id := fmt.Sprintf("%s %02d", lg.LabelPrefix, slot)
	if family > 0 {
		id += " " + string(rune('A'+family))
	}
	return id
}

// SeriesIDFor returns the series id for a game, using the women's id when set.
func (lg *League) SeriesIDFor(womens bool) string {
	if womens && lg.WomensSeriesID != "" {
		return lg.WomensSeriesID
	}
	return lg.SeriesID
}

// AiringTitleFor returns the airing title for a game.
func (lg *League) AiringTitleFor(womens bool) string {
	if womens && lg.WomensAiringTitle != "" {
		return lg.WomensAiringTitle
	}
	return lg.AiringTitle
}

// Teams returns the roster index for this league, or the women's roster when womens is set.
func (c *Catalog) Teams(lg *League, womens bool) *TeamIndex {
	name := lg.Roster
	if womens && lg.WomensRoster != "" {
		name = lg.WomensRoster
	}
	return c.rosters[name]
}

// Roster returns a roster by its CSV league name.
func (c *Catalog) Roster(name string) (*TeamIndex, bool) {
	ti, ok := c.rosters[name]
	return ti, ok
}

func matchAny(res []*regexp.Regexp, s string) bool {
	for _, re := range res {
		if re.MatchString(s) {
			return true
		}
	}
	return false
}

// Override is a user's changes to one league. Pointers distinguish "not overridden"
// from an explicit value; durations are Go duration strings so the type stores as JSON.
type Override struct {
	AiringTitle *string `json:"airing_title,omitempty"`
	Duration    *string `json:"duration,omitempty"`
	StartPad    *string `json:"start_pad,omitempty"`
	Logo        *string `json:"logo,omitempty"`
	Placard     *string `json:"placard,omitempty"`

	// ChannelBase is where this league's numbers start. Setting it hands the league's
	// numbering to epg3r: every channel in it is numbered from here, and the Lineup refuses
	// to renumber them by hand. Unset means the shipped start and numbers the user owns.
	ChannelBase *string `json:"channel_base,omitempty"`
}

// Managed reports whether the user has handed this league's numbering over. A league with a
// start of its own has no numbers set by hand: they are all derived from it.
func (o Override) Managed() bool {
	_, ok, _ := overrideNumber("channel_base", o.ChannelBase)
	return ok
}

// IsZero reports whether nothing is overridden. Every field is a pointer, so the
// zero value stays correct as fields are added.
func (o Override) IsZero() bool { return o == Override{} }

// Validate checks the override's values.
func (o Override) Validate() error {
	if _, _, err := overrideDuration("duration", o.Duration, false); err != nil {
		return err
	}
	if _, _, err := overrideDuration("start_pad", o.StartPad, true); err != nil {
		return err
	}
	_, _, err := overrideNumber("channel_base", o.ChannelBase)
	return err
}

// maxChannelBase keeps a start and the block above it inside sane numbers, so a fat-fingered
// 85000000 is caught here rather than producing a guide nobody can navigate.
const maxChannelBase = 999_000

// overrideNumber reads a channel number the user typed. Like overrideDuration, ok is false
// when the field is not overridden at all, so a caller can tell "leave it alone" from "use
// this" — and both the form and the run reach the value through here, so they cannot disagree
// about what is acceptable.
func overrideNumber(name string, s *string) (n int, ok bool, err error) {
	if s == nil || strings.TrimSpace(*s) == "" {
		return 0, false, nil
	}
	n, err = strconv.Atoi(strings.TrimSpace(*s))
	if err != nil {
		return 0, false, fmt.Errorf("%s: %q is not a whole number", name, *s)
	}
	if n <= 0 || n > maxChannelBase {
		return 0, false, fmt.Errorf("%s must be a channel number between 1 and %d", name, maxChannelBase)
	}
	return n, true, nil
}

// overrideDuration reads one of the two duration fields. ok is false when the field is
// not overridden at all, so callers can tell "leave it alone" from "use this". Both the
// form and the run go through here, so they cannot disagree on what is acceptable.
func overrideDuration(name string, s *string, allowZero bool) (d time.Duration, ok bool, err error) {
	if s == nil || *s == "" {
		return 0, false, nil
	}
	d, err = time.ParseDuration(*s)
	if err != nil {
		return 0, false, fmt.Errorf("%s: %q is not a duration like 3h30m", name, *s)
	}
	if d < 0 || (d == 0 && !allowZero) {
		return 0, false, fmt.Errorf("%s must be positive", name)
	}
	return d, true, nil
}

// WithChannelStart returns a catalog whose leagues are laid out from start, each in its own
// block in manifest order. Every league moves together, so there is no arrangement for a user
// to get wrong and no pair of blocks that can be made to overlap.
func (c *Catalog) WithChannelStart(start int) *Catalog {
	if start <= 0 || start == c.ChannelStart() {
		return c
	}
	out := &Catalog{Leagues: slices.Clone(c.Leagues), byKey: map[string]*League{}, rosters: c.rosters, tokens: c.tokens}
	for i := range out.Leagues {
		lg := &out.Leagues[i]
		lg.ChannelBase = start + lg.ChannelSlot*BlockSize
		out.byKey[lg.Key] = lg
	}
	return out
}

// ChannelStart is where this catalog's shelf begins: the base of the league in the first
// block, which is the number the user chose.
func (c *Catalog) ChannelStart() int {
	if len(c.Leagues) == 0 {
		return DefaultChannelStart
	}
	lg := c.Leagues[0]
	return lg.ChannelBase - lg.ChannelSlot*BlockSize
}

// ShelfRange is every number the shelf can reach, from the first block to the end of the
// last. Moving the shelf is a translation of this one span.
func (c *Catalog) ShelfRange() (from, to int) {
	last := 0
	for i := range c.Leagues {
		last = max(last, c.Leagues[i].ChannelSlot)
	}
	start := c.ChannelStart()
	return start, start + (last+1)*BlockSize
}

// WithOverrides returns a catalog with user overrides applied to copies of the
// leagues. Rosters and compiled patterns are shared with the receiver.
func (c *Catalog) WithOverrides(overrides map[string]Override) *Catalog {
	if len(overrides) == 0 {
		return c
	}
	out := &Catalog{Leagues: slices.Clone(c.Leagues), byKey: map[string]*League{}, rosters: c.rosters, tokens: c.tokens}
	for i := range out.Leagues {
		lg := &out.Leagues[i]
		if o, ok := overrides[lg.Key]; ok {
			*lg = lg.With(o)
		}
		out.byKey[lg.Key] = lg
	}
	return out
}

// With returns this league as the user's override leaves it. It is the whole of what an
// override means, so anything that needs to know what a league will actually do — a run, or
// a page showing what would go out — asks here rather than reading the fields itself.
func (lg League) With(o Override) League {
	if o.AiringTitle != nil && *o.AiringTitle != "" {
		lg.AiringTitle = *o.AiringTitle
	}
	if d, ok, _ := overrideDuration("duration", o.Duration, false); ok {
		lg.Duration = d
	}
	if d, ok, _ := overrideDuration("start_pad", o.StartPad, true); ok {
		lg.StartPad = d
	}
	if o.Logo != nil {
		lg.Logo = *o.Logo
	}
	if o.Placard != nil {
		lg.Placard = *o.Placard
	}
	if n, ok, _ := overrideNumber("channel_base", o.ChannelBase); ok {
		lg.ChannelBase = n
	}
	return lg
}
