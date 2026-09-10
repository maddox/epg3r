# epg3r

Real guide data for the sports channels your IPTV provider gives you.

## The problem

Most IPTV providers carry live sports on channels that only exist for one game. The channel
is called something like `NFL 04: Bills vs Texans (09/13) 1:00PM ET` today, and something
else entirely tomorrow. Some providers also give each team its own channel.

Those channels almost never come with guide data. In Channels DVR they show up blank — you
can't see what's on, and you can't record anything.

## What epg3r does

epg3r reads your provider's channel list, works out what each channel actually is, and
builds a proper guide from it.

It figures out the league, the teams, and when the game starts. It gives every channel a
stable number and a logo, and every game a listing with both teams, artwork, and a start
time. If the same game is on three different channels, they share one listing, so recording
it once is enough.

Then it hands Channels DVR two links, and your sports channels look like every other channel
you have.

## Getting it running

You need Docker and a playlist link from your provider.

Make a `docker-compose.yml`:

```yaml
services:
  epg3r:
    image: ghcr.io/maddox/epg3r:latest
    container_name: epg3r
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      - epg3r-data:/data

volumes:
  epg3r-data:
```

Then start it:

```
docker compose up -d
```

Open `http://localhost:8080` (or your server's address) and epg3r asks you two things:

1. **Your provider's links.** The playlist link is required — it usually ends in `.m3u`. If
   your provider also publishes a guide, add that too; it ends in `.xml` and helps epg3r get
   game times right. epg3r checks both before moving on, so you'll know right away if a link
   is wrong.
2. **Where to start your channel numbers.** 10000 by default. Pick a range nothing else on
   your system is using.

That's it. epg3r builds your guide, which takes a minute or two the first time.

## Connecting it to Channels DVR

In Channels DVR, add a custom channel source using these two links:

- **Playlist:** `http://your-server:8080/m3u`
- **Guide:** `http://your-server:8080/xmltv`

Both links are on the epg3r dashboard, ready to copy.

## What you'll see

**Lineup** is every channel epg3r found, with what's on now and what's next. You can filter
by league, search, and click any channel to see everything scheduled on it.

**Channel numbers** are handed out automatically. Each league gets its own block of a
thousand, so NFL channels sit together, MLB channels sit together, and so on. You can move
the whole set by changing where the numbers start, or renumber individual channels yourself.

**Artwork** is drawn by epg3r. Team channels get their team's logo, event channels get their
league's, and each game gets a picture with both teams on it.

**Collections** let you group channels and give that group its own pair of links. Handy if
you only want football in one place, or want to hand a smaller set to something else.

**History** shows every time epg3r rebuilt your guide and what happened to each channel — a
good first stop if something looks wrong.

## Leagues it knows

NFL, MLB, NBA, NHL, WNBA, MLS, and NCAA football and basketball, men's and women's.

Channels epg3r doesn't recognize are left alone. Real networks like ESPN or NFL Network are
identified but skipped, since they already have guide data from somewhere else.

## Settings worth knowing

Most settings can be left as they are. Two that sometimes matter:

- **How often to check for new listings.** Hourly by default.
- **The address other apps use to reach epg3r.** Leave this empty unless epg3r answers on
  more than one address. If artwork isn't showing up in Channels DVR, this is usually why.

## Keeping it up to date

epg3r tells you in the header when a newer version is out, with a link to what changed. To
update:

```
docker compose pull
docker compose up -d
```

Your settings, channel numbers, and collections are kept in the `epg3r-data` volume, so
nothing is lost.

## Other things

Nothing about how epg3r reads your channels is configurable — which league a channel belongs
to, and which teams are playing, are worked out from the channel's own name. If something
lands in the wrong league, that's a bug worth
[reporting](https://github.com/maddox/epg3r/issues), not a setting to change.

Team logos are fetched once and cached. A team epg3r has no logo for gets one drawn from its
name instead, which covers most college teams.

If you'd rather run it without Docker Compose:

```
docker run -d --name epg3r -p 8080:8080 -v epg3r-data:/data ghcr.io/maddox/epg3r:latest
```

## Building it yourself

Everything runs in Docker; nothing gets installed on your machine.

```
make run     start it from source, with live reload
make test    run the tests
make help    everything else
```
