package art

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/jonmaddox/epg3r/internal/catalog"
)

// defaultSource is the only place the app names where crests come from. The catalog says
// which segment of it a league lives in and which id a team is, and nothing else knows.
const defaultSource = "https://a.espncdn.com"

// ErrNotFound means the URL names no league or team we have. Our own URLs are built from
// the catalog, so one of these is a bug rather than a picture we could not draw.
var ErrNotFound = errors.New("art: no such league or team")

// Image is a rendered picture and how to revalidate it.
type Image struct {
	PNG  []byte
	ETag string // quoted, ready for the header

	// Fallback means a crest was wanted and could not be had this time, so the picture is
	// lettered instead. Those are not kept: the next request should try again.
	Fallback bool
}

type Options struct {
	Catalog  *catalog.Catalog // required
	CacheDir string           // required; {DataDir}/cache/art
	Source   string           // where crests come from; empty means defaultSource
	Client   *http.Client
	Log      *slog.Logger
	Now      func() time.Time

	// Dev keeps drawn pictures out of the cache on disk. What the compositor draws changes
	// with every edit, and a cached picture has no way to know that: only Version says the
	// drawing has moved on, and remembering to bump it between edits is not a plan.
	Dev bool

	MaxFetches  int // concurrent fetches; 0 means 6
	PositiveTTL time.Duration
	NegativeTTL time.Duration
}

// Service draws and serves the art. It holds two caches: composited pictures, in memory and
// on disk, and the crests they are drawn from.
type Service struct {
	cat   *catalog.Catalog
	marks *marks
	dir   string
	dev   bool
	nonce string // dev only; see New
	log   *slog.Logger

	flight singleflight.Group
	mu     sync.Mutex
	cache  map[string]Image
}

// keptInMemory is how many composited pictures to hold. This is what absorbs a guide load,
// where hundreds of idle channels ask for the same league placard within a second. Overflow
// drops the lot rather than tracking use: the disk cache is right behind it.
const keptInMemory = 256

func New(opts Options) (*Service, error) {
	if opts.Catalog == nil || opts.CacheDir == "" {
		return nil, errors.New("art: a catalog and a cache directory are required")
	}
	if _, err := parsedFont(); err != nil {
		return nil, fmt.Errorf("art: %w", err)
	}
	log := opts.Log
	if log == nil {
		log = slog.Default()
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	fetches := cmpOr(opts.MaxFetches, 6)
	client := opts.Client
	if client == nil {
		client = &http.Client{
			Timeout: 10 * time.Second,
			Transport: &http.Transport{
				MaxConnsPerHost:     fetches,
				MaxIdleConnsPerHost: fetches,
				IdleConnTimeout:     90 * time.Second,
			},
		}
	}
	dropOldVersions(filepath.Join(opts.CacheDir, "render"))
	// In dev, every picture is new on every start. What the compositor draws changes with
	// every edit, and nothing else in the signature moves when it does — so a browser or a
	// media server revalidates, is told 304, and goes on showing a drawing we have replaced.
	// Only Version says the drawing has moved on, and bumping it between edits is not a
	// plan; it is for shipping a change to art someone already has.
	nonce := ""
	if opts.Dev {
		nonce = strconv.FormatInt(now().UnixNano(), 36)
	}
	return &Service{
		nonce: nonce,
		cat:   opts.Catalog,
		dir:   opts.CacheDir,
		dev:   opts.Dev,
		log:   log,
		marks: &marks{
			base:   cmpOr(opts.Source, defaultSource),
			client: client,
			dir:    filepath.Join(opts.CacheDir, "source"),
			log:    log,
			now:    now,
			posTTL: cmpOr(opts.PositiveTTL, 30*24*time.Hour),
			negTTL: cmpOr(opts.NegativeTTL, 7*24*time.Hour),
			sem:    make(chan struct{}, fetches),
		},
	}, nil
}

// dropOldVersions removes renders drawn by a compositor we have replaced. Bumping Version
// rotates every URL, so those files would otherwise sit there forever, answering nothing.
func dropOldVersions(root string) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.Name() != Version {
			_ = os.RemoveAll(filepath.Join(root, e.Name()))
		}
	}
}

// encode turns a drawn picture into the bytes to serve, in the shape build returns.
func encode(img *image.RGBA, err error) ([]byte, []string, bool, error) {
	if err != nil {
		return nil, nil, false, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, nil, false, err
	}
	return buf.Bytes(), nil, false, nil
}

func cmpOr[T comparable](v, fallback T) T {
	var zero T
	if v == zero {
		return fallback
	}
	return v
}

