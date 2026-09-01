package server

// The stylesheet is inlined into the page rather than served as a separate
// asset. It costs one extra kilobyte per page load and removes a round trip, a
// route, a cache-header decision and an embedded filesystem -- a good trade at
// this size, and a bad one past a few kilobytes.
const pageStyle = `<style>
:root {
  --bg: #ffffff;
  --fg: #1a1a1a;
  --muted: #666666;
  --line: #e3e3e3;
  --link: #1a56b8;
  --mark: #fff2a8;
  --code-bg: #f4f4f5;
}
@media (prefers-color-scheme: dark) {
  :root {
    --bg: #16181c;
    --fg: #e6e6e6;
    --muted: #999999;
    --line: #2c2f36;
    --link: #7aa7ff;
    --mark: #6b5a12;
    --code-bg: #22252b;
  }
}
* { box-sizing: border-box; }
body {
  margin: 0;
  background: var(--bg);
  color: var(--fg);
  font: 15px/1.6 -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif;
}
header {
  display: flex;
  align-items: center;
  gap: 20px;
  padding: 18px 28px;
  border-bottom: 1px solid var(--line);
  flex-wrap: wrap;
}
.brand {
  font-size: 20px;
  font-weight: 600;
  letter-spacing: -0.02em;
  color: var(--fg);
  text-decoration: none;
}
form[role="search"] {
  display: flex;
  flex: 1;
  min-width: 260px;
  gap: 8px;
}
input[type="search"] {
  flex: 1;
  padding: 9px 13px;
  font-size: 15px;
  color: var(--fg);
  background: var(--bg);
  border: 1px solid var(--line);
  border-radius: 6px;
}
input[type="search"]:focus {
  outline: none;
  border-color: var(--link);
}
button {
  padding: 9px 18px;
  font-size: 15px;
  color: #fff;
  background: var(--link);
  border: 0;
  border-radius: 6px;
  cursor: pointer;
}
main {
  max-width: 760px;
  padding: 24px 28px 60px;
}
.meta, .hint, .empty { color: var(--muted); }
.meta { font-size: 13px; margin: 0 0 20px; }
.error {
  padding: 12px 14px;
  border-left: 3px solid #c0392b;
  background: var(--code-bg);
}
ol.results { list-style: none; margin: 0; padding: 0; }
ol.results li { margin-bottom: 26px; }
ol.results h3 {
  margin: 0 0 2px;
  font-size: 17px;
  font-weight: 500;
  color: var(--link);
}
.uri {
  font-size: 12px;
  color: var(--muted);
  word-break: break-all;
  margin-bottom: 4px;
}
.snippet { margin: 0; }
mark {
  background: var(--mark);
  color: inherit;
  padding: 0 1px;
  border-radius: 2px;
}
.score { font-size: 11px; color: var(--muted); margin-top: 3px; }
nav.pages { display: flex; gap: 18px; margin-top: 30px; }
nav.pages a { color: var(--link); text-decoration: none; }
table.syntax { border-collapse: collapse; margin-top: 10px; }
table.syntax td { padding: 5px 18px 5px 0; vertical-align: top; }
table.syntax td:last-child { color: var(--muted); }
code {
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 13px;
  background: var(--code-bg);
  padding: 1px 5px;
  border-radius: 3px;
}
h2 { font-size: 15px; margin: 28px 0 0; }
footer {
  padding: 16px 28px;
  border-top: 1px solid var(--line);
  font-size: 12px;
  color: var(--muted);
}
footer a { color: var(--muted); }
</style>`
