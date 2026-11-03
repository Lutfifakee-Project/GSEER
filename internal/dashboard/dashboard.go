// GSEER — Internet Asset Intelligence & Search Engine.
// Copyright (C) 2026  The GSEER Authors
//
// This program is free software: you can redistribute it and/or modify
// it under the terms of the GNU Affero General Public License as published
// by the Free Software Foundation, either version 3 of the License, or
// (at your option) any later version.
//
// This program is distributed in the hope that it will be useful,
// but WITHOUT ANY WARRANTY; without even the implied warranty of
// MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
// GNU Affero General Public License for more details.
//
// You should have received a copy of the GNU Affero General Public License
// along with this program.  If not, see <https://www.gnu.org/licenses/>.

// Package dashboard is the server-rendered web UI (AC-4.9 third
// surface).  It renders a search form and result table from the SAME
// SearchService the CLI and REST API use — GQL semantics, pagination,
// and freshness notices are identical across all three surfaces.
//
// The dashboard is read-only: it never triggers scanning (ADR-0010) and
// imports only internal/search (AC-3.1 import-graph test).
package dashboard

import (
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Lutfifakee-Project/GSEER/internal/httpx"
	"github.com/Lutfifakee-Project/GSEER/internal/search"
)

// Dashboard serves the web UI over the shared SearchService.
type Dashboard struct {
	svc *search.Service
	tpl *template.Template
}

// New builds the Dashboard.
func New(svc *search.Service) *Dashboard {
	return &Dashboard{
		svc: svc,
		tpl: template.Must(template.New("page").Funcs(template.FuncMap{"fmtTime": fmtTime}).Parse(pageTpl)),
	}
}

// Handler returns the dashboard's http.Handler (mounted at "/" by the
// server command).  Hardening (M16 security review):
//   - security response headers on every response (nosniff, DENY
//     framing, no-referrer, a scriptless CSP);
//   - GET-only serving: the dashboard has no state-changing endpoint and
//     no cookies, so there is no CSRF surface by construction — the
//     method guard pins that invariant instead of trusting future code
//     to keep it (405 for anything but GET/HEAD).
func (d *Dashboard) Handler() http.Handler {
	return httpx.SecurityHeaders(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		d.handleIndex(w, r)
	}))
}

type pageData struct {
	Query      string
	Mode       string
	Limit      int
	Notice     string
	Error      string
	Rows       []search.Row
	Count      int
	HasMore    bool
	NextCursor string
	PrevCursor string
	TookMs     int64
	Searched   bool
	// Facets (v0.1.0): aggregates over the same filtered rows;
	// informational on the dashboard (facet values are not query links —
	// building compound GQL from arbitrary values would need per-type
	// quoting; the API/CLI are the interactive surfaces).
	Facets []search.Facet
}

