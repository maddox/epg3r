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
women's). Channel kinds:

- **Slot channels** (`NFL 04`, `NCAAF 008 | WEST GEORGIA AT KENNESAW STATE | 09/03 07:00PM | ESPN+`).
  Dozens of provider formats are handled through normalization and segment
  classification rather than one regex per provider. Several providers in one playlist
  may all have an "NFL 04"; each provider style gets its own family of channels
  (`NFL 04`, `NFL 04 B`, ...) with stable numbers.
- **Team channels** (`US NFL Buffalo Bills (HD)`, `NBALP: Oklahoma City Thunder`). Their
  games come from the provider's XMLTV when it has them, and otherwise from the slot
  channels: if a slot says Bills vs Texans on Sunday, both team channels carry it.
- **Placeholders** (`Offline`, `No Event Scheduled`, bare labels) stay in the lineup as
  idle channels so Channels DVR does not see channels appear and disappear.
- **Network channels** (NFL Network, ESPN, local affiliates) are recognised and listed
  but not exported; they have real guide data elsewhere.

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
