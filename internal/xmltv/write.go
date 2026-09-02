package xmltv

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"io"

	"github.com/jonmaddox/epg3r/internal/model"
)

// Stamp is the XMLTV timestamp layout. Output is always UTC.
const Stamp = "20060102150405 -0700"

// Write renders a snapshot as Channels DVR friendly XMLTV: one <channel> per exported
// channel and, per programme, a
// title, sub-title, description, series-id, episode-num, date, icon, video quality,
// <new/>, <live/>, categories, and Gracenote team ids.
func Write(w io.Writer, snap *model.Snapshot, generator string) error {
	var buf bytes.Buffer
	buf.WriteString(xml.Header)
	buf.WriteString(`<!DOCTYPE tv SYSTEM "xmltv.dtd">` + "\n")

	doc := tvDoc{GeneratorName: generator}
	for _, ch := range snap.Channels {
		xc := xChannel{ID: ch.ID, DisplayName: []string{ch.Name}}
		if ch.LogoURL != "" {
			xc.Icon = &xIcon{Src: ch.LogoURL}
		}
		doc.Channels = append(doc.Channels, xc)
		for _, p := range ch.SortedProgrammes() {
			doc.Programmes = append(doc.Programmes, programme(ch, p))
		}
	}

	enc := xml.NewEncoder(&buf)
	enc.Indent("", "  ")
	if err := enc.Encode(doc); err != nil {
		return err
	}
	buf.WriteByte('\n')
	_, err := w.Write(buf.Bytes())
	return err
}

func programme(ch model.Channel, p model.Programme) xProgramme {
	ev := p.Event
	xp := xProgramme{
		Start:   ev.Start.UTC().Format(Stamp),
		Stop:    ev.Stop.UTC().Format(Stamp),
		Channel: ch.ID,
		Title:   xLang{Lang: "en", Text: ev.Title},
	}
	if p.Idle {
		xp.Desc = &xLang{Lang: "en", Text: "No event currently scheduled on this channel."}
		return xp
	}
	if ev.SubTitle != "" {
		xp.SubTitle = &xLang{Lang: "en", Text: ev.SubTitle}
	}
	desc := ev.Description
	if desc == "" {
		desc = fmt.Sprintf("%s presents %s", ev.Title, ev.SubTitle)
	}
	if p.Note != "" {
		desc += " (" + p.Note + ")"
	}
	xp.Desc = &xLang{Lang: "en", Text: desc}
	xp.SeriesID = &xSystem{System: "epg3r", Text: ev.SeriesID}
	xp.EpisodeNum = &xSystem{System: "epg3r", Text: ev.ID}
	xp.Date = ev.Kickoff.Format("2006-01-02")
	if ev.PlacardURL != "" {
		xp.Icon = &xIcon{Src: ev.PlacardURL}
	}
	xp.Video = &xVideo{Quality: "HDTV"}
	xp.New = &struct{}{}
	xp.Live = &struct{}{}
	seen := map[string]bool{}
	for _, c := range append([]string{ev.Genre}, ev.Categories...) {
		if c == "" || seen[c] {
			continue
		}
		seen[c] = true
		xp.Categories = append(xp.Categories, xLang{Lang: "en", Text: c})
	}
	for _, t := range ev.Teams {
		if t.TMSBrandID != "" {
			xp.TeamIDs = append(xp.TeamIDs, xSystem{System: "tms", Text: t.TMSBrandID})
		}
	}
	return xp
}

type tvDoc struct {
	XMLName       xml.Name     `xml:"tv"`
	GeneratorName string       `xml:"generator-info-name,attr"`
	Channels      []xChannel   `xml:"channel"`
	Programmes    []xProgramme `xml:"programme"`
}

type xChannel struct {
	ID          string   `xml:"id,attr"`
	DisplayName []string `xml:"display-name"`
	Icon        *xIcon   `xml:"icon,omitempty"`
}

type xIcon struct {
	Src string `xml:"src,attr"`
}

type xLang struct {
	Lang string `xml:"lang,attr,omitempty"`
	Text string `xml:",chardata"`
}

type xSystem struct {
	System string `xml:"system,attr"`
	Text   string `xml:",chardata"`
}

type xVideo struct {
	Quality string `xml:"quality"`
}

type xProgramme struct {
	Start      string    `xml:"start,attr"`
	Stop       string    `xml:"stop,attr"`
	Channel    string    `xml:"channel,attr"`
	Title      xLang     `xml:"title"`
	SubTitle   *xLang    `xml:"sub-title,omitempty"`
	Desc       *xLang    `xml:"desc,omitempty"`
	SeriesID   *xSystem  `xml:"series-id,omitempty"`
	EpisodeNum *xSystem  `xml:"episode-num,omitempty"`
	Date       string    `xml:"date,omitempty"`
	Icon       *xIcon    `xml:"icon,omitempty"`
	Video      *xVideo   `xml:"video,omitempty"`
	New        *struct{} `xml:"new,omitempty"`
	Live       *struct{} `xml:"live,omitempty"`
	Categories []xLang   `xml:"category"`
	TeamIDs    []xSystem `xml:"team-id"`
}
