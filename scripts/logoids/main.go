// Command logoids resolves each team's id in the mark source's layout and writes it into
// the catalog as logo_id. It is run by hand (`make logo-ids`) and its output is committed,
// so the app never looks an id up at runtime: a team either has one and gets its real mark,
// or has none and is drawn from its name. Rosters change a few times a year; a lookup on
// every render would be a network dependency bought for nothing.
//
// Nothing here ships in the binary. It is a package main under scripts/ for that reason.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/jonmaddox/epg3r/internal/catalog"
)

// source is where one roster's ids come from. byID says how the mark source addresses this
// league: the pro leagues by abbreviation ("buf"), soccer and the colleges by number.
type source struct {
	roster string
	api    string
	byID   bool
}

var sources = []source{
	{roster: "NFL", api: "football/nfl"},
	{roster: "MLB", api: "baseball/mlb"},
	{roster: "NBA", api: "basketball/nba"},
	{roster: "NHL", api: "hockey/nhl"},
	{roster: "WNBA", api: "basketball/wnba"},
	{roster: "MLS", api: "soccer/usa.1", byID: true},
	{roster: "NCAA Football", api: "football/college-football", byID: true},
	{roster: "NCAA Basketball", api: "basketball/mens-college-basketball", byID: true},
	{roster: "NCAA Womens Basketball", api: "basketball/womens-college-basketball", byID: true},
}

const endpoint = "https://site.api.espn.com/apis/site/v2/sports/%s/teams?limit=1000"

type apiTeam struct {
	ID               string `json:"id"`
	Abbreviation     string `json:"abbreviation"`
	DisplayName      string `json:"displayName"`
	ShortDisplayName string `json:"shortDisplayName"`
	Location         string `json:"location"`
	Name             string `json:"name"`
	Nickname         string `json:"nickname"`
}

func main() {
	path := flag.String("catalog", "internal/catalog/data/catalog.yaml", "manifest to rewrite")
	dry := flag.Bool("n", false, "report what would change without writing")
	flag.Parse()

	cat, err := catalog.Load()
	if err != nil {
		log.Fatalf("load catalog: %v", err)
	}
	client := &http.Client{Timeout: 30 * time.Second}

	ids := map[string]map[string]string{} // roster -> team name -> id
	for _, src := range sources {
		roster, ok := cat.Roster(src.roster)
		if !ok {
			log.Fatalf("roster %q is not in the manifest", src.roster)
		}
		teams, err := fetch(client, src.api)
		if err != nil {
			log.Fatalf("%s: %v", src.roster, err)
		}
		ids[src.roster] = resolve(src, roster, teams)
		fmt.Printf("%-24s %4d of %4d teams\n", src.roster, len(ids[src.roster]), len(roster.Teams))
	}

	changed, err := rewrite(*path, ids, *dry)
	if err != nil {
		log.Fatal(err)
	}
	switch {
	case *dry:
		fmt.Printf("\n%d ids would be written to %s\n", changed, *path)
	default:
		fmt.Printf("\nwrote %d ids to %s\n", changed, *path)
	}
}

func fetch(client *http.Client, api string) ([]apiTeam, error) {
	resp, err := client.Get(fmt.Sprintf(endpoint, api))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: HTTP %d", api, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var doc struct {
		Sports []struct {
			Leagues []struct {
				Teams []struct {
					Team apiTeam `json:"team"`
				} `json:"teams"`
			} `json:"leagues"`
		} `json:"sports"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil, err
	}
	var out []apiTeam
	for _, s := range doc.Sports {
		for _, l := range s.Leagues {
			for _, t := range l.Teams {
				out = append(out, t.Team)
			}
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s: no teams in the response", api)
	}
	return out, nil
}

// resolve matches each team the source lists against the roster, using the same index the
// title parser uses so the ids are as good as the name matching already is.
//
// Several source teams reach one roster team all the time — "Alma Scots" scores 0.933
// against Alabama, and every satellite campus scores well against its parent — so a
// collision cannot mean "give up on both". The best claim wins: an exact match beats any
// fuzzy one, and among equals the higher score. Only a genuine tie leaves the team without
// an id, where a lettermark is right and a guess is not.
func resolve(src source, roster *catalog.TeamIndex, teams []apiTeam) map[string]string {
	type claim struct {
		token string
		rank  float64
		tied  bool
	}
	best := map[string]*claim{}
	for _, t := range teams {
		token := strings.ToLower(t.Abbreviation)
		if src.byID {
			token = t.ID
		}
		if token == "" {
			continue
		}
		// Try every name the source gives and keep the strongest reading, rather than the
		// first: "Ohio State Buckeyes" matches fuzzily where plain "Ohio State" is exact.
		var match *catalog.Team
		var rank float64
		for _, name := range []string{t.DisplayName, t.Location + " " + t.Name, t.ShortDisplayName, t.Nickname, t.Location} {
			if strings.TrimSpace(name) == "" {
				continue
			}
			m, method, score := roster.Match(name)
			if m == nil {
				continue
			}
			if method == catalog.MatchExact {
				score += 1 // any exact reading outranks every fuzzy one
			}
			if score > rank {
				match, rank = m, score
			}
		}
		if match == nil {
			continue
		}
		switch cur := best[match.Name]; {
		case cur == nil:
			best[match.Name] = &claim{token: token, rank: rank}
		case rank > cur.rank:
			*cur = claim{token: token, rank: rank}
		case rank == cur.rank && cur.token != token:
			cur.tied = true
		}
	}
	found := map[string]string{}
	for name, c := range best {
		if !c.tied {
			found[name] = c.token
		}
	}
	return found
}

var (
	rosterLine = regexp.MustCompile(`^  - roster: (.+)$`)
	teamLine   = regexp.MustCompile(`^      - name: (.+)$`)
)

// rewrite edits the manifest in place, line by line rather than through a YAML round trip:
// the file is hand-maintained and full of comments and deliberate ordering, and re-emitting
// it would rewrite all 12,000 lines to change a few hundred.
func rewrite(path string, ids map[string]map[string]string, dry bool) (int, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(string(body), "\n")

	var out []string
	var roster, team string
	var block []string
	changed := 0

	// flush emits the team block being collected, with its logo_id line put back in a
	// stable place: before aliases when there is one, so the hand-written part stays last.
	flush := func() {
		if team == "" {
			return
		}
		id := ids[roster][team]
		if id != "" {
			changed++
			line := fmt.Sprintf("        logo_id: '%s'", id)
			at := len(block)
			for i, l := range block {
				if strings.HasPrefix(l, "        aliases:") {
					at = i
					break
				}
			}
			block = slices.Insert(block, at, line)
		}
		out = append(out, block...)
		team, block = "", nil
	}

	for _, ln := range lines {
		if team != "" && strings.HasPrefix(ln, "        ") {
			if !strings.HasPrefix(ln, "        logo_id:") { // drop the old one; this is idempotent
				block = append(block, ln)
			}
			continue
		}
		flush()
		if m := rosterLine.FindStringSubmatch(ln); m != nil {
			roster = strings.TrimSpace(m[1])
		}
		if m := teamLine.FindStringSubmatch(ln); m != nil && roster != "" {
			team = strings.TrimSpace(m[1])
			block = []string{ln}
			continue
		}
		out = append(out, ln)
	}
	flush()

	if dry {
		return changed, nil
	}
	return changed, os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o644)
}
