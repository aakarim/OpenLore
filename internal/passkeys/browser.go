package passkeys

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"mime"
	"net/http"
	"net/url"
	"path"
	"sort"
	"strings"

	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// LoreBrowserHandler serves an authenticated web browser over the filesystem.
// Unauthenticated requests are redirected to the passkey login page.
//
// fsForIdentity returns the per-identity, read-scoped filesystem for a resolved
// session identity — the SAME layered session FS used by the SSH shell, SFTP,
// and MCP/HTTP transports. The browser performs all Stat/ReadDir/ReadFile calls
// through that scoped FS, so docset boundaries (including the carve-out of nested
// docsets from an ancestor grant) are enforced identically here. There is no
// separate allow-list in the browser: the scoped FS is the sole authority on
// what a session may see.
func (p *Passkeys) LoreBrowserHandler(fsForIdentity func(identity string) vfs.FileSystem) http.Handler {
	lorePath := "/" + strings.Trim(p.cfg.LorePath, "/")
	if lorePath == "/" {
		lorePath = "/lore"
	}

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if servePWAAsset(w, r, lorePath) {
			return
		}

		session, ok := p.sessions.ValidateRequest(r)
		if !ok {
			redirect := url.QueryEscape(r.URL.Path)
			http.Redirect(w, r, "/passkey/login?redirect="+redirect, http.StatusFound)
			return
		}

		fsys := fsForIdentity(session.Identity)
		if fsys == nil {
			http.Error(w, "403 forbidden", http.StatusForbidden)
			return
		}

		// Map the request path under lorePath onto a filesystem path.
		rel := strings.TrimPrefix(r.URL.Path, lorePath)
		if rel == "" {
			http.Redirect(w, r, lorePath+"/", http.StatusFound)
			return
		}
		fsPath := vfs.CleanPath(rel)

		info, err := fsys.Stat(fsPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}

		if info.Dir {
			p.renderDir(w, fsys, lorePath, fsPath)
			return
		}

		data, err := fsys.ReadFile(fsPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("raw") != "1" {
			p.renderFile(w, r, lorePath, fsPath)
			return
		}
		if isMarkdown(fsPath) {
			if err := renderMarkdown(w, data); err != nil {
				http.Error(w, "failed to render markdown", http.StatusInternalServerError)
			}
			return
		}
		ctype := mime.TypeByExtension(path.Ext(fsPath))
		if ctype == "" {
			ctype = "text/plain; charset=utf-8"
		}
		w.Header().Set("Content-Type", ctype)
		w.Write(data)
	})
}

