# epg3r!

This downloads and updates XMLTV data for sports.

### CLI

```bash
docker run \
--name nam3r \
-e M3U_URL="http://site.com/iptv.m3u"
-e AWS_ACCESS_KEY_ID=XXXXXXXXXXXX \
-e AWS_SECRET_ACCESS_KEY=XXXXXXXXXXXX \
epg3r
```

### Docker Compose

```yaml
version: "3.1"
services:
  nam3r:
    image: epg3r
    container_name: epg3r
    environment:
      - M3U_URL=http://site.com/iptv.m3u
      - AWS_ACCESS_KEY_ID=XXXXXXXXXXXX
      - AWS_SECRET_ACCESS_KEY=XXXXXXXXXXXX
```
