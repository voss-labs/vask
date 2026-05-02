// Command vask-web is the read-only public web mirror of the SSH forum
// (cmd/vask). Renders server-side HTML from the same store package as
// the SSH and operator binaries; designed to sit behind Cloudflare with
// aggressive edge caching plus an in-process LRU so the VM serves a
// tiny fraction of total traffic.
//
// Read-only by contract: no auth, no writes, no moderation surface, no
// per-user fields exposed. Anonymous reads pass myUserID=0.
//
// Usage:
//
//	TURSO_DATABASE_URL=libsql://... TURSO_AUTH_TOKEN=... \
//	  vask-web -addr 127.0.0.1:8080
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/voss-labs/vask/internal/store"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8080", "listen address")
	dbPath := flag.String("db", "ask.db", "local SQLite fallback when TURSO_DATABASE_URL is empty")
	flag.Parse()

	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	// VASK_BASE_URL is the canonical public origin (e.g. https://vask.vosslabs.org).
	// Drives canonical/og:url tags, sitemap loc entries, and robots Sitemap line.
	baseURL := strings.TrimRight(os.Getenv("VASK_BASE_URL"), "/")
	if baseURL == "" {
		baseURL = "http://" + *addr
		logger.Warn("VASK_BASE_URL unset — falling back to http dev URL; DO NOT ship to production like this", "fallback", baseURL)
	}
	// VASK_OG_IMAGE is an absolute URL to a 1200×630 PNG/JPG used for social
	// previews. Twitter/Slack/Discord won't render SVG so leave empty until you
	// have a raster file hosted (e.g. https://vosslabs.org/brand/social/og-default.png).
	ogImage := strings.TrimSpace(os.Getenv("VASK_OG_IMAGE"))

	target := os.Getenv("TURSO_DATABASE_URL")
	token := os.Getenv("TURSO_AUTH_TOKEN")
	if target == "" {
		target = *dbPath
	}

	st, err := store.Open(target, token)
	if err != nil {
		logger.Error("store open", "err", err)
		os.Exit(1)
	}
	defer st.Close()

	// Warm the libsql pipeline before accepting traffic. First-request TLS +
	// auth round-trip to a remote Turso region can be 1-2s; without this the
	// initial browser hit often races the browser's patience and gets canceled.
	warmCtx, warmCancel := context.WithTimeout(context.Background(), 10*time.Second)
	warmStart := time.Now()
	if _, err := st.ListPopularTags(warmCtx, 1); err != nil {
		logger.Warn("warmup query failed", "err", err)
	} else {
		logger.Info("warmup ok", "took", time.Since(warmStart).Round(time.Millisecond))
	}
	warmCancel()

	srv := newServer(st, logger, baseURL, ogImage)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", srv.handleFeed(store.SortHot, "hot"))
	mux.HandleFunc("GET /new", srv.handleFeed(store.SortNew, "new"))
	mux.HandleFunc("GET /top", srv.handleFeed(store.SortTop, "top"))
	mux.HandleFunc("GET /tag/{tag}", srv.handleTag)
	mux.HandleFunc("GET /p/{id}", srv.handlePost)
	mux.HandleFunc("GET /sitemap.xml", srv.handleSitemap)
	mux.HandleFunc("GET /healthz", srv.handleHealth)
	mux.HandleFunc("GET /robots.txt", srv.handleRobots)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           secureHeaders(mux),
		ReadHeaderTimeout: 5 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(shutCtx)
	}()

	// Keep the libsql TLS conn warm. http.Transport drops idle conns after
	// ~90s; on a low-traffic forum the next request would otherwise pay the
	// cold round-trip again.
	go func() {
		t := time.NewTicker(60 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				kctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				if _, err := st.ListPopularTags(kctx, 1); err != nil {
					logger.Warn("keepalive failed", "err", err)
				}
				cancel()
			}
		}
	}()

	logger.Info("vask-web listening", "addr", *addr, "db", redactURL(target))
	if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logger.Error("listen", "err", err)
		os.Exit(1)
	}
}

// server ==============================================================

type server struct {
	store   *store.Store
	log     *slog.Logger
	feedTpl *template.Template
	postTpl *template.Template
	cache   *lru
	baseURL string
	ogImage string
}

func newServer(st *store.Store, log *slog.Logger, baseURL, ogImage string) *server {
	feedT := template.Must(template.New("base").Funcs(funcs).Parse(baseTpl))
	feedT = template.Must(feedT.Parse(feedTpl))
	postT := template.Must(template.New("base").Funcs(funcs).Parse(baseTpl))
	postT = template.Must(postT.Parse(postTpl))
	return &server{
		store: st, log: log,
		feedTpl: feedT, postTpl: postT,
		cache:   newLRU(500),
		baseURL: baseURL, ogImage: ogImage,
	}
}

// handlers ============================================================

