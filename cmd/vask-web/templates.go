package main

import (
	"fmt"
	"html/template"
	"strings"
	"time"
)

// funcs are shared template helpers. html/template auto-escapes their
// string returns when interpolated.
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
		if len(r) <= 240 {
			return s
		}
		return string(r[:240]) + "…"
	},
	"author": func(name string, id int64) string {
		if name == "" {
			return fmt.Sprintf("anony-%d", id)
		}
		return name
	},
	"add": func(a, b int) int { return a + b },
}

// Templates ============================================================
//
// Compact terminal-leaning layout. Same palette/discipline as the SSH
// TUI (internal/tui/style.go) and vosslabs.org: dark background, brand
// orange used only as an accent, sharp 1px borders, system monospace
// stack throughout. Inline CSS — zero static asset requests.

const baseTpl = `<!doctype html>
<html lang="en"><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}} — vask</title>
<meta name="description" content="{{.Description}}">
<meta name="theme-color" content="#0A0A0A">
<link rel="canonical" href="{{.BaseURL}}{{.Path}}">

<!-- Brand assets shared with vosslabs.org (CF Pages) -->
<link rel="icon" href="https://vosslabs.org/brand/favicon/favicon.svg" type="image/svg+xml">
<link rel="icon" href="https://vosslabs.org/brand/favicon/favicon-32.png" type="image/png" sizes="32x32">
<link rel="apple-touch-icon" href="https://vosslabs.org/brand/favicon/apple-touch-icon.png" sizes="180x180">
<link rel="manifest" href="https://vosslabs.org/brand/favicon/site.webmanifest">

<meta property="og:type" content="{{.OGType}}">
<meta property="og:title" content="{{.Title}}">
<meta property="og:description" content="{{.Description}}">
<meta property="og:url" content="{{.BaseURL}}{{.Path}}">
<meta property="og:site_name" content="vask · vosslabs.org">
<meta property="og:locale" content="en_US">
{{if .OGImage}}<meta property="og:image" content="{{.OGImage}}">{{end}}

<meta name="twitter:card" content="{{if .OGImage}}summary_large_image{{else}}summary{{end}}">
<meta name="twitter:title" content="{{.Title}}">
<meta name="twitter:description" content="{{.Description}}">
{{if .OGImage}}<meta name="twitter:image" content="{{.OGImage}}">{{end}}

{{block "articleMeta" .}}{{end}}
<style>
  :root {
    color-scheme: dark;
    --bg:        #0a0a0a;
    --bg-alt:    #111111;
    --fg:        #FAFAFA;
    --dim:       #A3A3A3;
    --mute:      #737373;
    --border:    #262626;
    --border-hi: #3a3a3a;
    --brand:     #FB7A3C;
    --brand-deep:#C4421D;
    --max-w:     760px;
    --gutter:    20px;
    --mono:      ui-monospace, SFMono-Regular, 'JetBrains Mono', Menlo, Consolas, monospace;
  }
  * { box-sizing: border-box; }
  html, body { background: var(--bg); margin: 0; padding: 0; }
  body {
    color: var(--fg); font-family: var(--mono);
    font-size: 14px; line-height: 1.55;
    -webkit-font-smoothing: antialiased; -moz-osx-font-smoothing: grayscale;
  }
  ::selection { background: var(--brand); color: var(--bg); }

  a { color: var(--brand); text-decoration: none;
      border-bottom: 1px solid transparent;
      transition: color .12s, border-color .12s; }
  a:hover { color: var(--brand-deep); }

  .container { max-width: var(--max-w); margin: 0 auto; padding: 0 var(--gutter); }
  code { font-family: var(--mono); }

  /* nav ============================================================= */
  .nav { border-bottom: 1px solid var(--border); padding: 14px 0; }
  .nav-inner { display: flex; align-items: center; gap: 22px; flex-wrap: wrap; }
  .logo { display: inline-flex; align-items: center; gap: 8px;
          font-weight: 700; font-size: 0.95rem;
          color: var(--fg); border-bottom: none; }
  .logo:hover { color: var(--fg); }
  .logo-dot { width: 8px; height: 8px; background: var(--brand); display: inline-block; }
  .nav-links { display: flex; gap: 16px; align-items: baseline; }
  .nav-link { font-size: 0.85rem; color: var(--dim); border-bottom: none; padding-bottom: 1px; }
  .nav-link:hover { color: var(--fg); }
  .nav-link.active { color: var(--brand); border-bottom: 1px solid var(--brand); }
  .nav-eyebrow {
    margin-left: auto; color: var(--mute); font-size: 0.78rem;
    display: inline-flex; align-items: baseline; gap: 8px; flex-wrap: wrap;
  }
  .nav-eyebrow .dot { color: var(--brand); }
  .nav-eyebrow .label { color: var(--mute); }
  .nav-eyebrow .cmd { color: var(--fg); user-select: all; }
  .nav-eyebrow .cmd::before { content: '$ '; color: var(--brand); }

  /* feed ============================================================ */
  main { padding: 24px 0 8px; }
  .label {
    color: var(--mute); font-size: 0.78rem; letter-spacing: 0.04em;
    margin: 0 0 14px;
  }
  .label .dot { color: var(--brand); }
  .label .accent { color: var(--brand); }

  .feed { list-style: none; padding: 0; margin: 0; }
  .feed-item { padding: 16px 0; border-bottom: 1px dashed var(--border); }
  .feed-item:last-child { border-bottom: none; }
  .feed-title { font-size: 0.95rem; font-weight: 700; margin: 0 0 4px; line-height: 1.4; }
  .feed-title a { color: var(--fg); border-bottom: none; }
  .feed-title a:hover { color: var(--brand); }
  .feed-snippet { margin: 6px 0 0; color: var(--dim); white-space: pre-wrap; word-wrap: break-word; }

  .empty {
    padding: 40px 0; text-align: center; color: var(--mute);
    font-size: 0.85rem;
  }

  /* meta line ======================================================= */
  .meta { font-size: 0.78rem; color: var(--mute);
          display: flex; flex-wrap: wrap; gap: 10px; align-items: center; }
  .meta .sep { color: var(--border-hi); }
  .meta a { color: var(--brand); border-bottom: none; }
  .meta a:hover { color: var(--brand-deep); }

  /* post page ======================================================= */
  .post { padding: 16px 0 8px; }
  .post-title { font-size: 1.1rem; font-weight: 700; margin: 0 0 6px; line-height: 1.35; }
  .post-body { font-size: 0.95rem; line-height: 1.6; margin: 14px 0 0;
               white-space: pre-wrap; word-wrap: break-word; }

  /* comments ======================================================== */
  .comments-head { border-top: 1px solid var(--border); padding-top: 18px; margin-top: 24px; }
  .comments-title { font-size: 0.78rem; color: var(--mute); margin: 0 0 12px; letter-spacing: 0.04em; }
  .comments-title .count { color: var(--brand); }
  .comment { padding: 8px 0 8px 12px; border-left: 1px solid var(--border-hi); margin: 8px 0; }
  .comment .meta { margin-bottom: 4px; }
  .comment-body { font-size: 0.92rem; line-height: 1.55; color: var(--fg);
                  white-space: pre-wrap; word-wrap: break-word; }
  .comment .children { margin-left: 10px; margin-top: 4px; }
  .no-comments { padding: 8px 0; color: var(--mute); font-size: 0.78rem; }

  /* pager =========================================================== */
  .pager { display: flex; gap: 22px; padding: 18px 0;
           border-top: 1px solid var(--border); margin-top: 16px; font-size: 0.85rem; }
  .pager a { color: var(--dim); border-bottom: none; }
  .pager a:hover { color: var(--brand); }

  /* footer ========================================================== */
  .footer { border-top: 1px solid var(--border); padding: 28px 0 24px; margin-top: 40px; }
  .footer-top { display: flex; justify-content: space-between; gap: 28px; flex-wrap: wrap; margin-bottom: 18px; }
  .footer-tag { font-size: 0.78rem; color: var(--mute); display: block; max-width: 360px; line-height: 1.6; margin-top: 6px; }
  .footer-tag .accent { color: var(--brand); }
  .footer-links { display: flex; gap: 32px; font-size: 0.82rem; }
  .footer-col { display: flex; flex-direction: column; gap: 6px; }
  .footer-col h4 { margin: 0 0 2px; color: var(--mute); font-size: 0.72rem; font-weight: 500; letter-spacing: 0.04em; }
  .footer-col a { color: var(--dim); border-bottom: none; }
  .footer-col a:hover { color: var(--fg); }
  .footer-bottom { border-top: 1px solid var(--border); padding-top: 14px;
                   font-size: 0.75rem; color: var(--mute); line-height: 1.6; }
  .footer-bottom code { color: var(--fg); }

  @media (max-width: 600px) {
    .nav-inner { gap: 14px; }
    .nav-links { gap: 12px; }
    .nav-eyebrow { font-size: 0.72rem; width: 100%; margin-left: 0; padding-top: 4px; }
    .nav-eyebrow .label { display: none; }
    .footer-top { flex-direction: column; gap: 22px; }
  }
</style>
</head><body>
<nav class="nav"><div class="container nav-inner">
  <a href="/" class="logo"><span>vask</span><span class="logo-dot" aria-hidden="true"></span></a>
  <div class="nav-links">
    <a class="nav-link {{if eq .Sort "hot"}}active{{end}}" href="/">hot</a>
    <a class="nav-link {{if eq .Sort "new"}}active{{end}}" href="/new">new</a>
    <a class="nav-link {{if eq .Sort "top"}}active{{end}}" href="/top">top</a>
  </div>
  <span class="nav-eyebrow">
    <span class="label"><span class="dot">·</span> reply, vote, or post:</span>
    <code class="cmd">ssh vask.vosslabs.org</code>
  </span>
</div></nav>

<main><div class="container">
{{block "main" .}}{{end}}
</div></main>

<footer class="footer"><div class="container">
  <div class="footer-top">
    <div>
      <a href="/" class="logo"><span>vask</span><span class="logo-dot" aria-hidden="true"></span></a>
      <span class="footer-tag">Vidyalankar <span class="accent">Open Source</span> Software Labs · ask channel · read-only web mirror</span>
    </div>
    <div class="footer-links">
      <div class="footer-col">
        <h4>browse</h4>
        <a href="/">hot</a>
        <a href="/new">new</a>
        <a href="/top">top</a>
      </div>
      <div class="footer-col">
        <h4>source</h4>
        <a href="https://github.com/voss-labs/vask">github</a>
        <a href="https://vosslabs.org">vosslabs.org</a>
      </div>
    </div>
  </div>
  <div class="footer-bottom">
    to post, vote, or reply &nbsp;·&nbsp; <code>ssh vask.vosslabs.org</code>
  </div>
</div></footer>
</body></html>`

