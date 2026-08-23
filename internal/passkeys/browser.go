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
	"time"

	"github.com/aakarim/go-openlore/pkg/okf"
	"github.com/aakarim/go-openlore/pkg/vfs"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

// FileHistoryEntry is one committed change to the file currently displayed by
// the lore browser. History is best-effort: deployments without a history
// provider still render the file view normally.
type FileHistoryEntry struct {
	Time        time.Time
	Attribution string
	Action      string
	Hash        string
}

type FileHistoryProvider func(identity, filePath string) ([]FileHistoryEntry, error)

// PermissionsPath is the settings page where a principal manages which
// delegate identities have access to what. The lore browser's identity menu
// links here; pkg/openlore mounts the handler at the same path.
const PermissionsPath = "/settings/permissions"

// identityMenuCSS styles the signed-in identity button and its dropdown,
// shared by the directory and file views.
const identityMenuCSS = `.identity-menu{position:relative;flex:none}.identity-button{display:flex;align-items:center;gap:.4rem;max-width:13rem;height:2rem;padding:0 .7rem;border:1px solid #363046;border-radius:999px;background:#2f2a3c;color:rgba(255,253,248,.88);font:inherit;font-size:.85rem;cursor:pointer}.identity-button:hover{background:#363046;color:#fffdf8}.identity-name{overflow:hidden;text-overflow:ellipsis;white-space:nowrap}.identity-caret{flex:none;color:rgba(238,233,245,.6);font-size:.6rem}.identity-dropdown{position:absolute;top:calc(100% + .4rem);right:0;z-index:20;min-width:14rem;padding:.4rem;border:1px solid #363046;border-radius:8px;background:#252130;box-shadow:0 12px 32px rgba(0,0,0,.5)}.identity-dropdown[hidden]{display:none}.identity-signed-in{margin-bottom:.35rem;padding:.5rem .6rem;border-bottom:1px solid #2f2a3c;color:rgba(238,233,245,.6);font-size:.75rem}.identity-signed-in strong{display:block;margin-top:.15rem;overflow-wrap:anywhere;color:#fffdf8;font-size:.85rem}.identity-dropdown a{display:block;padding:.5rem .6rem;border-radius:6px;color:rgba(255,253,248,.88);font-size:.85rem;text-decoration:none}.identity-dropdown a:hover{background:#2f2a3c;color:#fffdf8;text-decoration:none}`

// identityMenuScript toggles the dropdown and closes it on outside click or
// Escape.
const identityMenuScript = `<script>(()=>{const button=document.getElementById('identity-button');const dropdown=document.getElementById('identity-dropdown');function close(){dropdown.hidden=true;button.setAttribute('aria-expanded','false')}button.addEventListener('click',event=>{event.stopPropagation();if(dropdown.hidden){dropdown.hidden=false;button.setAttribute('aria-expanded','true')}else{close()}});document.addEventListener('click',event=>{if(!dropdown.hidden&&!dropdown.contains(event.target))close()});document.addEventListener('keydown',event=>{if(event.key==='Escape'&&!dropdown.hidden)close()})})()</script>`

