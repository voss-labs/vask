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
	"sync"
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

	srv := newServer(st, logger)
	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", srv.handleFeed(store.SortHot, "hot"))
	mux.HandleFunc("GET /new", srv.handleFeed(store.SortNew, "new"))
	mux.HandleFunc("GET /top", srv.handleFeed(store.SortTop, "top"))
	mux.HandleFunc("GET /tag/{tag}", srv.handleTag)
	mux.HandleFunc("GET /p/{id}", srv.handlePost)
	mux.HandleFunc("GET /healthz", srv.handleHealth)
	mux.HandleFunc("GET /robots.txt", srv.handleRobots)

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
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
}

func newServer(st *store.Store, log *slog.Logger) *server {
	feedT := template.Must(template.New("base").Funcs(funcs).Parse(baseTpl))
	feedT = template.Must(feedT.Parse(feedTpl))
	postT := template.Must(template.New("base").Funcs(funcs).Parse(baseTpl))
	postT = template.Must(postT.Parse(postTpl))
	return &server{store: st, log: log, feedTpl: feedT, postTpl: postT, cache: newLRU(500)}
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
			Title: label, Sort: label, Posts: posts, Offset: offset,
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
		Title: "#" + tag, Sort: "tag", Tag: tag, Posts: posts, Offset: offset,
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
		Title: post.Title, Post: *post, Threads: threadComments(comments),
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
	fmt.Fprint(w, "User-agent: *\nAllow: /\n")
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

type feedView struct {
	Title  string
	Sort   string
	Tag    string
	Posts  []store.Post
	Offset int
}

type postView struct {
	Title   string
	Post    store.Post
	Threads []*commentNode
}

type commentNode struct {
	C        store.Comment
	Children []*commentNode
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

// LRU =================================================================

type lruEntry struct {
	body []byte
	exp  time.Time
}

type lru struct {
	mu  sync.Mutex
	m   map[string]lruEntry
	max int
}

func newLRU(max int) *lru { return &lru{m: make(map[string]lruEntry, max), max: max} }

func (l *lru) get(key string) ([]byte, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	e, ok := l.m[key]
	if !ok || time.Now().After(e.exp) {
		delete(l.m, key)
		return nil, false
	}
	return e.body, true
}

func (l *lru) set(key string, body []byte, ttl time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if len(l.m) >= l.max {
		now := time.Now()
		for k, e := range l.m {
			if now.After(e.exp) {
				delete(l.m, k)
			}
		}
		for len(l.m) >= l.max {
			for k := range l.m {
				delete(l.m, k)
				break
			}
		}
	}
	l.m[key] = lruEntry{body: body, exp: time.Now().Add(ttl)}
}

// helpers =============================================================

func redactURL(u string) string {
	if base, _, ok := strings.Cut(u, "?"); ok {
		return base + "?…"
	}
	return u
}

var funcs = template.FuncMap{
	"ago": func(t time.Time) string {
		d := time.Since(t)
		switch {
		case d < time.Minute:
			return "just now"
		case d < time.Hour:
			return fmt.Sprintf("%dm ago", int(d.Minutes()))
		case d < 24*time.Hour:
			return fmt.Sprintf("%dh ago", int(d.Hours()))
		default:
			return fmt.Sprintf("%dd ago", int(d.Hours()/24))
		}
	},
	"snippet": func(s string) string {
		s = strings.TrimSpace(s)
		r := []rune(s)
		if len(r) <= 220 {
			return s
		}
		return string(r[:220]) + "…"
	},
	"author": func(name string, id int64) string {
		if name == "" {
			return fmt.Sprintf("anony-%d", id)
		}
		return name
	},
	"add": func(a, b int) int { return a + b },
}

// templates ===========================================================
//
// Inline stylesheet: zero static asset requests, monospace-leaning to
// echo the SSH UI. html/template auto-escapes all interpolated user
// content.

const baseTpl = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — vask</title>
<style>
  /* palette mirrors internal/tui/style.go — same tokens as vosslabs.org */
  :root {
    --bg:        #0a0a0a;
    --fg:        #FAFAFA;
    --dim:       #A8A29E;
    --mute:      #737373;
    --border:    #3A3A3A;
    --border-hi: #525252;
    --brand:     #FB7A3C;
    --brand-deep:#C4421D;
    color-scheme: dark;
  }
  html, body { background: var(--bg); color: var(--fg); }
  body { font: 15px/1.55 ui-monospace, SFMono-Regular, Menlo, monospace;
         max-width: 760px; margin: 0 auto; padding: 1.5rem 1rem 3rem; }
  a { color: var(--brand); text-underline-offset: 3px; }
  a:hover { color: var(--brand-deep); }
  header, footer { display: flex; gap: 1rem; align-items: baseline; flex-wrap: wrap; }
  header { border-bottom: 1px solid var(--border); padding-bottom: .8rem; margin-bottom: 1rem; }
  footer { border-top: 1px solid var(--border); padding-top: .8rem; margin-top: 2.5rem; color: var(--mute); }
  header strong a { color: var(--brand); }
  h1, h2 { font-size: 1.05rem; color: var(--brand); margin: 1.2rem 0 .5rem; }
  ul.feed { list-style: none; padding: 0; margin: 0; }
  ul.feed li { padding: .9rem 0; border-bottom: 1px dashed var(--border); }
  ul.feed li > a { text-decoration: none; }
  ul.feed li > a strong { color: var(--fg); }
  ul.feed li > a:hover strong { color: var(--brand); }
  .meta { font-size: .85em; color: var(--dim); margin: .25rem 0 .35rem; }
  .body { white-space: pre-wrap; word-wrap: break-word; color: var(--fg); }
  .tags a { margin-right: .4em; }
  .comments { margin-top: 1.8rem; }
  .comment { border-left: 2px solid var(--border-hi); padding: .35rem .8rem; margin: .55rem 0; }
  .comment .children { margin-left: .8rem; }
  .pager { margin-top: 1.5rem; display: flex; gap: 1.5rem; }
  .hint { margin-left: auto; color: var(--mute); font-size: .85em; }
  ::selection { background: var(--brand); color: var(--bg); }
</style>
</head><body>
<header>
  <strong><a href="/">vask</a></strong>
  <a href="/">hot</a> <a href="/new">new</a> <a href="/top">top</a>
  <span class="hint">read-only mirror — ssh vask.vosslabs.org for the real thing</span>
</header>
{{block "main" .}}{{end}}
<footer style="margin-top:3rem">
  <span>vask.vosslabs.org</span>
  <a href="https://github.com/voss-labs/vask">source</a>
</footer>
</body></html>`

const feedTpl = `{{define "main"}}
<h1>{{if eq .Sort "tag"}}#{{.Tag}}{{else}}{{.Sort}}{{end}}</h1>
<ul class="feed">
{{range .Posts}}
  <li>
    <a href="/p/{{.ID}}"><strong>{{.Title}}</strong></a>
    <div class="meta">
      {{author .Username .UserID}} · {{ago .CreatedAt}} ·
      {{.Score}} pts · {{.CommentCount}} comments
      {{if .Tags}}· <span class="tags">{{range .Tags}}<a href="/tag/{{.}}">#{{.}}</a> {{end}}</span>{{end}}
    </div>
    <div class="body">{{snippet .Body}}</div>
  </li>
{{else}}
  <li>nothing here yet.</li>
{{end}}
</ul>
{{if .Posts}}
<div class="pager">
  {{if gt .Offset 0}}<a href="?offset={{add .Offset -20}}">← prev</a>{{end}}
  {{if eq (len .Posts) 20}}<a href="?offset={{add .Offset 20}}">next →</a>{{end}}
</div>
{{end}}
{{end}}`

const postTpl = `{{define "main"}}
<article class="post">
  <h1>{{.Post.Title}}</h1>
  <div class="meta">
    {{author .Post.Username .Post.UserID}} · {{ago .Post.CreatedAt}} ·
    {{.Post.Score}} pts · {{.Post.CommentCount}} comments
    {{if .Post.Tags}}· <span class="tags">{{range .Post.Tags}}<a href="/tag/{{.}}">#{{.}}</a> {{end}}</span>{{end}}
  </div>
  <div class="body">{{.Post.Body}}</div>
</article>
<section class="comments">
  <h2>{{.Post.CommentCount}} comments</h2>
  {{template "thread" .Threads}}
</section>
{{end}}
{{define "thread"}}
{{range .}}
  <div class="comment">
    <div class="meta">{{author .C.Username .C.UserID}} · {{ago .C.CreatedAt}} · {{.C.Score}} pts</div>
    <div class="body">{{.C.Body}}</div>
    {{if .Children}}<div class="children">{{template "thread" .Children}}</div>{{end}}
  </div>
{{end}}
{{end}}`
