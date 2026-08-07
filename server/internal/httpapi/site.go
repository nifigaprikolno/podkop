package httpapi

import (
	"errors"
	"html/template"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/nifigaprikolno/podkop/server/internal/markdown"
	"github.com/nifigaprikolno/podkop/server/internal/store"
)

// siteData is what the public devlog templates receive.
type siteData struct {
	Styles template.CSS
	Title  string
	// Name and Tagline brand the site; they come from configuration so the
	// content matches the domain it is served from.
	Name    string
	Tagline string
	Posts   []sitePost
	Post    *sitePost
}

type sitePost struct {
	Title   string
	Slug    string
	Summary string
	Cover   string
	Tag     string
	Date    string
	ISODate string
	Body    template.HTML
	URL     string
}

// handleSite serves the devlog — the public face of this host. The static
// sections (hero, roadmap, stack) live in the template; the log entries come
// from the store and are written in the operator area.
func (s *Server) handleSite(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		data := s.newSiteData(s.cfg.SiteName + " — " + s.cfg.SiteTagline)
		data.Posts = s.sitePosts(false)
		s.renderSite(w, "site-index", data)
	case strings.HasPrefix(r.URL.Path, "/post/"):
		slug := strings.TrimPrefix(r.URL.Path, "/post/")
		post, err := s.store.PostBySlug(strings.Trim(slug, "/"))
		if err != nil || !post.Published {
			if err != nil && !errors.Is(err, store.ErrNotFound) {
				log.Printf("load post: %v", err)
			}
			s.siteNotFound(w)
			return
		}
		view := s.sitePost(post)
		data := s.newSiteData(post.Title + " — " + s.cfg.SiteName + " devlog")
		data.Post = &view
		s.renderSite(w, "site-post", data)
	default:
		s.siteNotFound(w)
	}
}

func (s *Server) newSiteData(title string) siteData {
	return siteData{
		Styles:  s.siteCSS,
		Title:   title,
		Name:    s.cfg.SiteName,
		Tagline: s.cfg.SiteTagline,
	}
}

func (s *Server) siteNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	s.renderSite(w, "site-404", s.newSiteData("Not found — "+s.cfg.SiteName))
}

func (s *Server) sitePosts(includeDrafts bool) []sitePost {
	posts := s.store.Posts(!includeDrafts)
	out := make([]sitePost, 0, len(posts))
	for _, p := range posts {
		out = append(out, s.sitePost(p))
	}
	return out
}

func (s *Server) sitePost(p *store.Post) sitePost {
	created := p.CreatedAt
	if created.IsZero() {
		created = time.Now()
	}
	return sitePost{
		Title:   p.Title,
		Slug:    p.Slug,
		Summary: p.Summary,
		Cover:   p.Cover,
		Tag:     p.Tag,
		Date:    created.Local().Format("02 Jan 2006"),
		ISODate: created.Format("2006-01-02"),
		Body:    template.HTML(markdown.Render(p.Body)),
		URL:     "/post/" + p.Slug,
	}
}

func (s *Server) renderSite(w http.ResponseWriter, name string, data siteData) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.siteTmpl.ExecuteTemplate(w, name, data); err != nil {
		log.Printf("site render %s: %v", name, err)
	}
}