// LeaguePlacard is the art for an airing whose teams did not both resolve. It is 4:3, the shape a
// guide gives a program.
func (s *Service) LeaguePlacard(ctx context.Context, leagueKey string) (Image, error) {
	lg, ok := s.league(leagueKey)
	if !ok {
		return Image{}, ErrNotFound
	}
	return s.render(ctx, "placard/"+leagueKey, func(ctx context.Context) ([]byte, []string, bool, error) {
		mark, source := s.leagueMark(ctx, lg)
		body, _, _, err := encode(composeLeague(artOf(lg, mark), subject{Mark: mark}))
		return body, source, false, err
	})
}

// LeagueLogo is the logo for a channel that is not any one team, which is every slot channel
// in a league. There is no crest to fetch for a league, so this is always drawn.
func (s *Service) LeagueLogo(ctx context.Context, leagueKey string) (Image, error) {
	lg, ok := s.league(leagueKey)
	if !ok {
		return Image{}, ErrNotFound
	}
	return s.render(ctx, "logo/"+leagueKey, func(ctx context.Context) ([]byte, []string, bool, error) {
		return s.logo(ctx, lg, s.leagueMarkURL(lg), upper(cmpOr(lg.LabelPrefix, lg.Key)))
	})
}

// TeamLogo is the logo for a channel dedicated to one team: the crest itself, served as it
// arrived. A logo sits small beside a channel name, against a background that belongs to
// whoever is showing it, so there is nothing here to compose — only a roundel to fall back
// to when the team has no crest.
func (s *Service) TeamLogo(ctx context.Context, leagueKey, teamKey string) (Image, error) {
	lg, team, ok := s.team(leagueKey, teamKey)
	if !ok {
		return Image{}, ErrNotFound
	}
	return s.render(ctx, "logo/"+leagueKey+"/"+teamKey, func(ctx context.Context) ([]byte, []string, bool, error) {
		return s.logo(ctx, lg, s.teamMarkURL(lg, team), letters(team.Abbr, team.Name))
	})
}

// MatchupPlacard is one game. away and home are in the order the airing reads; home is last.
func (s *Service) MatchupPlacard(ctx context.Context, leagueKey, awayKey, homeKey string) (Image, error) {
	lg, away, ok := s.team(leagueKey, awayKey)
	if !ok {
		return Image{}, ErrNotFound
	}
	_, home, ok := s.team(leagueKey, homeKey)
	if !ok {
		return Image{}, ErrNotFound
	}
	return s.render(ctx, "placard/"+leagueKey+"/"+awayKey+"/"+homeKey,
		func(ctx context.Context) ([]byte, []string, bool, error) {
			mark, source := s.leagueMark(ctx, lg)
			a, aEtag, aMissing := s.subject(ctx, lg, away)
			h, hEtag, hMissing := s.subject(ctx, lg, home)
			png, _, _, err := encode(composeMatchup(artOf(lg, mark), a, h, "at"))
			return png, append(source, aEtag, hEtag), aMissing || hMissing, err
		})
}

// logo answers a channel logo: the mark itself when there is one to fetch, and a roundel
// when there is not. url is empty when this thing has no mark at all — the college leagues
// have none, and most college teams have none — in which case the roundel is not a fallback
// but the answer, and worth keeping.
func (s *Service) logo(ctx context.Context, lg *catalog.League, url, marks string) ([]byte, []string, bool, error) {
	if url != "" {
		if got, ok := s.marks.get(ctx, url); ok {
			return got.body, []string{got.etag}, false, nil
		}
	}
	body, _, _, err := encode(composeLogo(artOf(lg, nil), marks))
	return body, nil, url != "", err // wanted a mark and missed it: come back for it soon
}

// subject gathers what to draw for one team. missing means a crest was wanted and could not
// be had right now, which is the one case whose picture is not worth keeping.
func (s *Service) subject(ctx context.Context, lg *catalog.League, t *catalog.Team) (sub subject, etag string, missing bool) {
	sub = subject{Letters: letters(t.Abbr, t.Name)}
	if t.LogoID == "" || lg.LogoPath == "" {
		return sub, "", false // nothing to fetch; the roundel is this team's picture
	}
	got, ok := s.marks.get(ctx, s.teamMarkURL(lg, t))
	if !ok {
		return sub, "", true
	}
	sub.Mark = got.img
	return sub, got.etag, false
}

// The two URLs the marks come from. This, and the default source above, is everything the
// app knows about how that source is laid out.
func (s *Service) teamMarkURL(lg *catalog.League, t *catalog.Team) string {
	if t.LogoID == "" || lg.LogoPath == "" {
		return ""
	}
	return s.marks.base + "/i/teamlogos/" + lg.LogoPath + "/500/" + t.LogoID + ".png"
}

