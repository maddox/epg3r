package catalog

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/runes"
	"golang.org/x/text/unicode/norm"
)

// Team is one roster entry.
type Team struct {
	Key          string   `json:"key"`                     // slug of the name, stable within a roster
	Roster       string   `json:"roster"`                  // roster name
	Name         string   `json:"name"`                    // "Green Bay Packers"
	Abbr         string   `json:"abbr,omitempty"`          // "GB"
	TMSBrandID   string   `json:"tms_brand_id"`            // Gracenote teamBrandId; emitted as <team-id system="tms">
	TMSTeamID    string   `json:"tms_team_id,omitempty"`   // Gracenote franchise id, kept for reference
	UniversityID string   `json:"university_id,omitempty"` // NCAA only
	LogoID       string   `json:"logo_id,omitempty"`       // this team's id within its league's logo_path; resolved offline
	Aliases      []string `json:"-"`                       // hand-maintained spellings from the manifest
}

// MatchMethod says how a name was resolved.
type MatchMethod string

const (
	MatchExact MatchMethod = "exact" // normalized name, abbreviation, or alias
	MatchFuzzy MatchMethod = "fuzzy" // Jaro-Winkler above threshold
)

// TeamIndex resolves free-text team names within one roster.
type TeamIndex struct {
	Roster  string
	Teams   []*Team
	exact   map[string]*Team   // normalized alias -> team (unique only)
	aliases map[*Team][]string // every normalized alias per team, for fuzzy scoring
}

const (
	fuzzyThreshold = 0.90
	fuzzyMargin    = 0.03
)

// rosterFile is one entry under rosters in the manifest.
type rosterFile struct {
	Roster string `yaml:"roster"`
	Teams  []struct {
		Name         string   `yaml:"name"`
		Abbr         string   `yaml:"abbr"`
		TMSBrandID   string   `yaml:"tms_brand_id"`
		TMSTeamID    string   `yaml:"tms_team_id"`
		UniversityID string   `yaml:"university_id"`
		LogoID       string   `yaml:"logo_id"`
		Aliases      []string `yaml:"aliases"`
	} `yaml:"teams"`
}

func buildRosters(files []rosterFile) (map[string]*TeamIndex, error) {
	out := map[string]*TeamIndex{}
	for _, rf := range files {
		if rf.Roster == "" {
			return nil, fmt.Errorf("catalog.yaml: roster with no name")
		}
		if _, dup := out[rf.Roster]; dup {
			return nil, fmt.Errorf("catalog.yaml: roster %q defined twice", rf.Roster)
		}
		seen := map[string]bool{}
		teams := make([]*Team, 0, len(rf.Teams))
		for _, t := range rf.Teams {
			name := strings.TrimSpace(t.Name)
			switch {
			case name == "":
				return nil, fmt.Errorf("roster %s: team with no name", rf.Roster)
			case seen[name]:
				return nil, fmt.Errorf("roster %s: %q listed twice", rf.Roster, name)
			case t.TMSBrandID == "":
				return nil, fmt.Errorf("roster %s: %q has no tms_brand_id", rf.Roster, name)
			}
			seen[name] = true
			teams = append(teams, &Team{
				Key: slug(name), Roster: rf.Roster, Name: name, Abbr: strings.TrimSpace(t.Abbr),
				TMSBrandID: t.TMSBrandID, TMSTeamID: t.TMSTeamID, UniversityID: t.UniversityID,
				LogoID: strings.TrimSpace(t.LogoID), Aliases: t.Aliases,
			})
		}
		out[rf.Roster] = newTeamIndex(rf.Roster, teams)
	}
	return out, nil
}

func newTeamIndex(roster string, teams []*Team) *TeamIndex {
	slices.SortFunc(teams, func(a, b *Team) int { return strings.Compare(a.Name, b.Name) })
	ti := &TeamIndex{Roster: roster, Teams: teams, exact: map[string]*Team{}, aliases: map[*Team][]string{}}

	owners := map[string]map[*Team]bool{}
	add := func(t *Team, alias string) {
		n := Normalize(alias)
		if n == "" {
			return
		}
		if owners[n] == nil {
			owners[n] = map[*Team]bool{}
		}
		if !owners[n][t] {
			owners[n][t] = true
			ti.aliases[t] = append(ti.aliases[t], n)
		}
	}

	for _, t := range teams {
		add(t, t.Name)
		if len(t.Abbr) >= 2 {
			add(t, t.Abbr)
		}
		for _, a := range t.Aliases {
			add(t, a)
		}
		if !isCollegiate(roster) {
			for _, g := range generatedAliases(t.Name) {
				add(t, g)
			}
		}
	}

	// Only aliases owned by exactly one team resolve exactly. "Sox" in MLB or
	// "New York" in the NFL are ambiguous and are dropped from the exact map, though
	// they still contribute to fuzzy scoring.
	for alias, ts := range owners {
		if len(ts) == 1 {
			for t := range ts {
				ti.exact[alias] = t
			}
		}
	}
	return ti
}

// cityAbbrevs are common short forms providers use in place of a city.
var cityAbbrevs = map[string][]string{
	"los angeles":   {"la", "l a"},
	"new york":      {"ny", "n y"},
	"new england":   {"ne"},
	"new jersey":    {"nj"},
	"new orleans":   {"no", "nola"},
	"san francisco": {"sf"},
	"san antonio":   {"sa"},
	"san diego":     {"sd"},
	"san jose":      {"sj"},
	"tampa bay":     {"tb"},
	"golden state":  {"gs", "gsw"},
	"oklahoma city": {"okc"},
	"kansas city":   {"kc"},
	"st louis":      {"stl"},
	"green bay":     {"gb"},
	"las vegas":     {"lv"},
	"salt lake":     {"sl"},
	"washington":    {"wsh", "wash"},
	"philadelphia":  {"philly", "phila"},
}

