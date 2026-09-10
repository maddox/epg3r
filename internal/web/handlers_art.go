package web

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/jonmaddox/epg3r/internal/art"
)

// The art routes. A ServeMux segment is either a literal or a whole wildcard, so the
// extension cannot be part of the pattern; the last segment carries it and pngName takes it
// off. Requiring it keeps the URLs stable and gives a consumer a filename it will treat as
// a picture.
func (s *Server) handleLeaguePlacard(w http.ResponseWriter, r *http.Request) {
	img, err := s.Art.LeaguePlacard(r.Context(), pngName(r.PathValue("league")))
	s.serveArt(w, r, img, err)
}

func (s *Server) handleLeagueLogo(w http.ResponseWriter, r *http.Request) {
	img, err := s.Art.LeagueLogo(r.Context(), pngName(r.PathValue("league")))
	s.serveArt(w, r, img, err)
}

func (s *Server) handleTeamLogo(w http.ResponseWriter, r *http.Request) {
	img, err := s.Art.TeamLogo(r.Context(), r.PathValue("league"), pngName(r.PathValue("team")))
	s.serveArt(w, r, img, err)
}

func (s *Server) handleMatchupPlacard(w http.ResponseWriter, r *http.Request) {
	img, err := s.Art.MatchupPlacard(r.Context(), r.PathValue("league"), r.PathValue("away"), pngName(r.PathValue("home")))
	s.serveArt(w, r, img, err)
}

// pngName reads a segment written as a filename. Anything without the extension is not one
// of our URLs and names nothing.
func pngName(segment string) string {
	name, ok := strings.CutSuffix(segment, ".png")
	if !ok {
		return ""
	}
	return name
}

// serveArt answers one picture. Each URL is a deterministic name for one, so it revalidates
// against a tag the art package derives from what it was drawn from, without touching the
// bytes.
func (s *Server) serveArt(w http.ResponseWriter, r *http.Request, img art.Image, err error) {
	switch {
	case errors.Is(err, art.ErrNotFound):
		http.NotFound(w, r)
		return
	case err != nil:
		s.Log.Error("art", "path", r.URL.Path, "err", err, "req", RequestID(r.Context()))
		http.Error(w, "could not draw this picture", http.StatusInternalServerError)
		return
	}
	w.Header().Set("ETag", img.ETag)
	switch {
	case img.Fallback:
		// A crest was wanted and could not be had. Come back for it soon rather than
		// leaving a lettered stand-in in front of people for a day.
		w.Header().Set("Cache-Control", "public, max-age=300")
	case s.tpl.dev:
		w.Header().Set("Cache-Control", "no-cache")
	default:
		w.Header().Set("Cache-Control", "public, max-age=86400")
	}
	if r.Header.Get("If-None-Match") == img.ETag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Content-Length", fmt.Sprint(len(img.PNG)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(img.PNG)
}