func (d *Dashboard) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	data := pageData{
		Query: strings.TrimSpace(r.URL.Query().Get("q")),
		Mode:  r.URL.Query().Get("mode"),
	}
	if data.Mode == "" {
		data.Mode = "service"
	}
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 1000 {
			data.Limit = n
		}
	}
	if data.Limit == 0 {
		data.Limit = 50
	}
	cursor := r.URL.Query().Get("cursor")

	if data.Query != "" {
		res, err := d.svc.Search(r.Context(), search.Query{
			GQL:       data.Query,
			Mode:      data.Mode,
			Limit:     data.Limit,
			Cursor:    cursor,
			WithStats: true,
		})
		if err != nil {
			data.Error = err.Error()
		} else {
			data.Rows = res.Rows
			data.Count = res.Query.RowCount
			data.HasMore = res.HasMore
			data.NextCursor = res.NextCursor
			data.TookMs = res.Query.TookMs
			data.Notice = res.Notice
			data.Searched = true
			// Facets ride on the SAME query through the SAME service; a
			// facets failure never fails the search itself.
			if fres, ferr := d.svc.Facets(r.Context(), search.FacetsQuery{GQL: data.Query, Mode: data.Mode, MaxValues: 8}); ferr == nil {
				data.Facets = fres.Facets
			}
			// "Previous page" = re-run from the start up to the last
			// page's offset is not expressible with a keyset cursor;
			// the honest v0.0.1 affordance is "back to first page".
			if cursor != "" {
				data.PrevCursor = "-"
			}
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := d.tpl.Execute(w, data); err != nil {
		// Headers are already sent; nothing actionable — log-shaped note.
		_, _ = w.Write([]byte("<!-- render error -->"))
	}
}

// fmtTime renders timestamps in the table (UTC, second precision).
func fmtTime(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

// pageTpl is the whole v0.0.1 dashboard: a search box, the freshness
// notice, and the result table.  Everything is html/template-escaped.
var pageTpl = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>GSEER — Search</title>
<style>
  body { font-family: system-ui, sans-serif; margin: 2rem; color: #1a202c; }
  h1 { font-size: 1.4rem; } .tagline { color: #718096; margin-top: -0.5rem; }
  form.search { margin: 1.5rem 0; display: flex; gap: .5rem; flex-wrap: wrap; }
  form.search input[name=q] { flex: 1 1 24rem; padding: .5rem .75rem; font-size: 1rem; }
  form.search select, form.search button { padding: .5rem .75rem; font-size: 1rem; }
  .notice { padding: .75rem 1rem; border-radius: .375rem; margin: 1rem 0; }
  .notice.warn { background: #fefcbf; border: 1px solid #d69e2e; }
  .notice.empty { background: #ebf8ff; border: 1px solid #4299e1; }
  .error { background: #fff5f5; border: 1px solid #c53030; padding: .75rem 1rem; border-radius: .375rem; white-space: pre-wrap; }
  table { border-collapse: collapse; width: 100%; font-size: .9rem; }
  th, td { text-align: left; padding: .375rem .5rem; border-bottom: 1px solid #e2e8f0; }
  th { background: #f7fafc; }
  .count { color: #4a5568; margin: .75rem 0; }
  .facets { margin: .75rem 0; font-size: .85rem; }
  .facet { margin: .25rem 0; }
  .facet-name { display: inline-block; min-width: 7rem; color: #2c5282; font-weight: 600; }
  .facet-value { display: inline-block; background: #edf2f7; border-radius: .25rem; padding: .1rem .4rem; margin-right: .35rem; }
  .facet-value em { color: #718096; font-style: normal; }
  .pager { margin: 1rem 0; } .pager a { margin-right: 1rem; }
  code { background: #edf2f7; padding: 0 .25rem; }
</style>
</head>
<body>
<h1>GSEER — Internet Asset Intelligence &amp; Search</h1>
<p class="tagline">Discover. Identify. Search. — self-hosted index</p>

<form class="search" method="get" action="/">
  <input type="text" name="q" value="{{.Query}}" placeholder='GQL, e.g. port==443 &amp;&amp; title CONTAINS "login"' autofocus>
  <select name="mode">
    <option value="service" {{if eq .Mode "service"}}selected{{end}}>service mode</option>
    <option value="asset" {{if eq .Mode "asset"}}selected{{end}}>asset mode</option>
  </select>
  <button type="submit">Search</button>
</form>

{{with .Error}}<div class="error">{{.}}</div>{{end}}
{{with .Notice}}<div class="notice warn">{{.}}</div>{{end}}

{{if .Searched}}
<p class="count">{{.Count}} result(s) in {{.TookMs}} ms — query: <code>{{.Query}}</code></p>
{{if .Facets}}
<div class="facets">
  {{range .Facets}}{{if .Values}}
  <div class="facet"><span class="facet-name">{{.Name}}</span>
    {{range .Values}}<span class="facet-value">{{.Value}} <em>({{.Count}})</em></span>{{end}}
  </div>
  {{end}}{{end}}
</div>
{{end}}
{{if .Rows}}
<table>
  <tr><th>Asset</th><th>Kind</th><th>Port</th><th>Protocol</th><th>Product</th><th>HTTP</th><th>Title</th><th>Last seen</th><th>Row</th></tr>
  {{range .Rows}}
  <tr>
    <td>{{or .AssetValue "—"}}</td>
    <td>{{.AssetKind}}</td>
    <td>{{if .Port}}{{.Port}}{{else}}—{{end}}</td>
    <td>{{or .Protocol "—"}}</td>
    <td>{{or .Product "—"}}</td>
    <td>{{if .HTTPStatus}}{{.HTTPStatus}}{{else}}—{{end}}</td>
    <td>{{or .HTTPTitle "—"}}</td>
    <td>{{fmtTime .LastSeen}}</td>
    <td>{{.RowKind}}</td>
  </tr>
  {{end}}
</table>
<div class="pager">
  {{if .HasMore}}<a href="/?q={{urlquery .Query}}&amp;mode={{.Mode}}&amp;limit={{.Limit}}&amp;cursor={{urlquery .NextCursor}}">Next page →</a>{{end}}
  {{if .PrevCursor}}<a href="/?q={{urlquery .Query}}&amp;mode={{.Mode}}&amp;limit={{.Limit}}">↑ First page</a>{{end}}
</div>
{{else if not .Error}}
<p class="notice empty">No results for this query.</p>
{{end}}
{{end}}

<p style="color:#a0aec0;font-size:.8rem;margin-top:3rem">GSEER — searches run against the local index only; search never scans.</p>
</body>
</html>`