// isCollegiate reports whether a roster holds school names rather than "City Nickname"
// names. Generated nickname and city aliases only make sense for the latter: "Western
// Illinois" has no nickname, and treating its last word as one would swallow "Illinois".
func isCollegiate(roster string) bool { return strings.HasPrefix(roster, "NCAA") }

// generatedAliases derives the variants a provider is likely to write for a pro team
// name: the nickname ("Packers"), a two-word nickname ("Trail Blazers"), the city
// alone ("Edmonton"), and the city's common abbreviations ("LA Lakers"). Ambiguous
// results (two teams sharing a city) are dropped by the caller's uniqueness rule.
func generatedAliases(name string) []string {
	n := Normalize(name)
	words := strings.Fields(n)
	var out []string
	if len(words) >= 2 {
		out = append(out, words[len(words)-1])                     // "packers"
		out = append(out, strings.Join(words[len(words)-2:], " ")) // "trail blazers"
		out = append(out, strings.Join(words[:len(words)-1], " ")) // "edmonton"
	}
	for city, abbrs := range cityAbbrevs {
		if strings.HasPrefix(n, city+" ") {
			rest := strings.TrimPrefix(n, city+" ")
			for _, a := range abbrs {
				out = append(out, a+" "+rest)
			}
		}
	}
	return out
}

// ShortName is the name used in a team channel id: the nickname for pro teams
// ("Bills"), two words when one would be ambiguous in the roster ("Red Sox"), and the
// full school name for college teams.
func (ti *TeamIndex) ShortName(t *Team) string {
	if isCollegiate(ti.Roster) {
		return t.Name
	}
	words := strings.Fields(t.Name)
	if len(words) < 2 {
		return t.Name
	}
	last := words[len(words)-1]
	for _, o := range ti.Teams {
		if o != t {
			ow := strings.Fields(o.Name)
			if len(ow) > 0 && strings.EqualFold(ow[len(ow)-1], last) {
				return strings.Join(words[len(words)-2:], " ")
			}
		}
	}
	return last
}

// Match resolves a team name written by a provider. It returns nil when nothing is
// close enough; callers keep the raw text in that case.
func (ti *TeamIndex) Match(name string) (*Team, MatchMethod, float64) {
	if ti == nil {
		return nil, "", 0
	}
	n := Normalize(name)
	if n == "" {
		return nil, "", 0
	}
	if t, ok := ti.exact[n]; ok {
		return t, MatchExact, 1
	}

	// Every alias is scored, including the ones that cannot win: the runner-up decides
	// whether the best score is clear enough to trust, so skipping the field would
	// change which matches are accepted.
	var best, second float64
	var bestTeam *Team
	nr := []rune(n)
	for _, t := range ti.Teams { // deterministic order
		for _, a := range ti.aliases[t] {
			s := jaroWinklerRunes(nr, []rune(a))
			switch {
			case s > best:
				if bestTeam != t {
					second = best
				}
				best, bestTeam = s, t
			case s > second && t != bestTeam:
				second = s
			}
		}
	}
	if bestTeam != nil && best >= fuzzyThreshold && best-second >= fuzzyMargin {
		return bestTeam, MatchFuzzy, best
	}
	return nil, "", 0
}

// Normalize folds a name for comparison: NFKD, strip accents, lowercase, "&" to "and",
// punctuation to spaces, collapsed whitespace. Apostrophes are removed so "A's" is "as".
func Normalize(s string) string {
	s = norm.NFKD.String(s)
	s = runes.Remove(runes.In(unicode.Mn)).String(s)
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "&", " and ")
	var b strings.Builder
	b.Grow(len(s))
	lastSpace := true
	for _, r := range s {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
			lastSpace = false
		case r == '\'' || r == '’':
		default:
			if !lastSpace {
				b.WriteByte(' ')
				lastSpace = true
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func slug(s string) string { return strings.ReplaceAll(Normalize(s), " ", "-") }

// jaroWinklerRunes returns string similarity in [0,1], taking runes so a caller
// comparing one string against many converts it once.
func jaroWinklerRunes(ra, rb []rune) float64 {
	la, lb := len(ra), len(rb)
	if la == 0 || lb == 0 {
		return 0
	}
	window := max(0, max(la, lb)/2-1)
	ma := make([]bool, la)
	mb := make([]bool, lb)
	matches := 0
	for i := 0; i < la; i++ {
		lo := max(0, i-window)
		hi := min(lb-1, i+window)
		for j := lo; j <= hi; j++ {
			if !mb[j] && ra[i] == rb[j] {
				ma[i], mb[j] = true, true
				matches++
				break
			}
		}
	}
	if matches == 0 {
		return 0
	}
	trans := 0
	k := 0
	for i := 0; i < la; i++ {
		if !ma[i] {
			continue
		}
		for !mb[k] {
			k++
		}
		if ra[i] != rb[k] {
			trans++
		}
		k++
	}
	m := float64(matches)
	jaro := (m/float64(la) + m/float64(lb) + (m-float64(trans)/2)/m) / 3
	prefix := 0
	for prefix < min(4, la, lb) && ra[prefix] == rb[prefix] {
		prefix++
	}
	return jaro + float64(prefix)*0.1*(1-jaro)
}