const feedTpl = `{{define "main"}}
<p class="label">
  <span class="dot">·</span>
  {{if eq .Sort "tag"}}vask · tag · <span class="accent">#{{.Tag}}</span>
  {{else if eq .Sort "hot"}}vask · discussions · hot
  {{else if eq .Sort "new"}}vask · discussions · new
  {{else if eq .Sort "top"}}vask · discussions · top
  {{else}}vask · {{.Sort}}{{end}}
</p>

<ul class="feed">
{{range .Posts}}
  <li class="feed-item">
    <h2 class="feed-title"><a href="/p/{{.ID}}">{{.Title}}</a></h2>
    <div class="meta">
      <span>{{author .Username .UserID}}</span>
      <span class="sep">·</span>
      <span>{{ago .CreatedAt}}</span>
      <span class="sep">·</span>
      <span>{{.Score}} pts</span>
      <span class="sep">·</span>
      <span>{{.CommentCount}} comments</span>
      {{range .Tags}}<a href="/tag/{{.}}">#{{.}}</a>{{end}}
    </div>
    {{if .Body}}<p class="feed-snippet">{{snippet .Body}}</p>{{end}}
  </li>
{{else}}
  <li class="empty">— no posts yet —</li>
{{end}}
</ul>

{{if .Posts}}
<nav class="pager">
  {{if gt .Offset 0}}<a href="?offset={{add .Offset -20}}">← prev</a>{{end}}
  {{if eq (len .Posts) 20}}<a href="?offset={{add .Offset 20}}">next →</a>{{end}}
</nav>
{{end}}
{{end}}`