func (p *Passkeys) renderFile(w http.ResponseWriter, r *http.Request, lorePath, fsPath string) {
	parentURL := lorePath + path.Dir(fsPath)
	if path.Dir(fsPath) == "/" {
		parentURL += "/"
	}

	var crumbs strings.Builder
	fmt.Fprintf(&crumbs, `<a href="%s/">Lore</a>`, html.EscapeString(strings.TrimRight(lorePath, "/")))
	parts := strings.Split(strings.Trim(fsPath, "/"), "/")
	for i, part := range parts {
		crumbs.WriteString(`<span class="separator">/</span>`)
		if i == len(parts)-1 {
			fmt.Fprintf(&crumbs, `<span aria-current="page">%s</span>`, html.EscapeString(part))
			continue
		}
		crumbPath := "/" + strings.Join(parts[:i+1], "/")
		fmt.Fprintf(&crumbs, `<a href="%s%s/">%s</a>`, html.EscapeString(lorePath), html.EscapeString(crumbPath), html.EscapeString(part))
	}

	iframeURL := r.URL.EscapedPath() + "?raw=1"
	name := path.Base(fsPath)
	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">`)
	b.WriteString(`<meta name="viewport" content="width=device-width, initial-scale=1">`)
	b.WriteString(pwaHead(lorePath))
	fmt.Fprintf(&b, "<title>%s — OpenLore</title>", html.EscapeString(name))
	b.WriteString(`<style>*{box-sizing:border-box}html,body{height:100%;margin:0}body{display:flex;flex-direction:column;background:#0d1117;color:#c9d1d9;font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif}.bar{height:3rem;flex:none;display:flex;align-items:center;gap:.65rem;padding:0 .85rem;border-bottom:1px solid #30363d;background:#161b22}.breadcrumbs{display:flex;align-items:center;gap:.5rem;min-width:0;overflow:hidden;white-space:nowrap;font-size:.9rem}.breadcrumbs a{color:#58a6ff;text-decoration:none}.breadcrumbs a:hover{text-decoration:underline}.breadcrumbs span[aria-current=page]{overflow:hidden;text-overflow:ellipsis;color:#c9d1d9}.separator{color:#6e7681}.close{display:grid;place-items:center;width:2rem;height:2rem;margin-left:auto;flex:none;border-radius:6px;color:#8b949e;text-decoration:none;font-size:1.35rem;line-height:1}.close:hover{background:#30363d;color:#f0f6fc}iframe{width:100%;flex:1;border:0;background:#fff}</style></head><body>`)
	fmt.Fprintf(&b, `<nav class="bar" aria-label="File navigation"><div class="breadcrumbs">%s</div><a class="close" href="%s" aria-label="Close file and return to folder" title="Close">×</a></nav>`, crumbs.String(), html.EscapeString(parentURL))
	fmt.Fprintf(&b, `<iframe src="%s" title="%s"></iframe>`, html.EscapeString(iframeURL), html.EscapeString(name))
	b.WriteString(`</body></html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(b.String()))
}

func isMarkdown(filePath string) bool {
	ext := strings.ToLower(path.Ext(filePath))
	return ext == ".md" || ext == ".markdown"
}

func renderMarkdown(w http.ResponseWriter, source []byte) error {
	frontmatter, markdown, hasFrontmatter := okf.SplitFrontmatter(source)
	if !hasFrontmatter {
		markdown = source
	}

	var rendered bytes.Buffer
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	if err := md.Convert(markdown, &rendered); err != nil {
		return err
	}

	var formattedFrontmatter string
	if hasFrontmatter {
		formattedFrontmatter = `<section class="frontmatter" aria-label="Frontmatter"><div class="frontmatter-title">Frontmatter</div><pre><code>` + html.EscapeString(strings.TrimSpace(string(frontmatter))) + `</code></pre></section>`
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := fmt.Fprintf(w, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><base target="_top"><meta name="viewport" content="width=device-width, initial-scale=1"><style>:root{color-scheme:light dark}*{box-sizing:border-box}body{max-width:860px;margin:0 auto;padding:2.5rem 2rem;font:16px/1.65 -apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;color:#1f2328;background:#fff}h1,h2{border-bottom:1px solid #d0d7de;padding-bottom:.3em}h1,h2,h3,h4,h5,h6{line-height:1.25;margin:1.5em 0 .65em}h1:first-child{margin-top:0}a{color:#0969da}pre{overflow:auto;padding:1rem;border-radius:6px;background:#f6f8fa}code{font:85%% SFMono-Regular,Consolas,'Liberation Mono',monospace;background:#eff1f3;padding:.2em .4em;border-radius:4px}pre code{background:transparent;padding:0}.frontmatter{margin:0 0 2rem;border:1px solid #d0d7de;border-radius:6px;overflow:hidden}.frontmatter-title{padding:.45rem .8rem;border-bottom:1px solid #d0d7de;background:#f6f8fa;color:#59636e;font-size:.75rem;font-weight:600;text-transform:uppercase;letter-spacing:.05em}.frontmatter pre{margin:0;border-radius:0}blockquote{margin-left:0;padding-left:1em;border-left:4px solid #d0d7de;color:#59636e}img{max-width:100%%}table{border-collapse:collapse;display:block;overflow:auto}th,td{padding:.4rem .8rem;border:1px solid #d0d7de}tr:nth-child(2n){background:#f6f8fa}hr{border:0;border-top:1px solid #d0d7de}@media(prefers-color-scheme:dark){body{color:#e6edf3;background:#0d1117}a{color:#58a6ff}h1,h2,hr{border-color:#30363d}pre,code,tr:nth-child(2n),.frontmatter-title{background:#161b22}.frontmatter,.frontmatter-title{border-color:#30363d}.frontmatter-title,blockquote{color:#9198a1}blockquote{border-color:#3d444d}th,td{border-color:#3d444d}}</style></head><body>%s%s</body></html>`, formattedFrontmatter, rendered.String())
	return err
}

