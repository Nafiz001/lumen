package server

// The stylesheet is inlined into the page rather than served as a separate
// asset. It costs a few kilobytes per page load and removes a round trip, a
// route, a cache-header decision and an embedded filesystem: a good trade at
// this size.
//
// The look follows the name. A lumen is a unit of light, so relevance is drawn
// as light: each result has a bar lit by the terms that matched, one colour
// per term, sized by what that term added to the score. Cool paper and deep
// ink-blue carry the text; Go's cyan marks what is clickable; the warm lamp
// gold is used for light only (the highlight and the wordmark's glow).
const pageStyle = `
:root {
  --bg: #f4f6f6;
  --panel: #ffffff;
  --ink: #0e2330;
  --muted: #566871;
  --line: #d8e0e2;
  --accent: #00758f;
  --lamp: #f0b429;
  --mark: rgba(240, 180, 41, .38);
  --t0: #00758f;
  --t1: #b26a00;
  --t2: #6a4bc4;
  --t3: #2c7d52;
  --tb: #8a98a0;
  --shadow: 0 1px 2px rgba(14, 35, 48, .06), 0 8px 24px -12px rgba(14, 35, 48, .18);
  --serif: "Iowan Old Style", "Palatino Linotype", Palatino, Charter, Georgia, serif;
  --sans: system-ui, -apple-system, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
  --mono: ui-monospace, "SF Mono", "Cascadia Mono", Consolas, "Liberation Mono", monospace;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #09141c;
    --panel: #0f1d28;
    --ink: #e5edf1;
    --muted: #8fa3ae;
    --line: #213644;
    --accent: #4cc9e8;
    --lamp: #f5c04a;
    --mark: rgba(245, 192, 74, .30);
    --t0: #4cc9e8;
    --t1: #f5c04a;
    --t2: #a99bf0;
    --t3: #5fd39b;
    --tb: #607784;
    --shadow: 0 1px 2px rgba(0, 0, 0, .4), 0 12px 28px -14px rgba(0, 0, 0, .7);
  }
}
* { box-sizing: border-box; }
html { -webkit-text-size-adjust: 100%; }
body {
  margin: 0;
  min-height: 100vh;
  display: flex;
  flex-direction: column;
  background: var(--bg);
  color: var(--ink);
  font: 16px/1.6 var(--sans);
}
main { flex: 1; }
a { color: var(--accent); text-underline-offset: 3px; }
code { font: .86em var(--mono); background: color-mix(in srgb, var(--ink) 7%, transparent); padding: .1em .4em; border-radius: 4px; }
.sr { position: absolute; width: 1px; height: 1px; overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; }
:focus-visible { outline: 2px solid var(--accent); outline-offset: 2px; }

/* the search box, on both pages */
.search { display: flex; gap: 8px; width: 100%; }
.search input {
  flex: 1;
  min-width: 0;
  font: inherit;
  color: var(--ink);
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 12px;
  padding: .62em .95em;
  box-shadow: var(--shadow);
}
.search input::placeholder { color: var(--muted); }
.search input:focus { outline: none; border-color: var(--accent); box-shadow: 0 0 0 3px color-mix(in srgb, var(--accent) 24%, transparent), var(--shadow); }
.search button {
  font: inherit;
  font-weight: 600;
  color: #fff;
  background: var(--accent);
  border: 0;
  border-radius: 12px;
  padding: 0 1.2em;
  cursor: pointer;
}
@media (prefers-color-scheme: dark) { .search button { color: #06232c; } }
.search button:hover { filter: brightness(1.08); }

footer {
  display: flex;
  justify-content: space-between;
  gap: 12px;
  flex-wrap: wrap;
  padding: 18px max(20px, calc((100vw - 64rem) / 2 + 20px));
  border-top: 1px solid var(--line);
  color: var(--muted);
  font-size: 13px;
}
footer a { color: var(--muted); }

/* ---- the home page ---- */
.intro { width: 100%; max-width: 64rem; margin: 0 auto; padding: clamp(40px, 11vh, 120px) 20px 40px; }
.word {
  margin: 0;
  text-align: center;
  font: 400 clamp(4.5rem, 16vw, 8.5rem)/1 var(--serif);
  letter-spacing: -.03em;
  text-shadow: 0 0 .35em color-mix(in srgb, var(--lamp) 38%, transparent);
}
.tag { margin: 14px auto 28px; text-align: center; max-width: 36rem; color: var(--muted); font-size: 1.1rem; text-wrap: balance; }
.intro .search { max-width: 40rem; margin: 0 auto; }
.intro .search input { font-size: 1.15rem; padding: .85em 1.1em; border-radius: 16px; }
.intro .search button { border-radius: 16px; }
.try { display: flex; flex-wrap: wrap; justify-content: center; align-items: baseline; gap: 8px 10px; margin: 18px 0 0; font-size: .92rem; color: var(--muted); }
.try a {
  font: .9rem var(--mono);
  text-decoration: none;
  color: var(--ink);
  background: var(--panel);
  border: 1px solid var(--line);
  border-radius: 999px;
  padding: .2em .8em;
}
.try a:hover { border-color: var(--accent); color: var(--accent); }

.index {
  display: grid;
  grid-template-columns: repeat(3, minmax(0, 1fr));
  gap: 0 24px;
  margin: 72px 0 0;
  padding: 28px 0;
  border-block: 1px solid var(--line);
}
.index div { display: flex; flex-direction: column; }
.index b { font: 400 clamp(2rem, 5vw, 3rem)/1.1 var(--serif); font-variant-numeric: tabular-nums; }
.index span { color: var(--muted); font-size: .9rem; }
.index p { grid-column: 1 / -1; margin: 18px 0 0; color: var(--muted); font-size: .92rem; }

.how, .syntax { margin-top: 56px; }
.how h2, .syntax h2 { font: 400 1.5rem/1.2 var(--serif); margin: 0 0 18px; }
.how ol { list-style: none; margin: 0; padding: 0; display: grid; grid-template-columns: repeat(4, minmax(0, 1fr)); gap: 24px; counter-reset: step; }
.how li { counter-increment: step; border-top: 2px solid var(--line); padding-top: 14px; }
.how li:nth-child(1) { border-color: var(--t0); }
.how li:nth-child(2) { border-color: var(--t1); }
.how li:nth-child(3) { border-color: var(--t2); }
.how li:nth-child(4) { border-color: var(--t3); }
.how h3 { margin: 0 0 6px; font-size: 1rem; }
.how h3::before { content: counter(step) "  "; color: var(--muted); font: .85rem var(--mono); }
.how p { margin: 0; color: var(--muted); font-size: .94rem; }
.syntax dl { margin: 0; display: grid; grid-template-columns: repeat(auto-fill, minmax(17rem, 1fr)); gap: 14px 28px; }
.syntax dl div { display: flex; flex-direction: column; gap: 2px; }
.syntax dt { margin: 0; }
.syntax dt a { text-decoration: none; }
.syntax dt code { font-size: .95rem; padding: .2em .55em; }
.syntax dd { margin: 0; color: var(--muted); font-size: .92rem; }

/* ---- the results page ---- */
.bar {
  position: sticky;
  top: 0;
  z-index: 2;
  display: flex;
  align-items: center;
  gap: 18px;
  padding: 12px max(20px, calc((100vw - 52rem) / 2));
  background: color-mix(in srgb, var(--bg) 88%, transparent);
  backdrop-filter: blur(10px);
  border-bottom: 1px solid var(--line);
}
.brand {
  font: 400 1.9rem/1 var(--serif);
  letter-spacing: -.02em;
  color: var(--ink);
  text-decoration: none;
  text-shadow: 0 0 .6em color-mix(in srgb, var(--lamp) 50%, transparent);
}
.bar .search { max-width: 36rem; }
.wrap { width: 100%; max-width: 52rem; margin: 0 auto; padding: 22px 20px 56px; }
.meta { margin: 0 0 10px; color: var(--muted); font-size: .92rem; }
.meta b { color: var(--ink); font-weight: 600; }
.legend { list-style: none; margin: 0 0 22px; padding: 0; display: flex; flex-wrap: wrap; gap: 6px 10px; }
.legend li { display: inline-flex; align-items: center; gap: 7px; font: .85rem var(--mono); border: 1px solid var(--line); border-radius: 999px; padding: .15em .75em .15em .6em; background: var(--panel); }
.legend span { color: var(--muted); font-family: var(--sans); font-size: .8rem; }
i { display: inline-block; width: .7em; height: .7em; border-radius: 50%; background: currentColor; flex: none; }
.t0 { color: var(--t0); } .t1 { color: var(--t1); } .t2 { color: var(--t2); } .t3 { color: var(--t3); } .tb { color: var(--tb); }
.legend li { color: var(--ink); }
.legend li i { color: inherit; }
.legend .t0 i { background: var(--t0); } .legend .t1 i { background: var(--t1); } .legend .t2 i { background: var(--t2); } .legend .t3 i { background: var(--t3); }

.hits { list-style: none; margin: 0; padding: 0; }
.hit { padding: 20px 0 22px; border-top: 1px solid var(--line); }
.hit:first-child { border-top: 0; padding-top: 4px; }
.crumbs { display: flex; flex-wrap: wrap; align-items: center; gap: 2px 8px; font: .8rem var(--mono); color: var(--muted); overflow-wrap: anywhere; }
.crumbs span:first-child { color: var(--ink); }
.hit h2 { margin: 3px 0 6px; font: 400 1.4rem/1.3 var(--serif); letter-spacing: -.005em; }
.hit h2 a { color: var(--ink); text-decoration-color: color-mix(in srgb, var(--accent) 55%, transparent); text-decoration-thickness: 1.5px; }
.hit h2 a:hover { color: var(--accent); }
.snippet { margin: 0 0 12px; color: color-mix(in srgb, var(--ink) 86%, var(--muted)); overflow-wrap: anywhere; }
mark { background: var(--mark); color: inherit; padding: .02em .12em; border-radius: 3px; box-shadow: 0 0 .8em color-mix(in srgb, var(--lamp) 40%, transparent); }

.lamp { display: flex; align-items: center; gap: 12px; }
.track { flex: 1; max-width: 22rem; height: 7px; display: flex; border-radius: 999px; overflow: hidden; background: color-mix(in srgb, var(--ink) 9%, transparent); }
.seg { height: 100%; }
.seg + .seg { border-left: 2px solid var(--bg); }
.c0 { background: linear-gradient(90deg, color-mix(in srgb, var(--t0) 55%, transparent), var(--t0)); }
.c1 { background: linear-gradient(90deg, color-mix(in srgb, var(--t1) 55%, transparent), var(--t1)); }
.c2 { background: linear-gradient(90deg, color-mix(in srgb, var(--t2) 55%, transparent), var(--t2)); }
.c3 { background: linear-gradient(90deg, color-mix(in srgb, var(--t3) 55%, transparent), var(--t3)); }
.cb { background: var(--tb); }
.num { font: .82rem var(--mono); color: var(--muted); font-variant-numeric: tabular-nums; }

.why { margin-top: 10px; font-size: .92rem; }
.why summary { cursor: pointer; color: var(--accent); width: fit-content; }
.why[open] summary { margin-bottom: 8px; }
.why table { width: 100%; border-collapse: collapse; margin: 4px 0 8px; font-size: .88rem; background: var(--panel); border: 1px solid var(--line); border-radius: 10px; overflow: hidden; }
.why th, .why td { padding: 7px 12px; border-bottom: 1px solid var(--line); text-align: left; font-weight: 400; }
.why thead th { color: var(--muted); font-size: .78rem; }
.why tbody tr:last-child th, .why tbody tr:last-child td { border-bottom: 0; }
.why tbody th { font: .86rem var(--mono); }
.why tbody th i { margin-right: 8px; }
.why .t0 i { background: var(--t0); } .why .t1 i { background: var(--t1); } .why .t2 i { background: var(--t2); } .why .t3 i { background: var(--t3); } .why .tb i { background: var(--tb); }
.why tbody th { color: var(--ink); }
.n { text-align: right !important; font-variant-numeric: tabular-nums; font-family: var(--mono); }
.why p { margin: 0; color: var(--muted); font-size: .86rem; max-width: 42rem; }
.also { margin: 10px 0 0; font-size: .85rem; color: var(--muted); overflow-wrap: anywhere; }

.pager { display: flex; align-items: center; justify-content: space-between; gap: 12px; margin-top: 28px; padding-top: 22px; border-top: 1px solid var(--line); font-size: .95rem; }
.pager ol { list-style: none; display: flex; gap: 2px; margin: 0; padding: 0; }
.pager li { min-width: 2.2em; text-align: center; color: var(--muted); }
.pager li a, .pager li span[aria-current] { display: block; padding: .15em .5em; border-radius: 8px; text-decoration: none; }
.pager li span[aria-current] { background: var(--ink); color: var(--bg); font-weight: 600; }
.pager li a:hover { background: color-mix(in srgb, var(--accent) 14%, transparent); }
.pager .off { color: var(--muted); opacity: .5; }

.empty h2 { font: 400 1.6rem/1.3 var(--serif); margin: 8px 0 10px; }
.empty p { color: var(--muted); max-width: 38rem; }
.empty .try { justify-content: flex-start; }
.error { padding: 12px 14px; border-left: 3px solid #c0392b; background: var(--panel); }

@media (max-width: 40rem) {
  .bar { flex-wrap: wrap; gap: 8px 14px; }
  .bar .search { flex-basis: 100%; max-width: none; order: 3; }
  .index { grid-template-columns: 1fr 1fr 1fr; gap: 0 10px; }
  .how ol { grid-template-columns: 1fr 1fr; }
  .pager ol { display: none; }
  .hit h2 { font-size: 1.25rem; }
  .why table { font-size: .8rem; }
  .why th, .why td { padding: 6px 7px; }
}
@media (prefers-reduced-motion: no-preference) {
  .search input, .try a, .pager li a { transition: border-color .15s, box-shadow .15s, color .15s, background .15s; }
}
`
