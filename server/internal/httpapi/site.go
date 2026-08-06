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
	Posts  []sitePost
	Post   *sitePost
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

// handleSite serves the Halogen devlog — the public face of this host. The
// static sections (hero, roadmap, stack) live in the template; the log entries
// come from the store and are written in the operator area.
func (s *Server) handleSite(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/":
		s.renderSite(w, "site-index", siteData{
			Styles: s.siteCSS,
			Title:  "Halogen — an open-city street racer on s&box",
			Posts:  s.sitePosts(false),
		})
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
		s.renderSite(w, "site-post", siteData{
			Styles: s.siteCSS,
			Title:  post.Title + " — Halogen devlog",
			Post:   &view,
		})
	default:
		s.siteNotFound(w)
	}
}

func (s *Server) siteNotFound(w http.ResponseWriter) {
	w.WriteHeader(http.StatusNotFound)
	s.renderSite(w, "site-404", siteData{Styles: s.siteCSS, Title: "Not found — Halogen"})
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