func (p *Passkeys) renderDir(w http.ResponseWriter, fsys vfs.FileSystem, lorePath, fsPath string) {
	entries, err := fsys.ReadDir(fsPath)
	if err != nil {
		http.Error(w, "404 not found", http.StatusNotFound)
		return
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Dir != entries[j].Dir {
			return entries[i].Dir
		}
		return entries[i].FileName < entries[j].FileName
	})

	var b strings.Builder
	b.WriteString("<!DOCTYPE html><html lang=\"en\"><head><meta charset=\"utf-8\">")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">")
	b.WriteString(pwaHead(lorePath))
	b.WriteString("<title>OpenLore</title><style>")
	b.WriteString("*{box-sizing:border-box;margin:0;padding:0}body{font-family:-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#0d1117;color:#c9d1d9;padding:2rem;max-width:820px;margin:0 auto}")
	b.WriteString("h1{font-size:1.2rem;margin-bottom:1rem;color:#c9d1d9}a{color:#58a6ff;text-decoration:none}a:hover{text-decoration:underline}")
	b.WriteString("ul{list-style:none}li{padding:0.35rem 0;border-bottom:1px solid #21262d}.entry{display:block;padding:.35rem 0;touch-action:manipulation}.dir{color:#79c0ff}.crumb{color:#8b949e;margin-bottom:1.5rem;font-size:0.9rem}.ios-install{display:none;margin:0 0 1.5rem;padding:.85rem 1rem;border:1px solid #30363d;border-radius:8px;background:#161b22;color:#c9d1d9;font-size:.9rem;line-height:1.45}.ios-install strong{display:block;margin-bottom:.2rem;color:#f0f6fc}.share-icon{display:inline-block;color:#58a6ff;font-size:1.15rem;line-height:1;vertical-align:-.08rem}.action-overlay{position:fixed;inset:0;z-index:10;display:grid;place-items:center;padding:1.25rem;background:rgba(0,0,0,.62)}.action-overlay[hidden]{display:none}.action-sheet{width:min(22rem,100%);padding:1rem;border:1px solid #30363d;border-radius:12px;background:#161b22;box-shadow:0 16px 48px rgba(0,0,0,.45)}.action-name{overflow:hidden;margin-bottom:.25rem;color:#f0f6fc;font-weight:600;text-overflow:ellipsis;white-space:nowrap}.action-hint{margin-bottom:1rem;color:#8b949e;font-size:.8rem}.action-buttons{display:grid;grid-template-columns:repeat(3,1fr);gap:.65rem}.action-buttons button{min-height:2.75rem;border:1px solid #30363d;border-radius:8px;background:#21262d;color:#f0f6fc;font:inherit;cursor:pointer}.action-buttons button:active{background:#30363d}.copy-status{min-height:1.2rem;margin-top:.65rem;color:#7ee787;font-size:.8rem;text-align:center}</style></head><body>")

	fmt.Fprintf(&b, "<h1>📜 %s</h1>", html.EscapeString(fsPath))
	b.WriteString(`<aside class="ios-install" id="ios-install" aria-label="Install OpenLore"><strong>Add OpenLore to your Home Screen</strong>In Safari, tap Share <span class="share-icon" aria-hidden="true">□<span style="position:relative;left:-.72em;top:-.28em">↑</span></span>, then tap <b>Add to Home Screen</b>. OpenLore will then launch in its own app window.</aside>`)
	b.WriteString(`<script>(()=>{const standalone=window.matchMedia('(display-mode: standalone)').matches||navigator.standalone===true;const ua=navigator.userAgent;const ios=/iPad|iPhone|iPod/.test(ua)||(navigator.platform==='MacIntel'&&navigator.maxTouchPoints>1);const safari=/Safari/.test(ua)&&!/CriOS|FxiOS|EdgiOS|OPiOS/.test(ua);if(ios&&safari&&!standalone)document.getElementById('ios-install').style.display='block'})()</script>`)

	if fsPath != "/" {
		parent := path.Dir(fsPath)
		fmt.Fprintf(&b, "<div class=\"crumb\"><a href=\"%s\">⬆ up</a></div>", html.EscapeString(lorePath+parent))
	}

	b.WriteString("<ul>")
	for _, e := range entries {
		child := path.Join(fsPath, e.FileName)
		href := lorePath + child
		name := html.EscapeString(e.FileName)
		if e.Dir {
			fmt.Fprintf(&b, "<li><a class=\"entry dir\" href=\"%s/\" data-path=\"%s\">%s/</a></li>", html.EscapeString(href), html.EscapeString(child), name)
		} else {
			fmt.Fprintf(&b, "<li><a class=\"entry\" href=\"%s\" data-path=\"%s\">%s</a></li>", html.EscapeString(href), html.EscapeString(child), name)
		}
	}
	b.WriteString(`</ul><div class="action-overlay" id="entry-actions" hidden role="dialog" aria-modal="true" aria-labelledby="action-name"><div class="action-sheet"><div class="action-name" id="action-name"></div><div class="action-hint">Double tap to open</div><div class="action-buttons"><button type="button" data-open>Open</button><button type="button" data-copy="url">Copy URL</button><button type="button" data-copy="path">Copy path</button></div><div class="copy-status" id="copy-status" aria-live="polite"></div></div></div>`)
	b.WriteString(`<script>(()=>{const overlay=document.getElementById('entry-actions');const name=document.getElementById('action-name');const status=document.getElementById('copy-status');let selected=null;let tapTimer=null;function hide(){overlay.hidden=true;status.textContent='';selected=null}function show(link){selected=link;name.textContent=link.textContent;status.textContent='';overlay.hidden=false}async function copy(text,label){try{if(navigator.clipboard&&navigator.clipboard.writeText){await navigator.clipboard.writeText(text)}else{const area=document.createElement('textarea');area.value=text;area.style.position='fixed';area.style.opacity='0';document.body.appendChild(area);area.select();if(!document.execCommand('copy'))throw new Error('copy failed');area.remove()}status.textContent=label+' copied'}catch{status.textContent='Could not copy'}}document.querySelectorAll('.entry').forEach(link=>link.addEventListener('click',event=>{if(event.detail===0)return;event.preventDefault();if(tapTimer&&selected===link){clearTimeout(tapTimer);tapTimer=null;window.location.assign(link.href);return}if(tapTimer)clearTimeout(tapTimer);selected=link;tapTimer=setTimeout(()=>{tapTimer=null;show(link)},275)}));overlay.addEventListener('click',event=>{if(event.target===overlay)hide()});overlay.querySelector('[data-open]').addEventListener('click',()=>{if(selected)window.location.assign(selected.href)});overlay.querySelectorAll('[data-copy]').forEach(button=>button.addEventListener('click',()=>{if(!selected)return;const kind=button.dataset.copy;copy(kind==='url'?selected.href:selected.dataset.path,kind==='url'?'URL':'Path')}));document.addEventListener('keydown',event=>{if(event.key==='Escape'&&!overlay.hidden)hide()})})()</script></body></html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(b.String()))
}

func pwaHead(lorePath string) string {
	manifestURL := html.EscapeString(lorePath + "/manifest.webmanifest")
	iconURL := html.EscapeString(lorePath + "/app-icon.svg")
	serviceWorkerURL, _ := json.Marshal(lorePath + "/service-worker.js")
	return fmt.Sprintf(`<link rel="manifest" href="%s"><link rel="apple-touch-icon" href="%s"><meta name="theme-color" content="#0d1117"><meta name="apple-mobile-web-app-capable" content="yes"><meta name="apple-mobile-web-app-status-bar-style" content="black-translucent"><meta name="apple-mobile-web-app-title" content="OpenLore"><script>if('serviceWorker'in navigator)window.addEventListener('load',()=>navigator.serviceWorker.register(%s))</script>`, manifestURL, iconURL, serviceWorkerURL)
}

func servePWAAsset(w http.ResponseWriter, r *http.Request, lorePath string) bool {
	switch r.URL.Path {
	case lorePath + "/manifest.webmanifest":
		w.Header().Set("Content-Type", "application/manifest+json")
		w.Header().Set("Cache-Control", "public, max-age=3600")
		json.NewEncoder(w).Encode(map[string]any{
			"name":             "OpenLore",
			"short_name":       "OpenLore",
			"description":      "Browse your OpenLore knowledge filesystem.",
			"start_url":        lorePath + "/",
			"scope":            lorePath + "/",
			"display":          "standalone",
			"background_color": "#0d1117",
			"theme_color":      "#0d1117",
			"icons": []map[string]string{{
				"src":     lorePath + "/app-icon.svg",
				"sizes":   "any",
				"type":    "image/svg+xml",
				"purpose": "any maskable",
			}},
		})
		return true
	case lorePath + "/service-worker.js":
		w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		fmt.Fprint(w, `self.addEventListener('install',()=>self.skipWaiting());self.addEventListener('activate',event=>event.waitUntil(self.clients.claim()));`)
		return true
	case lorePath + "/app-icon.svg":
		w.Header().Set("Content-Type", "image/svg+xml")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><rect width="512" height="512" rx="96" fill="#0d1117"/><path d="M145 104h222v304H145z" fill="#f0f6fc"/><path d="M181 164h150M181 218h150M181 272h112M181 326h132" stroke="#0d1117" stroke-width="24" stroke-linecap="round"/><path d="M122 81h222v304" fill="none" stroke="#58a6ff" stroke-width="26" stroke-linecap="round" stroke-linejoin="round"/></svg>`)
		return true
	default:
		return false
	}
}
