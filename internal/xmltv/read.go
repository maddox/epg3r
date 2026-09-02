// Package xmltv reads provider guides and writes the Channels DVR optimized guide.
package xmltv

import (
	"encoding/xml"
	"fmt"
	"io"
	"strings"
	"time"
)

// Channel is a <channel> from a provider guide.
type Channel struct {
	ID          string
	DisplayName string
	Icon        string
}

// Programme is a <programme> from a provider guide.
type Programme struct {
	Channel    string
	Start      time.Time
	Stop       time.Time
	Title      string
	SubTitle   string
	Desc       string
	Categories []string
	Icon       string
}

// Guide is a parsed provider XMLTV file.
type Guide struct {
	Channels   []Channel
	Programmes map[string][]Programme // by channel id, in file order
}

// Read parses an XMLTV document with a streaming decoder, so a multi-megabyte guide
// with thousands of programmes costs little memory beyond the result itself.
func Read(r io.Reader) (*Guide, error) {
	g := &Guide{Programmes: map[string][]Programme{}}
	dec := xml.NewDecoder(r)
	dec.Strict = false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read xmltv: %w", err)
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "channel":
			var c xmlChannel
			if err := dec.DecodeElement(&c, &se); err != nil {
				return nil, fmt.Errorf("read xmltv channel: %w", err)
			}
			g.Channels = append(g.Channels, Channel{ID: c.ID, DisplayName: displayName(c.DisplayNames), Icon: c.Icon.Src})
		case "programme":
			var p xmlProgramme
			if err := dec.DecodeElement(&p, &se); err != nil {
				return nil, fmt.Errorf("read xmltv programme: %w", err)
			}
			start, err := parseStamp(p.Start)
			if err != nil {
				return nil, fmt.Errorf("programme on %q: bad start %q", p.Channel, p.Start)
			}
			stop, err := parseStamp(p.Stop)
			if err != nil {
				return nil, fmt.Errorf("programme on %q: bad stop %q", p.Channel, p.Stop)
			}
			g.Programmes[p.Channel] = append(g.Programmes[p.Channel], Programme{
				Channel:    p.Channel,
				Start:      start,
				Stop:       stop,
				Title:      strings.TrimSpace(p.Title),
				SubTitle:   strings.TrimSpace(p.SubTitle),
				Desc:       strings.TrimSpace(p.Desc),
				Categories: p.Categories,
				Icon:       p.Icon.Src,
			})
		}
	}
	return g, nil
}

type xmlIcon struct {
	Src string `xml:"src,attr"`
}

type xmlChannel struct {
	ID           string   `xml:"id,attr"`
	DisplayNames []string `xml:"display-name"`
	Icon         xmlIcon  `xml:"icon"`
}

type xmlProgramme struct {
	Channel    string   `xml:"channel,attr"`
	Start      string   `xml:"start,attr"`
	Stop       string   `xml:"stop,attr"`
	Title      string   `xml:"title"`
	SubTitle   string   `xml:"sub-title"`
	Desc       string   `xml:"desc"`
	Categories []string `xml:"category"`
	Icon       xmlIcon  `xml:"icon"`
}

// XMLTV timestamps are "20060102150405 -0700"; the offset is optional and the
// seconds are sometimes dropped.
func parseStamp(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	for _, layout := range []string{"20060102150405 -0700", "20060102150405", "200601021504 -0700", "200601021504"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("unrecognised timestamp %q", s)
}

func displayName(names []string) string {
	for _, n := range names {
		if n = strings.TrimSpace(n); n != "" {
			return n
		}
	}
	return ""
}