func (s *server) handleFeed(sort store.SortMode, label string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		if offset < 0 {
			offset = 0
		}
		key := fmt.Sprintf("feed:%s:%d", label, offset)
		if s.serveCached(w, key, 30*time.Second) {
			return
		}
		posts, err := s.store.ListPosts(r.Context(), 0, store.ListPostsParams{
			Sort: sort, Limit: 20, Offset: offset,
		})
		if err != nil {
			s.fail(w, "list posts", err)
			return
		}
		w.Header().Set("Cache-Control", "public, max-age=30, s-maxage=60, stale-while-revalidate=600")
		s.render(w, key, s.feedTpl, feedView{
			pageMeta: s.feedMeta(label, ""),
			Posts:    posts,
			Offset:   offset,
		}, 30*time.Second)
	}
}

func (s *server) handleTag(w http.ResponseWriter, r *http.Request) {
	tag := strings.ToLower(strings.TrimSpace(r.PathValue("tag")))
	if tag == "" || len(tag) > 64 {
		http.NotFound(w, r)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 {
		offset = 0
	}
	key := fmt.Sprintf("tag:%s:%d", tag, offset)
	if s.serveCached(w, key, 60*time.Second) {
		return
	}
	posts, err := s.store.ListPosts(r.Context(), 0, store.ListPostsParams{
		Tag: tag, Sort: store.SortNew, Limit: 20, Offset: offset,
	})
	if err != nil {
		s.fail(w, "list posts by tag", err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=120, stale-while-revalidate=600")
	s.render(w, key, s.feedTpl, feedView{
		pageMeta: s.feedMeta("tag", tag),
		Tag:      tag,
		Posts:    posts,
		Offset:   offset,
	}, 60*time.Second)
}

func (s *server) handlePost(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		http.NotFound(w, r)
		return
	}
	key := fmt.Sprintf("post:%d", id)
	if s.serveCached(w, key, 60*time.Second) {
		return
	}
	post, err := s.store.GetPost(r.Context(), id, 0)
	if err != nil {
		s.fail(w, "get post", err)
		return
	}
	if post == nil {
		http.NotFound(w, r)
		return
	}
	comments, err := s.store.ListComments(r.Context(), id, 0)
	if err != nil {
		s.fail(w, "list comments", err)
		return
	}
	w.Header().Set("Cache-Control", "public, max-age=60, s-maxage=120, stale-while-revalidate=600")
	s.render(w, key, s.postTpl, postView{
		pageMeta: s.postMeta(*post),
		Post:     *post,
		Threads:  threadComments(comments),
	}, 60*time.Second)
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if _, err := s.store.ListPopularTags(ctx, 1); err != nil {
		http.Error(w, "db unhealthy", http.StatusServiceUnavailable)
		return
	}
	fmt.Fprintln(w, "ok")
}

func (s *server) handleRobots(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "public, max-age=86400")
	fmt.Fprintf(w, "User-agent: *\nAllow: /\nDisallow: /healthz\nSitemap: %s/sitemap.xml\n", s.baseURL)
}

func (s *server) handleSitemap(w http.ResponseWriter, r *http.Request) {
	const key = "sitemap"
	if body, ok := s.cache.get(key); ok {
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		w.Header().Set("Cache-Control", "public, max-age=300")
		_, _ = w.Write(body)
		return
	}
	posts, err := s.store.ListPosts(r.Context(), 0, store.ListPostsParams{
		Sort: store.SortNew, Limit: 500,
	})
	if err != nil {
		s.fail(w, "sitemap list posts", err)
		return
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">` + "\n")
	for _, p := range []string{"/", "/new", "/top"} {
		fmt.Fprintf(&b, "  <url><loc>%s%s</loc><changefreq>hourly</changefreq><priority>0.8</priority></url>\n", s.baseURL, p)
	}
	for _, p := range posts {
		fmt.Fprintf(&b,
			"  <url><loc>%s/p/%d</loc><lastmod>%s</lastmod></url>\n",
			s.baseURL, p.ID, p.CreatedAt.UTC().Format("2006-01-02"))
	}
	b.WriteString(`</urlset>` + "\n")
	body := []byte(b.String())
	s.cache.set(key, body, 5*time.Minute)
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Cache-Control", "public, max-age=300")
	_, _ = w.Write(body)
}

// secureHeaders sets baseline security response headers on every reply.
// HSTS is intentionally NOT set here — the TLS edge (Caddy) is the right
// place for it, and setting HSTS over plain HTTP in dev would error.
func secureHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hdr := w.Header()
		hdr.Set("X-Content-Type-Options", "nosniff")
		hdr.Set("Referrer-Policy", "no-referrer")
		hdr.Set("Permissions-Policy", "interest-cohort=(), browsing-topics=()")
		hdr.Set("X-Frame-Options", "DENY")
		hdr.Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'none'; "+
				"style-src 'self' 'unsafe-inline'; "+
				"img-src 'self' data: https:; "+
				"font-src 'self' data:; "+
				"manifest-src 'self' https://vosslabs.org; "+
				"connect-src 'self'; "+
				"form-action 'none'; "+
				"frame-ancestors 'none'; "+
				"base-uri 'self'")
		h.ServeHTTP(w, r)
	})
}

