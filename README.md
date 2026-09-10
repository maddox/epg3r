# epg3r!!

Real guide data for the sports channels your provider gives you.

## The problem

Most providers carry live sports via channels that only exist for one event. The channel
is called something like `NFL 04: Bills vs Texans (09/13) 1:00PM ET` today, and something
else entirely next week. Some providers also give teams their own channels.

These channels almost never come with proper guide data and you're expected to just use the title of the channel to know what's playing on them. These almost never work in software like Channels, as they show up blank — you can't see what's on, and you can't record anything.

## What epg3r does

epg3r reads your provider's channel list and optionally its XMLTV data to work out what each channel actually is, and
builds proper guide data from them. Then, the channels have real rich guide data.

It determines the league, the teams, and when the events start. It gives every channel a stable channel number, a channel logo. Airings are filled out with rich metadata including airing art showing the matchup, a proper start time, along with a description and categorical tags.

This makes the events on these channels not only look great in your guide, but also makes them recordable.

If the same event is on three different channels, they share one listing.

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

1. **Your provider's URLs.** The playlist URL is required — it usually ends in `.m3u`. If
   your provider also publishes a XMLTV guide data, add its URL too; it ends in `.xml` and gives epg3r even more data to work with.
2. **Where to start your channel numbers.** `10000` by default. Pick a range nothing else on
   your system is using.

That's it. epg3r builds your guide.

## Connecting it to other software

Find the URLs epg3r gives you on the dashboard and add them to your software.

## Sections

**Dashboard** Gives you a quick overview of how many channels epg3r found, how many it recognized, and how many airings it built.

**Lineup** is every channel epg3r found, with what's on now and what's next. You can filter
by league, search, and click any channel to see everything scheduled on it.

**Collections** let you create your own group of channels with their own M3U and XMLTV URLs.

**Leagues** lets you configure certain things per league, like the default event length and how early it should start.

**Sources** lets you manage your providers. You can have as many as you want.

**History** shows every time epg3r rebuilt your guide and what happened to each channel.

## Leagues it knows

NFL, MLB, NBA, NHL, WNBA, MLS, and NCAA Football, NCAA Men's Basketball, and NCAA Women's Basketball.

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
