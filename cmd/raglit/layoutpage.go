package main

import (
	"html/template"
	"io"
	"net/url"

	"github.com/iodesystems/raglit"
)

// The page that shows what the model SAW beside what the index KEPT.
//
// Server-rendered on purpose. The SPA needs a build step, and the thing this
// exists to show — that a page's raw transcription and its indexed text can
// differ by 40% of their bytes and nothing surfaced it — should be visible on
// any daemon that has the binary.
//
// Boxes are placed as PERCENTAGES because the model's coordinates are normalised
// to 0-1000 per axis independently. That means no image dimensions are needed,
// and the overlay stays aligned at any rendered width — including on a phone.
// Verified against a 2550x3300 scan: every box landed on its line.

var layoutTmpl = template.Must(template.New("layout").Funcs(template.FuncMap{
	"pctL": func(b raglit.LayoutBox) float64 { l, _, _, _ := b.Pct(); return l },
	"pctT": func(b raglit.LayoutBox) float64 { _, t, _, _ := b.Pct(); return t },
	"pctW": func(b raglit.LayoutBox) float64 { _, _, w, _ := b.Pct(); return w },
	"pctH": func(b raglit.LayoutBox) float64 { _, _, _, h := b.Pct(); return h },
	"qesc": func(s string) string { return url.QueryEscape(s) },
	"inc":  func(n int) int { return n + 1 },
	"dec":  func(n int) int { return n - 1 },
}).Parse(`<!doctype html><meta charset="utf-8">
<title>layout — {{.Doc}} p{{.Page}}</title>
<style>
 :root{--bg:#111;--fg:#ddd;--dim:#888;--line:#333;--hi:#e0a}
 *{box-sizing:border-box}
 body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.5 ui-sans-serif,system-ui,sans-serif}
 header{padding:12px 18px;border-bottom:1px solid var(--line);position:sticky;top:0;background:#181818;z-index:9}
 h1{font-size:14px;margin:0 0 4px;font-weight:600}
 .meta{color:var(--dim);font-size:12px}
 .meta b{color:#9c9;font-weight:600}
 .wrap{display:grid;grid-template-columns:minmax(320px,1fr) minmax(320px,1fr);gap:0;align-items:start}
 @media(max-width:900px){.wrap{grid-template-columns:1fr}}
 .imgpane{position:relative;padding:16px;border-right:1px solid var(--line)}
 .stage{position:relative;display:inline-block;width:100%}
 .stage img{width:100%;height:auto;display:block;background:#fff}
 .box{position:absolute;border:1.5px solid rgba(255,0,0,.55);cursor:pointer}
 .box:hover,.box.on{border-color:var(--hi);background:rgba(238,0,170,.16)}
 .box[data-label^="Section"]{border-color:rgba(0,160,255,.6)}
 .box[data-label="Image"]{border-color:rgba(0,200,120,.6)}
 .txtpane{padding:16px;min-width:0}
 .tabs{display:flex;gap:6px;margin-bottom:10px;flex-wrap:wrap}
 .tabs button{background:#222;color:#bbb;border:1px solid var(--line);border-radius:4px;padding:4px 10px;cursor:pointer;font:inherit;font-size:12px}
 .tabs button.on{background:#2c2c2c;color:#fff;border-color:#555}
 pre{white-space:pre-wrap;word-break:break-word;background:#161616;border:1px solid var(--line);border-radius:5px;padding:12px;margin:0;font:12px/1.6 ui-monospace,monospace;max-height:74vh;overflow:auto}
 .blocks{display:none;flex-direction:column;gap:6px;max-height:74vh;overflow:auto}
 .blk{border:1px solid var(--line);border-radius:5px;padding:7px 9px;background:#161616;cursor:pointer}
 .blk:hover,.blk.on{border-color:var(--hi);background:#1d161b}
 .blk .lab{color:#9c9;font-size:11px;text-transform:uppercase;letter-spacing:.4px}
 .blk .t{font-size:12.5px;color:#ccc}
 .none{color:var(--dim);font-style:italic;padding:10px 0}
 .nav a{color:#7ab;text-decoration:none;margin-right:10px}
</style>
<header>
 <h1>{{.Doc}} &nbsp;<span class="meta">page {{.Page}}</span></h1>
 <div class="meta">
   read by <b>{{if .Model}}{{.Model}}{{else}}{{.Engine}}{{end}}</b> ({{.Engine}}) ·
   <b>{{len .Boxes}}</b> layout block(s) ·
   raw <b>{{len .Raw}}</b> bytes vs indexed <b>{{len .Indexed}}</b> bytes
   {{if not .HasImage}} · <b>no page image saved</b>{{end}}
   <span class="nav" style="margin-left:12px">
     {{if gt .Page 1}}<a href="?index={{qesc $.Index}}&amp;path={{qesc .Doc}}&amp;page={{dec .Page}}">&larr; prev</a>{{end}}
     <a href="?index={{qesc $.Index}}&amp;path={{qesc .Doc}}&amp;page={{inc .Page}}">next &rarr;</a>
   </span>
 </div>
</header>
<div class="wrap">
 <div class="imgpane">
  {{if .HasImage}}
  <div class="stage" id="stage">
    <img src="/api/page-image?index={{qesc $.Index}}&amp;path={{qesc .Doc}}&amp;page={{.Page}}" alt="page {{.Page}}">
    {{range $i, $b := .Boxes}}<div class="box" data-i="{{$i}}" data-label="{{$b.Label}}"
      title="{{$b.Label}}"
      style="left:{{printf "%.3f" (pctL $b)}}%;top:{{printf "%.3f" (pctT $b)}}%;width:{{printf "%.3f" (pctW $b)}}%;height:{{printf "%.3f" (pctH $b)}}%"></div>{{end}}
  </div>
  {{else}}<div class="none">No page image was saved for this page.</div>{{end}}
 </div>
 <div class="txtpane">
  <div class="tabs">
    <button data-p="blocks" class="on">blocks ({{len .Boxes}})</button>
    <button data-p="indexed">indexed text</button>
    <button data-p="raw">raw transcription</button>
  </div>
  <div class="blocks" id="p-blocks" style="display:flex">
   {{if .Boxes}}{{range $i, $b := .Boxes}}<div class="blk" data-i="{{$i}}">
     <div class="lab">{{if $b.Label}}{{$b.Label}}{{else}}(unlabelled){{end}}</div>
     <div class="t">{{$b.Text}}</div></div>{{end}}
   {{else}}<div class="none">This page has no layout blocks — the engine that read it does not emit them.</div>{{end}}
  </div>
  <pre id="p-indexed" style="display:none">{{if .Indexed}}{{.Indexed}}{{else}}(no fragments begin on this page — a fragment starting on an earlier page may span it){{end}}</pre>
  <pre id="p-raw" style="display:none">{{if .Raw}}{{.Raw}}{{else}}(no cached transcription for this page image){{end}}</pre>
 </div>
</div>
<script>
 const panes={blocks:document.getElementById('p-blocks'),indexed:document.getElementById('p-indexed'),raw:document.getElementById('p-raw')};
 document.querySelectorAll('.tabs button').forEach(b=>b.onclick=()=>{
   document.querySelectorAll('.tabs button').forEach(x=>x.classList.toggle('on',x===b));
   for(const k in panes) panes[k].style.display = (k===b.dataset.p) ? (k==='blocks'?'flex':'block') : 'none';
 });
 // Hovering a box highlights its text and vice versa — the point of the view is
 // that you can tell WHICH words came from WHERE.
 const link=(a,b)=>a.forEach(el=>{
   const i=el.dataset.i, mate=b.find(x=>x.dataset.i===i);
   el.onmouseenter=()=>{el.classList.add('on'); mate&&mate.classList.add('on');};
   el.onmouseleave=()=>{el.classList.remove('on'); mate&&mate.classList.remove('on');};
   el.onclick=()=>mate&&mate.scrollIntoView({block:'nearest',behavior:'smooth'});
 });
 const boxes=[...document.querySelectorAll('.box')], blks=[...document.querySelectorAll('.blk')];
 link(boxes,blks); link(blks,boxes);
</script>
`))

// renderLayoutPage writes the page. index is carried through so every link on it
// stays inside the same index — a layout view that silently jumped indexes would
// show one document's boxes over another's picture.
func renderLayoutPage(w io.Writer, index string, pl *raglit.PageLayout) error {
	return layoutTmpl.Execute(w, struct {
		*raglit.PageLayout
		Index string
	}{pl, index})
}
