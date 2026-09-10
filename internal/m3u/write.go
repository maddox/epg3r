package m3u

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/jonmaddox/epg3r/internal/model"
)

// WriteOptions tune the playlist output.
type WriteOptions struct {
	// GuideTags adds Channels DVR tvc-guide-* fallback tags describing the current or
	// next program, for setups that load the M3U without the XMLTV.
	GuideTags bool

	// BaseURL makes root-relative logo paths absolute. Empty leaves them as they are.
	BaseURL string
}

// Write renders a snapshot as an extended M3U that Channels DVR can import: every
// channel carries a stable channel-id, tvg-id, tvg-name, channel-number, and logo.
func Write(w io.Writer, snap *model.Snapshot, opts WriteOptions) error {
	bw := bufio.NewWriter(w)
	fmt.Fprintln(bw, "#EXTM3U")

	for _, ch := range snap.Channels {
		attrs := []string{
			attr("channel-id", ch.ID),
			attr("tvg-id", ch.ID),
			attr("tvg-name", ch.Name),
			attr("channel-number", fmt.Sprint(ch.Number)),
		}
		if ch.LogoURL != "" {
			attrs = append(attrs, attr("tvg-logo", model.Abs(opts.BaseURL, ch.LogoURL)))
		}
		attrs = append(attrs, attr("group-title", strings.ToUpper(ch.LeagueKey)))
		if opts.GuideTags && ch.GuideTitle != "" {
			attrs = append(attrs,
				attr("tvc-guide-title", ch.GuideTitle),
				attr("tvc-guide-description", ch.GuideText),
				attr("tvc-guide-art", model.Abs(opts.BaseURL, ch.GuideArt)),
				attr("tvc-guide-categories", guideCategory),
				attr("tvc-guide-tags", "HDTV, Live, New"),
			)
		}
		fmt.Fprintf(bw, "#EXTINF:-1 %s,%s\n%s\n", strings.Join(attrs, " "), ch.Name, ch.StreamURL)
	}
	return bw.Flush()
}

// guideCategory is the Channels DVR category for live games; the tvc-guide-categories
// tag accepts only Movie, Sports event, or Series.
const guideCategory = "Sports event"

var attrEscaper = strings.NewReplacer(`"`, "'", "\n", " ")

func attr(k, v string) string { return k + `="` + attrEscaper.Replace(v) + `"` }
