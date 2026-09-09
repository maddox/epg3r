# epg3r

Channels DVR friendly guide data for the sports channels IPTV providers hand out.

IPTV providers carry live sports on channels that exist only for one game: a numbered
slot whose name changes to whatever is on ("NFL 04: Bills vs Texans (Sunday 09/13) 1:00PM
ET"), or a channel dedicated to one team. Their guide data for those channels is empty,
so Channels DVR shows nothing and cannot record them. epg3r reads the provider's M3U
(and their XMLTV if they have one), works out which league, teams, and kickoff each
channel carries, and serves an M3U and XMLTV that Channels DVR understands: stable
channel ids and numbers, one airing per game with the matchup, categories, Gracenote
team ids, and a shared episode id so the same game on three channels records once.

## Run it

From a checkout: `cp .env.example .env`, put your playlist URL in it, then `make up`.
`make logs` follows the log, `make stop` stops it, `make reset` also wipes its data.

Or with the published image:

```yaml
services:
  epg3r:
    image: ghcr.io/jonmaddox/epg3r:latest
    ports: ["8080:8080"]
    environment:
      EPG3R_M3U_URL: "http://provider.example/playlist.m3u"
      # EPG3R_XMLTV_URL: "http://provider.example/guide.xml"   # optional
    volumes:
      - epg3r-data:/data
volumes:
  epg3r-data:
```

Then add a custom channel source in Channels DVR:

- Playlist: `http://<host>:8080/m3u`
- Guide: `http://<host>:8080/xmltv`

The guide refreshes hourly (setting `refresh_interval_minutes`) and on start.

### Environment

| Variable | Default | Purpose |
|---|---|---|
| `EPG3R_DATA_DIR` | `/data` | SQLite database and caches |
| `EPG3R_LISTEN` | `:8080` | HTTP listen address |
| `EPG3R_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `EPG3R_LOG_FORMAT` | `text` | `text` or `json` |
| `EPG3R_M3U_URL` | | First-boot seed: creates a source |
| `EPG3R_XMLTV_URL` | | First-boot seed: the source's provider guide |
| `EPG3R_REFRESH_INTERVAL` | | First-boot seed, in minutes |
| `EPG3R_TIMEZONE` | | First-boot seed for the default zone (`America/New_York`) |
| `EPG3R_PUBLIC_URL` | | First-boot seed for absolute URLs in the output |

Seeds apply only to values never set before, so anything you later change in the
database (or the web UI, when it lands) wins over the environment.

### Commands

```
epg3r serve                              run the server and scheduler (default)
epg3r run-once                           one refresh, print the report, exit
epg3r parse-title -group NFL "<title>"   show how a channel title is parsed
epg3r healthcheck                        used by the Docker HEALTHCHECK
epg3r version
```

## What it understands

Leagues: NFL, MLB, NBA, NHL, WNBA, MLS, NCAA Football, NCAA Basketball (men's and
women's). Channel types:

- **Event channels** (`NFL 04`, `NCAAF 008 | WEST GEORGIA AT KENNESAW STATE | 09/03 07:00PM | ESPN+`).
  Dozens of provider formats are handled through normalization and segment
  classification rather than one regex per provider. Several providers in one playlist
  may all have an "NFL 04"; each provider style gets its own family of channels
  (`NFL 04`, `NFL 04 B`, ...) with stable numbers.
- **Team channels** (`US NFL Buffalo Bills (HD)`, `NBALP: Oklahoma City Thunder`). Their
  games come from the provider's XMLTV when it has them, and otherwise from the slot
  channels: if a slot says Bills vs Texans on Sunday, both team channels carry it.
- **Unused channels** (`Offline`, `No Event Scheduled`, bare labels) are numbered slots
  the provider has parked. They stay in the lineup so the consumer does not see channels
  appear and disappear.
- **Network channels** (NFL Network, ESPN, local affiliates) are recognised and listed
  but not exported; they have real guide data elsewhere.

### Looking at what you get

A channel's properties come from parsing: which league it belongs to, whether it is an
event channel or a team channel, which teams it carries. None of that is configurable. If
a channel lands in the wrong league, that is a bug in the catalog or the parser, not a
setting.

- **Lineup** lists every channel with what is on now and next. Filter by league, by type,
  by team, by whether anything is scheduled, or search what the table shows. Click a
  channel to see all of its airings.
- **Leagues** adjusts how a league's airings are described: airing title, game length,
  early start, and the art the league itself wears.

`/m3u` and `/xmltv` are the whole guide: every event-carrying channel epg3r recognises,
with nothing in the app taking any of them out.

### Art

Every channel gets a logo and every airing a picture, served by epg3r itself at `/art`.
A team channel wears its team's crest, an event channel its league's mark, and a game gets
a placard with both teams on it — falling back to the league's when a side did not resolve.

Crests are fetched once, cached under the data directory, and never redistributed; a team
epg3r has no crest for is drawn from its name instead, which is most of the college
rosters. Nothing is fetched during a refresh: a run writes paths, and a picture is drawn
the first time something asks for it.

Because the guide points at these by URL, `public_base_url` matters more than it used to.
Leave it empty and each request is answered with links built from the host it arrived on,
which is right for most setups; set it when more than one hostname reaches the app.

A league's own logo and its fallback airing art can be replaced on the Leagues page. Each
replaces only what epg3r draws for the league itself: a team channel still wears its own
team, and a game still gets its matchup.

**Collections** are how you choose what a consumer sees. A collection is a set of
channels you pick out — every team channel, or just the teams you follow — served at its
own `/m3u/<name>` and `/xmltv/<name>`. Point one consumer at the whole guide and another
at a collection, or use collections only. Tick channels in the Lineup — shift-click takes a
range — then Actions › Add to collection, picking an existing one or naming a new one; open a collection to see just its
channels and take any back out. A collection is a view of channels, not a copy: deleting
one leaves the channels alone, and a channel that goes for good leaves the collections it
was in.

Each change on the Leagues page triggers a refresh, so the guide follows within a second
or two.

A channel is its stream URL: identity is a hash of the source and that URL, and nothing
about the title takes part, because a provider rewrites titles every week. The number a
channel is published under is written once and read back for the life of the row.

When a provider changes its stream URLs, though, the channels behind them are new as far
as epg3r can tell — there is no identifier the two have in common. So numbers are not
held forever: a channel gone from every playlist for longer than **Forget channels after
(days)** is dropped and its number freed for another. Set that longer if your provider
drops channels out of season, shorter if it churns URLs often.

Numbers are yours to set. Tick channels in the Lineup, shift-click to take a range, give a starting number, and they
take consecutive numbers in the order shown; a number you set is marked with a dot and is
never reassigned. Playlists carry no channel numbers of their own, so what epg3r hands
out is only a starting point — the numbers that suit the lineup you are inserting into
are the ones only you know.

Channel numbers: each league owns a block of 1,000 starting at 8500 for the NFL
(so `NFL 03` is 8503), then 9500 MLB, 10500 MLS, 11500 NBA, 12500 NHL, 13500 WNBA,
14500 NCAAF, 15500 NCAAB. Team channels sit at the top of each block.

Leagues and rosters live in one manifest, `internal/catalog/data/catalog.yaml`: league
settings first, then a roster per Gracenote team list with each team's id (emitted as
`<team-id system="tms">`) and any extra spellings providers use. Add an alias there when
a team name goes unmatched. Each league's airings share a Gracenote series id
(NFL 191277, MLB 191273, MLS 191274, NBA 191276, NHL 448880, WNBA 191289, NCAA Football
191261, NCAA Basketball 191260, NCAA Women's Basketball 191292); women's college games
are told apart by the `(W)` marker in their titles.

## Develop

Everything runs in Docker; nothing is installed on the host.

```
./dev         # run from source with hot reload, using .env; ./dev reset wipes ./data
make test     # go test -race in the dev container
make vet
make css      # rebuild the committed Tailwind CSS after editing internal/web/static/src/app.css
make build    # production image
make help
```

Test fixtures under `testdata/` are a real aggregated playlist (stream URLs replaced)
and the provider guide that came with it. `internal/titleparse/testdata/titles.yaml`
holds title examples with expected parses; add a case there when a provider's format
misparses.
