// Package m3u reads and writes extended M3U playlists.
package m3u

import (
	"bufio"
	"fmt"
	"io"
	"strings"
)

// Entry is one channel in a playlist.
type Entry struct {
	Line  int               // line number of the #EXTINF directive
	Attrs map[string]string // key="value" attributes from #EXTINF
	Title string            // text after the last comma on the #EXTINF line
	URL   string
}

// Attr returns an attribute or "".
func (e Entry) Attr(key string) string { return e.Attrs[key] }

// Group returns group-title, falling back to a preceding #EXTGRP directive.
func (e Entry) Group() string { return e.Attrs["group-title"] }

// Parse reads a playlist. It tolerates CRLF, a UTF-8 BOM, blank lines, and unknown
// directives. #EXTGRP is honored as a group when group-title is absent.
func Parse(r io.Reader) ([]Entry, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	var (
		out     []Entry
		pending *Entry
		grp     string
		lineNo  int
	)
	for sc.Scan() {
		lineNo++
		line := strings.TrimRight(sc.Text(), "\r")
		if lineNo == 1 {
			line = strings.TrimPrefix(line, "\uFEFF")
		}
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "#EXTINF:"):
			e := parseExtinf(line)
			e.Line = lineNo
			if _, ok := e.Attrs["group-title"]; !ok && grp != "" {
				e.Attrs["group-title"] = grp
			}
			pending = &e
		case strings.HasPrefix(line, "#EXTGRP:"):
			grp = strings.TrimSpace(strings.TrimPrefix(line, "#EXTGRP:"))
		case strings.HasPrefix(line, "#"):
			continue // #EXTM3U, #EXTVLCOPT, comments
		default:
			if pending == nil {
				continue // a URL with no #EXTINF; not a channel we can name
			}
			pending.URL = line
			out = append(out, *pending)
			pending = nil
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read m3u: %w", err)
	}
	return out, nil
}

// parseExtinf splits `#EXTINF:-1 key="v" key2="v",Title`. Attribute values may
// contain commas, so the title is whatever follows the closing quote of the last
// attribute (or the first comma when there are no attributes).
func parseExtinf(line string) Entry {
	e := Entry{Attrs: map[string]string{}}
	rest := strings.TrimPrefix(line, "#EXTINF:")

	// Skip the duration token.
	if i := strings.IndexAny(rest, " ,"); i >= 0 {
		rest = rest[i:]
	} else {
		return e
	}

	// Consume key="value" pairs.
	for {
		rest = strings.TrimLeft(rest, " ")
		eq := strings.Index(rest, "=\"")
		if eq <= 0 || strings.ContainsAny(rest[:eq], " ,") {
			break
		}
		key := rest[:eq]
		end := strings.Index(rest[eq+2:], "\"")
		if end < 0 {
			break
		}
		e.Attrs[key] = rest[eq+2 : eq+2+end]
		rest = rest[eq+2+end+1:]
	}

	rest = strings.TrimLeft(rest, " ")
	if strings.HasPrefix(rest, ",") {
		rest = rest[1:]
	}
	e.Title = strings.TrimSpace(rest)
	return e
}