func (s *Service) leagueMarkURL(lg *catalog.League) string {
	switch {
	case lg.LogoID == "":
		return ""
	case strings.Contains(lg.LogoID, "://"):
		return lg.LogoID // this league's mark is not kept where its teams' are
	}
	return s.marks.base + "/i/teamlogos/leagues/500/" + lg.LogoID + ".png"
}

func (s *Service) league(key string) (*catalog.League, bool) {
	if !safeKey(key) {
		return nil, false
	}
	return s.cat.League(key)
}

func (s *Service) team(leagueKey, teamKey string) (*catalog.League, *catalog.Team, bool) {
	lg, ok := s.league(leagueKey)
	if !ok || !safeKey(teamKey) {
		return nil, nil, false
	}
	for _, womens := range []bool{false, true} {
		if t, found := s.cat.Teams(lg, womens).ByKey(teamKey); found {
			return lg, t, true
		}
	}
	return nil, nil, false
}

// leagueMark fetches a league's own mark, which both kinds of placard wear. A league that
// has none is drawn from its name instead, so a miss here is not worth reporting.
func (s *Service) leagueMark(ctx context.Context, lg *catalog.League) (image.Image, []string) {
	url := s.leagueMarkURL(lg)
	if url == "" {
		return nil, nil
	}
	got, ok := s.marks.get(ctx, url)
	if !ok {
		return nil, nil
	}
	return got.img, []string{got.etag}
}

func artOf(lg *catalog.League, mark image.Image) leagueArt {
	r, g, b, _ := lg.RGB()
	return leagueArt{Name: lg.Name, Sport: lg.Sport, Color: color.RGBA{r, g, b, 255}, Mark: mark}
}

// render answers one picture, from memory, then from disk, then by drawing it. Identical
// concurrent requests collapse: a guide load asks for the same league placard hundreds of
// times at once.
func (s *Service) render(ctx context.Context, name string, build func(context.Context) ([]byte, []string, bool, error)) (Image, error) {
	if img, ok := s.remembered(name); ok {
		return img, nil
	}
	v, err, _ := s.flight.Do(name, func() (any, error) {
		if img, ok := s.remembered(name); ok {
			return img, nil
		}
		if img, ok := s.onDisk(name); ok {
			s.remember(name, img)
			return img, nil
		}
		body, etags, fallback, err := build(ctx)
		if err != nil {
			return nil, err
		}
		img := Image{PNG: body, ETag: s.etag(name, etags), Fallback: fallback}
		s.remember(name, img)
		if !fallback {
			s.keep(name, img)
		}
		return img, nil
	})
	if err != nil {
		return Image{}, err
	}
	return v.(Image), nil
}

// etag names a picture by what it was drawn from rather than by its bytes, so revalidating
// one never has to touch it. The crests' own validators are folded in: without them a
// redrawn crest would keep the same name and consumers would hold the old picture for good.
func (s *Service) etag(name string, sources []string) string {
	sum := sha256.Sum256([]byte(name + "\x00" + s.nonce + "\x00" + strings.Join(sources, "\x00")))
	kind, _, _ := strings.Cut(name, "/")
	return `"` + Version + "-" + kind + "-" + hex.EncodeToString(sum[:8]) + `"`
}

func (s *Service) remembered(name string) (Image, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	img, ok := s.cache[name]
	return img, ok
}

func (s *Service) remember(name string, img Image) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache == nil {
		s.cache = map[string]Image{}
	}
	if len(s.cache) >= keptInMemory {
		clear(s.cache)
	}
	s.cache[name] = img
}

// The rendered cache is laid out to be read by a person: listing it says what the guide is
// showing. Every segment is a catalog key, which safeKey has already vetted.
func (s *Service) renderPath(name string) string {
	return filepath.Join(s.dir, "render", Version, filepath.FromSlash(name)+".png")
}

func (s *Service) onDisk(name string) (Image, bool) {
	if s.dev {
		return Image{}, false
	}
	path := s.renderPath(name)
	body, err := os.ReadFile(path)
	if err != nil {
		return Image{}, false
	}
	raw, err := os.ReadFile(path + ".meta")
	if err != nil {
		return Image{}, false
	}
	var md struct {
		ETag string `json:"etag"`
	}
	if json.Unmarshal(raw, &md) != nil || md.ETag == "" {
		return Image{}, false
	}
	return Image{PNG: body, ETag: md.ETag}, true
}

func (s *Service) keep(name string, img Image) {
	if s.dev {
		return
	}
	path := s.renderPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		s.marks.cannotWrite(err)
		return
	}
	if err := writeFile(path, img.PNG); err != nil {
		s.marks.cannotWrite(err)
		return
	}
	raw, err := json.Marshal(struct {
		ETag string `json:"etag"`
	}{img.ETag})
	if err != nil {
		return
	}
	if err := writeFile(path+".meta", raw); err != nil {
		s.marks.cannotWrite(err)
	}
}