const postTpl = `{{define "main"}}
<p class="label"><span class="dot">·</span> vask · post</p>

<article class="post">
  <h1 class="post-title">{{.Post.Title}}</h1>
  <div class="meta">
    <span>{{author .Post.Username .Post.UserID}}</span>
    <span class="sep">·</span>
    <span>{{ago .Post.CreatedAt}}</span>
    <span class="sep">·</span>
    <span>{{.Post.Score}} pts</span>
    <span class="sep">·</span>
    <span>{{.Post.CommentCount}} comments</span>
    {{range .Post.Tags}}<a href="/tag/{{.}}">#{{.}}</a>{{end}}
  </div>

  {{if .Post.Body}}<div class="post-body">{{.Post.Body}}</div>{{end}}

  <section class="comments-head">
    <h2 class="comments-title">comments <span class="count">{{.Post.CommentCount}}</span></h2>
    {{if .Threads}}
      {{template "thread" .Threads}}
    {{else}}
      <p class="no-comments">— no replies yet —</p>
    {{end}}
  </section>
</article>
{{end}}

{{define "thread"}}
{{range .}}
  <div class="comment">
    <div class="meta">
      <span>{{author .C.Username .C.UserID}}</span>
      <span class="sep">·</span>
      <span>{{ago .C.CreatedAt}}</span>
      <span class="sep">·</span>
      <span>{{.C.Score}} pts</span>
    </div>
    <div class="comment-body">{{.C.Body}}</div>
    {{if .Children}}<div class="children">{{template "thread" .Children}}</div>{{end}}
  </div>
{{end}}
{{end}}

{{define "articleMeta"}}
<meta property="article:published_time" content="{{.Post.CreatedAt.UTC.Format "2006-01-02T15:04:05Z"}}">
<meta property="article:author" content="{{author .Post.Username .Post.UserID}}">
{{range .Post.Tags}}<meta property="article:tag" content="{{.}}">
{{end}}{{end}}`