// rendering + LRU =====================================================

func (s *server) render(w http.ResponseWriter, key string, t *template.Template, data any, ttl time.Duration) {
	var buf strings.Builder
	if err := t.Execute(&buf, data); err != nil {
		s.fail(w, "render", err)
		return
	}
	body := []byte(buf.String())
	s.cache.set(key, body, ttl)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(body)
}

func (s *server) serveCached(w http.ResponseWriter, key string, ttl time.Duration) bool {
	body, ok := s.cache.get(key)
	if !ok {
		return false
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", fmt.Sprintf("public, max-age=%d, stale-while-revalidate=600", int(ttl.Seconds())))
	_, _ = w.Write(body)
	return true
}

func (s *server) fail(w http.ResponseWriter, what string, err error) {
	s.log.Error(what, "err", err)
	http.Error(w, "internal error", http.StatusInternalServerError)
}

// view models =========================================================

// pageMeta is shared <head>/canonical/social-preview metadata embedded in
// every concrete view. Lives at the top level of the template data so the
// base layout can reach it directly via {{.Title}}, {{.Description}}, etc.
type pageMeta struct {
	Title       string // human-readable page title (Hot, post title, #tag)
	Sort        string // hot | new | top | tag | post — drives nav active state
	Description string // <meta description> + og:description + twitter:description
	Path        string // canonical URL path (e.g. /p/123) — no query string
	BaseURL     string // canonical origin (e.g. https://vask.vosslabs.org)
	OGType      string // website | article
	OGImage     string // absolute URL to a 1200×630 PNG/JPG; empty omits the tag
}

type feedView struct {
	pageMeta
	Tag    string
	Posts  []store.Post
	Offset int
}

type postView struct {
	pageMeta
	Post    store.Post
	Threads []*commentNode
}

type commentNode struct {
	C        store.Comment
	Children []*commentNode
}

// meta builders =======================================================

func (s *server) feedMeta(label, tag string) pageMeta {
	var title, desc, path string
	switch label {
	case "tag":
		title = "#" + tag
		desc = fmt.Sprintf("Posts tagged #%s on vask — read-only web mirror of the Vidyalankar Open Source Software Labs SSH forum.", tag)
		path = "/tag/" + tag
	case "new":
		title = "New"
		desc = "Latest discussions on vask — read-only web mirror of the Vidyalankar Open Source Software Labs SSH forum. Reply via ssh vask.vosslabs.org."
		path = "/new"
	case "top":
		title = "Top"
		desc = "Top-voted discussions on vask — read-only web mirror of the Vidyalankar Open Source Software Labs SSH forum. Reply via ssh vask.vosslabs.org."
		path = "/top"
	default: // "hot"
		title = "Hot"
		desc = "Trending discussions on vask — read-only web mirror of the Vidyalankar Open Source Software Labs SSH forum. Reply via ssh vask.vosslabs.org."
		path = "/"
	}
	return pageMeta{
		Title: title, Sort: label, Description: desc, Path: path,
		BaseURL: s.baseURL, OGType: "website", OGImage: s.ogImage,
	}
}

func (s *server) postMeta(p store.Post) pageMeta {
	return pageMeta{
		Title:       p.Title,
		Sort:        "post",
		Description: postDescription(p),
		Path:        fmt.Sprintf("/p/%d", p.ID),
		BaseURL:     s.baseURL,
		OGType:      "article",
		OGImage:     s.ogImage,
	}
}

func postDescription(p store.Post) string {
	body := strings.TrimSpace(p.Body)
	if body == "" {
		return p.Title
	}
	body = strings.Join(strings.Fields(body), " ") // collapse all whitespace runs
	rs := []rune(body)
	if len(rs) > 200 {
		return string(rs[:200]) + "…"
	}
	return body
}

func threadComments(cs []store.Comment) []*commentNode {
	byID := make(map[int64]*commentNode, len(cs))
	for i := range cs {
		byID[cs[i].ID] = &commentNode{C: cs[i]}
	}
	var roots []*commentNode
	for _, c := range cs {
		node := byID[c.ID]
		if c.ParentCommentID != nil {
			if parent, ok := byID[*c.ParentCommentID]; ok {
				parent.Children = append(parent.Children, node)
				continue
			}
		}
		roots = append(roots, node)
	}
	return roots
}

// helpers =============================================================

func redactURL(u string) string {
	if base, _, ok := strings.Cut(u, "?"); ok {
		return base + "?…"
	}
	return u
}