func identityMenuHTML(identity string) string {
	name := html.EscapeString(identity)
	return fmt.Sprintf(`<div class="identity-menu"><button type="button" class="identity-button" id="identity-button" aria-haspopup="menu" aria-expanded="false" aria-controls="identity-dropdown" title="Signed in as %s"><span class="identity-name">%s</span><span class="identity-caret" aria-hidden="true">▾</span></button><div class="identity-dropdown" id="identity-dropdown" hidden role="menu"><div class="identity-signed-in">Signed in as<strong>%s</strong></div><a role="menuitem" href="%s">Permission settings</a></div></div>`, name, name, name, PermissionsPath)
}

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
func (p *Passkeys) LoreBrowserHandler(fsForIdentity func(identity string) vfs.FileSystem, historyForFile FileHistoryProvider) http.Handler {
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
			p.renderDir(w, fsys, lorePath, fsPath, session.Identity)
			return
		}

		data, err := fsys.ReadFile(fsPath)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("raw") != "1" {
			var history []FileHistoryEntry
			historyAvailable := historyForFile != nil
			if historyAvailable {
				var err error
				history, err = historyForFile(session.Identity, fsPath)
				if err != nil {
					historyAvailable = false
					if p.logger != nil {
						p.logger.Warn("file history unavailable", "identity", session.Identity, "path", fsPath, "error", err)
					}
				}
			}
			p.renderFile(w, r, lorePath, fsPath, session.Identity, history, historyAvailable)
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

func (p *Passkeys) renderFile(w http.ResponseWriter, r *http.Request, lorePath, fsPath, identity string, history []FileHistoryEntry, historyAvailable bool) {
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
	b.WriteString(`<style>@font-face{font-family:'Outfit';font-style:normal;font-weight:100 900;font-display:swap;src:url('/outfit.woff2') format('woff2')}*{box-sizing:border-box}html,body{height:100%;margin:0}body{display:flex;flex-direction:column;background:#1d1a23;color:rgba(255,253,248,.88);font-family:Outfit,-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif}.bar{height:3rem;flex:none;display:flex;align-items:center;gap:.65rem;padding:0 .85rem;border-bottom:1px solid #363046;background:#252130}.breadcrumbs{display:flex;align-items:center;gap:.5rem;min-width:0;overflow:hidden;white-space:nowrap;font-size:.9rem}.breadcrumbs a{color:#ff8a50;text-decoration:none}.breadcrumbs a:hover{text-decoration:underline}.breadcrumbs span[aria-current=page]{overflow:hidden;text-overflow:ellipsis;color:rgba(255,253,248,.88)}.separator{color:rgba(238,233,245,.45)}.history-toggle,.close{display:grid;place-items:center;height:2rem;flex:none;border:0;border-radius:6px;background:transparent;color:rgba(238,233,245,.6);font:inherit;cursor:pointer}.history-toggle{width:2rem;margin-left:auto}.history-toggle:hover,.close:hover{background:#363046;color:#fffdf8}.history-toggle svg{width:1.1rem;height:1.1rem;fill:currentColor}.close{width:2rem;text-decoration:none;font-size:1.35rem;line-height:1}.content{display:flex;min-height:0;flex:1}iframe{min-width:0;flex:1;border:0;background:#1d1a23}.history{width:20rem;flex:none;overflow:auto;border-left:1px solid #363046;background:#252130}.history[hidden]{display:none}.history-header{position:sticky;top:0;padding:1rem;border-bottom:1px solid #363046;background:#252130}.history-header h2{margin:0;color:#fffdf8;font-size:.95rem}.history-list{list-style:none;margin:0;padding:0}.history-entry{padding:1rem;border-bottom:1px solid #2f2a3c}.history-action{display:inline-block;margin-bottom:.45rem;padding:.15rem .4rem;border-radius:999px;background:#2f2a3c;color:#a880ff;font-size:.7rem;font-weight:600;text-transform:uppercase}.history-actor{overflow-wrap:anywhere;color:#fffdf8;font-size:.85rem}.history-time,.history-hash{display:block;margin-top:.3rem;color:rgba(238,233,245,.6);font-size:.75rem}.history-hash{overflow:hidden;text-overflow:ellipsis;white-space:nowrap;font-family:SFMono-Regular,Consolas,'Liberation Mono',monospace}.history-empty{padding:1rem;color:rgba(238,233,245,.6);font-size:.85rem;line-height:1.5}@media(max-width:700px){.history{position:absolute;z-index:2;top:3rem;right:0;bottom:0;width:min(20rem,88vw);box-shadow:-12px 0 30px rgba(0,0,0,.4)}}`)
	b.WriteString(identityMenuCSS)
	b.WriteString(`</style></head><body>`)
	fmt.Fprintf(&b, `<nav class="bar" aria-label="File navigation"><div class="breadcrumbs">%s</div><button class="history-toggle" id="history-toggle" type="button" aria-controls="file-history" aria-expanded="true" title="Toggle edit history"><span class="sr-only" hidden>Edit history</span><svg viewBox="0 0 16 16" aria-hidden="true"><path d="M1.643 3.143.427 1.927A.25.25 0 0 1 .604 1.5H4.75a.25.25 0 0 1 .25.25v4.146a.25.25 0 0 1-.427.177L3.31 4.81a5.5 5.5 0 1 1-.08 6.384.75.75 0 1 1 1.21-.888 4 4 0 1 0 .055-4.662L5.57 6.72A.75.75 0 0 1 4.51 7.78L1.643 4.914a1.25 1.25 0 0 1 0-1.77ZM8 4.5a.75.75 0 0 1 .75.75v2.44l1.53.765a.75.75 0 0 1-.67 1.342l-1.945-.973A.75.75 0 0 1 7.25 8.15v-2.9A.75.75 0 0 1 8 4.5Z"/></svg></button>%s<a class="close" href="%s" aria-label="Close file and return to folder" title="Close">×</a></nav>`, crumbs.String(), identityMenuHTML(identity), html.EscapeString(parentURL))
	fmt.Fprintf(&b, `<main class="content"><iframe src="%s" title="%s"></iframe><aside class="history" id="file-history" aria-label="Edit history"><header class="history-header"><h2>Edit history</h2></header>`, html.EscapeString(iframeURL), html.EscapeString(name))
	if !historyAvailable {
		b.WriteString(`<p class="history-empty">Edit history is unavailable.</p>`)
	} else if len(history) == 0 {
		b.WriteString(`<p class="history-empty">No edits have been recorded for this file.</p>`)
	} else {
		b.WriteString(`<ol class="history-list">`)
		for _, entry := range history {
			b.WriteString(`<li class="history-entry">`)
			fmt.Fprintf(&b, `<span class="history-action">%s</span><div class="history-actor">%s</div>`, html.EscapeString(strings.ReplaceAll(entry.Action, "_", " ")), html.EscapeString(entry.Attribution))
			fmt.Fprintf(&b, `<time class="history-time" datetime="%s">%s</time>`, html.EscapeString(entry.Time.Format(time.RFC3339)), html.EscapeString(entry.Time.Format("2 Jan 2006, 15:04 MST")))
			if entry.Hash != "" {
				fmt.Fprintf(&b, `<code class="history-hash" title="%s">%s</code>`, html.EscapeString(entry.Hash), html.EscapeString(entry.Hash))
			}
			b.WriteString(`</li>`)
		}
		b.WriteString(`</ol>`)
	}
	b.WriteString(`</aside></main><script>(()=>{const button=document.getElementById('history-toggle');const history=document.getElementById('file-history');button.addEventListener('click',()=>{const open=button.getAttribute('aria-expanded')==='true';button.setAttribute('aria-expanded',String(!open));history.hidden=open})})()</script>`)
	b.WriteString(identityMenuScript)
	b.WriteString(`</body></html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(b.String()))
}

func isMarkdown(filePath string) bool {
	ext := strings.ToLower(path.Ext(filePath))
	return ext == ".md" || ext == ".markdown"
}

func renderMarkdown(w http.ResponseWriter, source []byte) error {
	frontmatter, body, hasFrontmatter := okf.SplitFrontmatter(source)
	if !hasFrontmatter {
		body = source
	}

	var rendered bytes.Buffer
	md := goldmark.New(goldmark.WithExtensions(extension.GFM))
	if err := md.Convert(body, &rendered); err != nil {
		return err
	}

	var formattedFrontmatter string
	if hasFrontmatter {
		formattedFrontmatter = `<section class="frontmatter" aria-label="Frontmatter"><div class="frontmatter-title">Frontmatter</div><pre><code>` + html.EscapeString(strings.TrimSpace(string(frontmatter))) + `</code></pre></section>`
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, err := fmt.Fprintf(w, `<!DOCTYPE html><html lang="en"><head><meta charset="utf-8"><base target="_top"><meta name="viewport" content="width=device-width, initial-scale=1"><style>@font-face{font-family:'Outfit';font-style:normal;font-weight:100 900;font-display:swap;src:url('/outfit.woff2') format('woff2')}:root{color-scheme:light dark}*{box-sizing:border-box}body{max-width:860px;margin:0 auto;padding:2.5rem 2rem;font:16px/1.65 Outfit,-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;color:#3e3453;background:#fff}h1,h2{border-bottom:1px solid #e5e0da;padding-bottom:.3em}h1,h2,h3,h4,h5,h6{line-height:1.25;margin:1.5em 0 .65em}h1:first-child{margin-top:0}a{color:#e26d33}pre{overflow:auto;padding:1rem;border-radius:6px;background:#f8f6f4}code{font:85%% SFMono-Regular,Consolas,'Liberation Mono',monospace;background:#efeae6;padding:.2em .4em;border-radius:4px}pre code{background:transparent;padding:0}.frontmatter{margin:0 0 2rem;border:1px solid #e5e0da;border-radius:6px;overflow:hidden}.frontmatter-title{padding:.45rem .8rem;border-bottom:1px solid #e5e0da;background:#f8f6f4;color:rgba(62,52,83,.72);font-size:.75rem;font-weight:600;text-transform:uppercase;letter-spacing:.05em}.frontmatter pre{margin:0;border-radius:0}blockquote{margin-left:0;padding-left:1em;border-left:4px solid #e5e0da;color:rgba(62,52,83,.72)}img{max-width:100%%}table{border-collapse:collapse;display:block;overflow:auto}th,td{padding:.4rem .8rem;border:1px solid #e5e0da}tr:nth-child(2n){background:#f8f6f4}hr{border:0;border-top:1px solid #e5e0da}@media(prefers-color-scheme:dark){body{color:rgba(255,253,248,.88);background:#1d1a23}a{color:#ff8a50}h1,h2,hr{border-color:#363046}pre,code,tr:nth-child(2n),.frontmatter-title{background:#252130}.frontmatter,.frontmatter-title{border-color:#363046}.frontmatter-title,blockquote{color:rgba(238,233,245,.6)}blockquote{border-color:#363046}th,td{border-color:#363046}}</style></head><body>%s%s</body></html>`, formattedFrontmatter, rendered.String())
	return err
}

func (p *Passkeys) renderDir(w http.ResponseWriter, fsys vfs.FileSystem, lorePath, fsPath, identity string) {
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
	b.WriteString("<title>OpenLore</title><style>@font-face{font-family:'Outfit';font-style:normal;font-weight:100 900;font-display:swap;src:url('/outfit.woff2') format('woff2')}")
	b.WriteString("*{box-sizing:border-box;margin:0;padding:0}body{font-family:Outfit,-apple-system,BlinkMacSystemFont,'Segoe UI',Roboto,sans-serif;background:#1d1a23;color:rgba(255,253,248,.88)}")
	b.WriteString(".topbar{display:flex;align-items:center;justify-content:space-between;gap:.75rem;height:3rem;padding:0 .85rem;border-bottom:1px solid #363046;background:#252130}.brand{color:#fffdf8;font-size:.95rem;font-weight:600;text-decoration:none}.brand:hover{text-decoration:none}")
	b.WriteString(identityMenuCSS)
	b.WriteString("main{max-width:820px;margin:0 auto;padding:2rem}")
	b.WriteString("h1{font-size:1.2rem;margin-bottom:1rem;color:rgba(255,253,248,.88)}a{color:#ff8a50;text-decoration:none}a:hover{text-decoration:underline}")
	b.WriteString("ul{list-style:none}li{padding:0.35rem 0;border-bottom:1px solid #2f2a3c}.entry{display:block;padding:.35rem 0;touch-action:manipulation}.dir{color:#a880ff}.crumb{color:rgba(238,233,245,.6);margin-bottom:1.5rem;font-size:0.9rem}.ios-install{display:none;margin:0 0 1.5rem;padding:.85rem 1rem;border:1px solid #363046;border-radius:8px;background:#252130;color:rgba(255,253,248,.88);font-size:.9rem;line-height:1.45}.ios-install strong{display:block;margin-bottom:.2rem;color:#fffdf8}.share-icon{display:inline-block;color:#ff8a50;font-size:1.15rem;line-height:1;vertical-align:-.08rem}.action-overlay{position:fixed;inset:0;z-index:10;display:grid;place-items:center;padding:1.25rem;background:rgba(0,0,0,.62)}.action-overlay[hidden]{display:none}.action-sheet{width:min(22rem,100%);padding:1rem;border:1px solid #363046;border-radius:12px;background:#252130;box-shadow:0 16px 48px rgba(0,0,0,.45)}.action-name{overflow:hidden;margin-bottom:.25rem;color:#fffdf8;font-weight:600;text-overflow:ellipsis;white-space:nowrap}.action-hint{margin-bottom:1rem;color:rgba(238,233,245,.6);font-size:.8rem}.action-buttons{display:grid;grid-template-columns:repeat(3,1fr);gap:.65rem}.action-buttons button{min-height:2.75rem;border:1px solid #363046;border-radius:8px;background:#2f2a3c;color:#fffdf8;font:inherit;cursor:pointer}.action-buttons button:active{background:#363046}.action-subtext{display:block;margin-top:.15rem;color:rgba(238,233,245,.6);font-size:.7rem}.copy-status{min-height:1.2rem;margin-top:.65rem;color:#3dd68c;font-size:.8rem;text-align:center}</style></head><body>")

	fmt.Fprintf(&b, `<header class="topbar"><a class="brand" href="%s/">📜 OpenLore</a>%s</header><main>`, html.EscapeString(strings.TrimRight(lorePath, "/")), identityMenuHTML(identity))
	fmt.Fprintf(&b, "<h1>%s</h1>", html.EscapeString(fsPath))
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
	b.WriteString(`</ul><div class="action-overlay" id="entry-actions" hidden role="dialog" aria-modal="true" aria-labelledby="action-name"><div class="action-sheet"><div class="action-name" id="action-name"></div><div class="action-hint">Double tap to open</div><div class="action-buttons"><button type="button" data-open>Open</button><button type="button" data-copy="url">Copy URL</button><button type="button" data-copy="path">Copy path<span class="action-subtext">Copy for agent</span></button></div><div class="copy-status" id="copy-status" aria-live="polite"></div></div></div>`)
	b.WriteString(`<script>(()=>{const overlay=document.getElementById('entry-actions');const name=document.getElementById('action-name');const status=document.getElementById('copy-status');let selected=null;let tapTimer=null;function hide(){overlay.hidden=true;status.textContent='';selected=null}function show(link){selected=link;name.textContent=link.textContent;status.textContent='';overlay.hidden=false}async function copy(text,label){try{if(navigator.clipboard&&navigator.clipboard.writeText){await navigator.clipboard.writeText(text)}else{const area=document.createElement('textarea');area.value=text;area.style.position='fixed';area.style.opacity='0';document.body.appendChild(area);area.select();if(!document.execCommand('copy'))throw new Error('copy failed');area.remove()}status.textContent=label+' copied'}catch{status.textContent='Could not copy'}}document.querySelectorAll('.entry').forEach(link=>link.addEventListener('click',event=>{if(event.detail===0)return;event.preventDefault();if(tapTimer&&selected===link){clearTimeout(tapTimer);tapTimer=null;window.location.assign(link.href);return}if(tapTimer)clearTimeout(tapTimer);selected=link;tapTimer=setTimeout(()=>{tapTimer=null;show(link)},275)}));overlay.addEventListener('click',event=>{if(event.target===overlay)hide()});overlay.querySelector('[data-open]').addEventListener('click',()=>{if(selected)window.location.assign(selected.href)});overlay.querySelectorAll('[data-copy]').forEach(button=>button.addEventListener('click',()=>{if(!selected)return;const kind=button.dataset.copy;copy(kind==='url'?selected.href:selected.dataset.path,kind==='url'?'URL':'Path')}));document.addEventListener('keydown',event=>{if(event.key==='Escape'&&!overlay.hidden)hide()})})()</script></main>` + identityMenuScript + `</body></html>`)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(b.String()))
}

func pwaHead(lorePath string) string {
	manifestURL := html.EscapeString(lorePath + "/manifest.webmanifest")
	iconURL := html.EscapeString(lorePath + "/app-icon.svg")
	serviceWorkerURL, _ := json.Marshal(lorePath + "/service-worker.js")
	return fmt.Sprintf(`<link rel="manifest" href="%s"><link rel="apple-touch-icon" href="%s"><meta name="theme-color" content="#1d1a23"><meta name="apple-mobile-web-app-capable" content="yes"><meta name="apple-mobile-web-app-status-bar-style" content="black-translucent"><meta name="apple-mobile-web-app-title" content="OpenLore"><script>if('serviceWorker'in navigator)window.addEventListener('load',()=>navigator.serviceWorker.register(%s))</script>`, manifestURL, iconURL, serviceWorkerURL)
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
			"background_color": "#1d1a23",
			"theme_color":      "#1d1a23",
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
		fmt.Fprint(w, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512"><rect width="512" height="512" rx="96" fill="#1d1a23"/><path d="M145 104h222v304H145z" fill="#fffdf8"/><path d="M181 164h150M181 218h150M181 272h112M181 326h132" stroke="#1d1a23" stroke-width="24" stroke-linecap="round"/><path d="M122 81h222v304" fill="none" stroke="#ff8a50" stroke-width="26" stroke-linecap="round" stroke-linejoin="round"/></svg>`)
		return true
	default:
		return false
	}
}
