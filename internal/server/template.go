package server

// The page, as an html/template. Everything dynamic is escaped by the
// template; the one safe-marked value is the highlighted snippet (page.go).
const pageHTML = `{{define "search"}}<form class="search" action="/" method="get" role="search">
<label class="sr" for="q">Search {{num .Docs}} documents</label>
<input id="q" type="search" name="q" value="{{.Query}}" placeholder="Search {{num .Docs}} documents" autocomplete="off" spellcheck="false"{{if .Home}} autofocus{{end}}>
<button type="submit">Search</button>
</form>{{end}}<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<meta name="color-scheme" content="light dark">
<meta name="description" content="A search engine written from scratch in Go: an inverted index, BM25 ranking and a crawler, searching the Go blog and docs.">
<title>{{.Title}}</title>
<link rel="icon" href="data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' viewBox='0 0 32 32'%3E%3Crect width='32' height='32' rx='8' fill='%230e2330'/%3E%3Ccircle cx='16' cy='16' r='9' fill='%23f0b429' opacity='.25'/%3E%3Ccircle cx='16' cy='16' r='5' fill='%23f0b429'/%3E%3C/svg%3E">
<style>{{.Style}}</style>
</head>
<body class="{{if .Home}}home{{else}}results{{end}}">
{{if .Home}}
<main class="intro">
  <h1 class="word">lumen</h1>
  <p class="tag">A search engine written from scratch in Go. Ask it about the Go blog and docs.</p>
  {{template "search" .}}
  <p class="try"><span>Try</span>{{range .Examples}}<a href="/?q={{q .Query}}">{{.Query}}</a>{{end}}</p>

  <section class="index" aria-label="The index">
    <div><b>{{num .Docs}}</b><span>documents</span></div>
    <div><b>{{num .Terms}}</b><span>distinct terms</span></div>
    <div><b>{{num .Postings}}</b><span>postings</span></div>
    <p>Built in memory by this server from pages it crawled itself. No query leaves it.{{if .Freshness}} {{.Freshness}}{{end}}</p>
  </section>

  <section class="how" aria-labelledby="how-h">
    <h2 id="how-h">How it works</h2>
    <ol>{{range .Steps}}<li><h3>{{.Name}}</h3><p>{{.Text}}</p></li>{{end}}</ol>
  </section>

  <section class="syntax" aria-labelledby="syn-h">
    <h2 id="syn-h">Query syntax</h2>
    <dl>{{range .Syntax}}<div><dt><a href="/?q={{q .Query}}"><code>{{.Query}}</code></a></dt><dd>{{.Note}}</dd></div>{{end}}</dl>
  </section>
</main>
{{else}}
<header class="bar">
  <a class="brand" href="/">lumen</a>
  {{template "search" .}}
</header>
<main class="wrap">
{{if .Error}}
  <p class="error" role="alert">{{.Error}}</p>
{{else if .Empty}}
  <section class="empty">
    <h2>Nothing matched <q>{{.Query}}</q></h2>
    <p>Try fewer words or another spelling. Words are stemmed, so <em>running</em> already finds <em>runs</em>. Or start from one of these:</p>
    <p class="try">{{range .Examples}}<a href="/?q={{q .Query}}">{{.Query}}</a>{{end}}</p>
  </section>
{{else}}
  <p class="meta"><b>{{num .Total}}</b> result{{if ne .Total 1}}s{{end}} in {{.Took}} ms{{if .ShowParsed}} · read as <code>{{.Parsed}}</code>{{end}}</p>
  {{if .Legend}}<ul class="legend" aria-label="What each colour means">{{range .Legend}}<li class="t{{.Color}}"><i aria-hidden="true"></i>{{.Term}}<span>in {{num .Docs}} documents</span></li>{{end}}</ul>{{end}}

  <ol class="hits">
  {{range .Hits}}
    <li class="hit">
      <div class="crumbs">{{range $i, $p := .Path}}{{if $i}}<span aria-hidden="true">›</span>{{end}}<span>{{$p}}</span>{{end}}</div>
      <h2>{{if .Link}}<a href="{{.URI}}">{{.Title}}</a>{{else}}{{.Title}}{{end}}</h2>
      <p class="snippet">{{.Snippet}}</p>
      <div class="lamp" role="img" aria-label="Relevance score {{.Score}}">
        <div class="track">{{range .Bar}}<span class="seg {{if lt .Color 0}}cb{{else}}c{{.Color}}{{end}}" style="width:{{pct .Width}}"></span>{{end}}</div>
        <span class="num">{{.Score}}</span>
      </div>
      {{if .Rows}}<details class="why">
        <summary>Why this ranked here</summary>
        <table>
          <thead><tr><th scope="col">Term</th><th scope="col" class="n">In this page</th><th scope="col" class="n">In the index</th><th scope="col" class="n">Rarity</th><th scope="col" class="n">Adds</th></tr></thead>
          <tbody>{{range .Rows}}<tr><th scope="row" class="{{if lt .Color 0}}tb{{else}}t{{.Color}}{{end}}"><i aria-hidden="true"></i>{{.Term}}</th>{{if lt .Color 0}}<td class="n" colspan="3"></td>{{else}}<td class="n">{{.TF}}×</td><td class="n">{{num .DF}} docs</td><td class="n">{{.IDF}}</td>{{end}}<td class="n">+{{.Adds}}</td></tr>{{end}}</tbody>
        </table>
        <p>Okapi BM25: a word counts for more when it is rare in the index, and for less once it has appeared often in a page. The bar above is these numbers added up, scaled to the best result on this page.</p>
      </details>{{end}}
      {{if .Also}}<p class="also">The same page is also at {{range $i, $a := .Also}}{{if $i}}, {{end}}<a href="{{$a.URI}}">{{$a.Path}}</a>{{end}}.</p>{{end}}
    </li>
  {{end}}
  </ol>

  {{if .Pages}}<nav class="pager" aria-label="Pages">
    {{if .Prev}}<a href="{{.Prev}}" rel="prev">← Previous</a>{{else}}<span class="off">← Previous</span>{{end}}
    <ol>{{range .Pages}}{{if .Gap}}<li aria-hidden="true">…</li>{{else if .Current}}<li><span aria-current="page">{{.N}}</span></li>{{else}}<li><a href="{{.Href}}">{{.N}}</a></li>{{end}}{{end}}</ol>
    {{if .Next}}<a href="{{.Next}}" rel="next">Next →</a>{{else}}<span class="off">Next →</span>{{end}}
  </nav>{{end}}
{{end}}
</main>
{{end}}
<footer>
  <span>lumen · BM25 search in Go, standard library only</span>
  <span><a href="https://github.com/Nafiz001/lumen">Code</a> · <a href="/api/stats">API</a></span>
</footer>
<script>
addEventListener("keydown", function (e) {
  var t = document.activeElement && document.activeElement.tagName;
  if (e.key === "/" && t !== "INPUT" && t !== "TEXTAREA") {
    e.preventDefault();
    var box = document.getElementById("q");
    if (box) { box.focus(); box.select(); }
  }
});
</script>
</body>
</html>
`
