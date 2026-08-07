package httpapi

import (
	"fmt"
	"log"
	"net/http"
	"strings"

	"github.com/nifigaprikolno/podkop/server/internal/store"
)

// adminNews renders the devlog CMS: the list of posts plus an editor for the
// one being edited (?edit=<id>, or a blank form).
func (s *Server) adminNews(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	p := s.newPageFor(r, screenNews)
	p.CSRF = s.csrfFor(r)
	p.Notice = notices[q.Get("notice")]
	p.Error = errNotices[q.Get("err")]
	p.Posts = s.postRows()

	if id := q.Get("edit"); id != "" {
		post, err := s.store.Post(id)
		if err == nil {
			p.EditPost = post
		}
	}
	if p.EditPost == nil {
		p.EditPost = &store.Post{Published: false}
	}

	s.render(w, "page", p)
}

func (s *Server) adminSavePost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		s.redirect(w, r, screenNews, "", "")
		return
	}

	title := strings.TrimSpace(r.FormValue("title"))
	if title == "" {
		s.redirect(w, r, screenNews, "", "title_required")
		return
	}

	post := &store.Post{}
	if id := r.FormValue("id"); id != "" {
		if existing, err := s.store.Post(id); err == nil {
			post = existing
		}
	}
	post.Title = title
	post.Slug = strings.TrimSpace(r.FormValue("slug"))
	post.Summary = strings.TrimSpace(r.FormValue("summary"))
	post.Body = r.FormValue("body")
	post.Tag = strings.TrimSpace(r.FormValue("tag"))
	post.Cover = strings.TrimSpace(r.FormValue("cover"))
	post.Published = r.FormValue("published") == "on"

	if err := s.store.PutPost(post); err != nil {
		log.Printf("save post: %v", err)
		s.redirect(w, r, screenNews, "", "save_failed")
		return
	}
	s.events.Info("NEWS", fmt.Sprintf("post %q saved (%s)", post.Title, boolLabel(post.Published, "live", "draft")))
	s.redirect(w, r, screenNews, "post_saved", "")
}

func (s *Server) adminDeletePost(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if post, err := s.store.Post(r.FormValue("id")); err == nil {
			s.events.Info("NEWS", fmt.Sprintf("post %q deleted", post.Title))
		}
		_ = s.store.DeletePost(r.FormValue("id"))
	}
	s.redirect(w, r, screenNews, "post_deleted", "")
}
