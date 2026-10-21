# Admin dashboard restyle — frontend design spec

> 2026-10-22: seat counts and availability now belong to individual boards,
> even when they share an IP. See the superseding
> [per-board seat contract](../superpowers/specs/2026-10-22-per-board-seats-design.md).

Target file: `admin-api/dashboard/index.html` (641 lines, one `<style>`, one `<script>`, served byte-for-byte by `embed.go`; no build step).
Baseline verified 2026-10-21: `cd admin-api && go test -run TestDashboard -count=1 .` → `ok`.
Everything below was checked against `dashboard_test.go`, the `dataviz` skill (validator run on the real surfaces), and the `ui-ux-pro-max` rules (contrast, 44 px touch, bottom-nav ≤ 5, no emoji icons).

## 0. Read this first — constraints that shape every decision

**Hard constraints**

- One file. New CSS goes into the existing `<style>`; new SVG goes into one `<svg style="display:none">` sprite right after `<body>`; new JS helpers go into the existing `<script>` next to `fmtBytes`/`fmtAgo`.
- Never run Prettier or any formatter on this file. Four of the test markers depend on exact whitespace (`const T = k => i18n[k] ?? k;`, `d.slot_mismatch) body.slot`, `$('#dmSlot').validity.badInput`, `/^https:\/\//.test(custom)`).
- The test runs from the module dir: `cd admin-api && go test ./...`. There is no `go.mod`/`go.work` at the repo root, so `go test ./admin-api/...` from the root does not work.

**Markers that must survive (`dashboard_test.go`, exact substrings)**

| Marker | Where it lives today | Restyle gotcha |
|---|---|---|
| `id="dmodal"`, `role="dialog"`, `id="dmCoordHint"`, `id="fwmodal"` | static HTML | keep ids; add classes only |
| `const T = k => i18n[k] ?? k;` | script head | byte-exact |
| `<th>Device</th>` | users table header, inside the template literal | **cannot gain a class or attribute** — hide/shrink this column with `th:nth-child(6)` selectors, never `<th class="…">Device</th>` |
| `reveal-wifi` | API path in `revealWifi()` | n/a |
| `measureDevice(`, `slotPort(`, `otaAll()` | function names | survive as long as the functions keep their names (`function otaAll(){` contains `otaAll()`), so the inline `onclick="otaAll()"` may be replaced |
| `n.slot_mismatch`, `d.slot_mismatch) body.slot`, `$('#dmSlot').validity.badInput` | row template, save handler, slot parser | leave those statements alone |
| `${otaPill(n)}` | FW cell in the device row template | the FW cell must keep this exact interpolation |
| `data-act="ota-cancel"` | `otaPill()` | keep |
| `n.chip||'esp32s3'` | row template **and** `openFw()` | keep both |
| `targets[chip]`, `target: chip` | `openFw()` | keep |
| `/^https:\/\//.test(custom)` | `openFw()` | keep |
| `data-user="${un}"` | users row buttons | keep |

**Strings that must never appear**: `onclick="copyCreds`, `onclick='openUser`, `onclick="openUser`, `onclick="userAction`, `onclick="delUser`, `onclick="assignDevice`, `JSON.stringify(u)`, `onclick="devAction`, `onclick="otaDevice`.
Practical rule: no inline `onclick` at all in new markup (the five that remain — `closeModal()`, `route(...)`, `otaAll()`, `closeFw()`, `saveSettings()` — are tolerated by the test but step 8 removes them), and never name a variable `u` inside a `JSON.stringify(...)` call.

**Design stance (one sentence).** The product is a fleet of physical boards sitting in people's houses; the fleet table is the hero, the slot number is the one thing an operator types into a customer's proxy config, so that numeral is the memorable element — everything else (shell, KPIs, chart chrome) stays quiet so the status tints and the numerals read.

Deliberate departures from the mockups, each with a reason:
- Mockup 1's "NO" column becomes the **slot number** (1–30 ↔ port 3131–3160), not a row index; unassigned shows "—". A row index carries no information; the slot does.
- No ALL-CAPS column headers or stat labels (the current `.stat .k` uppercases). Sentence case; Thai has no case and uppercase tracking makes mixed Thai/Latin headers look broken.
- No notification bell (mockup 3): there is no event source yet. The head row shows a derived "needs attention" count instead (offline + OTA failed + over quota), which is real data.
- Emoji icons (⚡, ↗, ⚠) are replaced with inline SVG; emoji render differently on every OS and read as decoration.
- No light theme. A light pass would mean re-validating every pill/tint pair (14 ratios) and the chart palette for a second surface; the token structure leaves the door open (`:root[data-theme="light"]`) but it is not cheap, so it is not in scope.

---

## 1. Design tokens

Dark is the only theme. Mockup-2 hexes are kept where they pass; where a hex fails as *text*, a lifted "ink" variant is added and the original stays as the *fill/mark* colour. All ratios below are WCAG 2.x, computed (script in the scratchpad, `contrast.js`), not estimated.

```css
:root{
  color-scheme:dark;                       /* native inputs, scrollbars, <select> popups go dark */

  /* surfaces (mockup 2 neutrals) */
  --page:#0E1116;        /* body */
  --card:#222631;        /* cards, table rows, modals, sidebar */
  --card-2:#2F374C;      /* inputs, hover rows, neutral pill bg, gridlines */
  --line:#2F374C;        /* hairline borders (1.27:1 vs card — recessive on purpose) */
  --line-strong:#3D4760; /* input borders that must be seen */

  /* ink */
  --ink:#F2F5FA;         /* primary text   13.8:1 on card, 17.3:1 on page */
  --ink-2:#A0A9C0;       /* secondary text  6.4:1 on card,  8.0:1 on page */
  --ink-3:#868FA3;       /* muted text      4.7:1 on card,  5.8:1 on page  (mockup #727C94 is 3.6:1 — fails AA for text) */
  --ink-deco:#727C94;    /* mockup muted: large numerals, icons, dividers only (≥3:1 rule for non-text) */

  /* accents — fills/marks (mockup 2 hexes) */
  --primary:#455DDC;     /* primary action fill; white on it 5.45:1 */
  --ok:#43D1A7;          /* success / online         7.9:1 on card as a mark */
  --warn:#FF862E;        /* warning / paused / unmeasured / OTA queued  6.3:1 */
  --bad:#F73463;         /* danger / offline-blocked / rolled back      4.1:1 */
  --info:#9646EF;        /* OTA updating / firmware / "info"            3.2:1 */

  /* accents — inks (text on dark; lifted until ≥4.5:1 on their own pill bg) */
  --primary-ink:#8595E8; /* links, active nav text, focus ring   5.4:1 card / 6.7:1 page */
  --ok-ink:#43D1A7;      /* unchanged — already passes */
  --warn-ink:#FF862E;    /* unchanged — already passes */
  --bad-ink:#F96589;     /* #F73463 is 3.5:1 on its pill bg → lifted */
  --info-ink:#B67EF4;    /* #9646EF is 2.8:1 on its pill bg → lifted */
  --danger-fill:#D9224F; /* destructive button bg; white on it 4.89:1 (white on #F73463 is 3.73:1) */

  /* status pills: bg = accent at 16 % over card (pre-composited so no alpha stacking surprises) */
  --pill-ok-bg:#274144;   --pill-ok-fg:var(--ok-ink);       /* 5.67:1 */
  --pill-warn-bg:#453531; --pill-warn-fg:var(--warn-ink);   /* 4.82:1 */
  --pill-bad-bg:#442839;  --pill-bad-fg:var(--bad-ink);     /* 4.51:1 */
  --pill-info-bg:#352B4F; --pill-info-fg:var(--info-ink);   /* 4.52:1 */
  --pill-off-bg:#2F374C;  --pill-off-fg:#C3CBDC;            /* 7.28:1 (neutral: chip tag, offline, IP group) */

  /* row-card edge tints: accent at 8 % over card (decorative — status is also carried by the pill text) */
  --tint-ok:#25343A; --tint-warn:#342E31; --tint-bad:#332735; --tint-info:#2B2940; --tint-off:#262B38;

  /* charts (dataviz-validated for the dark card surface; see §3) */
  --series-1:#5B72E6;    /* slot 1 blue   — 3.6:1 on card */
  --series-2:#DE6815;    /* slot 2 orange — 4.4:1 */
  --series-3:#249F7D;    /* slot 3 green  — 4.6:1 (series use only; status green stays --ok) */
  --meter-fill:var(--series-1);
  --meter-track:#3B4FB0; /* same hue, 2.11:1 on card — validated --ordinal pair with the fill */
  --grid:#2F374C;        /* hairline gridlines, solid, 1.27:1 */
  --axis-ink:var(--ink-3);

  /* focus */
  --focus:var(--primary-ink);

  /* radii — one per hierarchy level, not one for everything */
  --r-sm:6px;   /* tags, gauge segments, sparkline card inset */
  --r-md:10px;  /* buttons, inputs, pills' rectangular cousins */
  --r-lg:14px;  /* cards, table row-cards, modals */
  --r-pill:999px;

  /* elevation — on dark, light edges read better than drop shadows */
  --shadow-card:inset 0 1px 0 rgba(255,255,255,.04);
  --shadow-pop:0 24px 48px -12px rgba(0,0,0,.7), inset 0 1px 0 rgba(255,255,255,.06);

  /* spacing (4-pt) */
  --sp-1:4px; --sp-2:8px; --sp-3:12px; --sp-4:16px; --sp-5:20px; --sp-6:24px; --sp-8:32px;

  /* type */
  --font:system-ui,-apple-system,"Segoe UI",Roboto,"Leelawadee UI","Noto Sans Thai","Helvetica Neue",sans-serif;
  --mono:ui-monospace,"Cascadia Mono",Consolas,"SF Mono",Menlo,monospace;
  --fs-12:.75rem;   /* 12 — Latin-only: MAC, coordinates, axis ticks */
  --fs-13:.8125rem; /* 13 — table cells, pills, Thai minimum */
  --fs-14:.875rem;  /* 14 — body */
  --fs-16:1rem;     /* 16 — section titles, modal h3 */
  --fs-20:1.25rem;  /* 20 — slot numerals, page h2 */
  --fs-28:1.75rem;  /* 28 — KPI values */
  --fs-40:2.5rem;   /* 40 — the one hero figure per view */
  --lh:1.5;         /* Thai tone marks need it; never below 1.4 anywhere */
}
```

**Semantic mapping of the five accents**

| Role | Fill / mark | Ink (text) | Used for |
|---|---|---|---|
| Primary action | `#455DDC` | `#8595E8` | primary buttons, links, active nav, focus ring, selected tab |
| Success / online | `#43D1A7` | `#43D1A7` | online pill, "updated" pill, up-is-good delta |
| Warning / paused | `#FF862E` | `#FF862E` | unmeasured, slot drift, OTA queued, over quota, "disabled" user |
| Danger / blocked | `#F73463` | `#F96589` | blocked, expired, rolled back, seats full, errors; destructive button fill is `#D9224F` |
| Info / OTA | `#9646EF` | `#B67EF4` | `updating` pill (new `pill info`), firmware column accents |

**Backwards-compatible aliases** so step 1 touches zero selectors below it:

```css
:root{ --bg:var(--page); --panel:var(--card); --panel2:var(--card-2); --fg:var(--ink);
       --muted:var(--ink-3); --accent:var(--primary); --r:var(--r-lg); }
```
(`--ok`, `--warn`, `--bad`, `--line` keep their names.)

**Type scale in use**

| Token | px | Weight | Where |
|---|---|---|---|
| hero | 40 | 600, proportional figures | one per view (Overview: devices online) |
| kpi | 28 | 600, proportional | KPI card value |
| h2 / numeral | 20 | 600 / 600 `--ink-deco` | view title / slot numeral |
| h3 | 16 | 600 | modal titles, card titles |
| body | 14 | 400 | default |
| cell | 13 | 400; `tabular-nums` on numeric columns | table cells, pills (600) |
| micro | 12 | 400 mono | MAC, lat/lon, axis ticks — Latin only |

---

## 2. Component specs

Class names the JS already emits and that the CSS must keep answering: `.pill.ok|.bad|.warn|.off`, `.mono`, `.muted`, `.right`, `.row-actions`, `.stat .k .v`, `.cards`, `.card`, `.banner`, `.err`, `.toolbar`, `.field`, `.modal-bg`, `.modal(.wide)`, `button.sec|.danger|.link`, `nav a.active`, `.brand`, `.spacer`.

### 2.1 Icon sprite (replaces ⚡ ↗ ⚠)

```html
<svg style="display:none" aria-hidden="true">
  <symbol id="i-bolt" viewBox="0 0 24 24"><path d="M13 2 3 14h8l-1 8 10-12h-8l1-8z"/></symbol>
  <symbol id="i-ext"  viewBox="0 0 24 24"><path d="M14 3h7v7M21 3l-9 9M19 14v5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7a2 2 0 0 1 2-2h5"/></symbol>
  <symbol id="i-pin"  viewBox="0 0 24 24"><path d="M12 22s7-6.1 7-12a7 7 0 1 0-14 0c0 5.9 7 12 7 12z"/><circle cx="12" cy="10" r="2.5"/></symbol>
  <!-- i-users, i-board (chip), i-cog, i-wifi, i-globe, i-alert: paste Lucide paths (ISC) -->
</svg>
```
```css
.ic{width:1em;height:1em;fill:none;stroke:currentColor;stroke-width:2;stroke-linecap:round;stroke-linejoin:round;vertical-align:-.15em}
```
Use: `<svg class="ic"><use href="#i-bolt"/></svg>`. Icons inherit text colour; never a second colour.

### 2.2 Buttons

```css
button{font:600 var(--fs-14)/1.2 var(--font);min-height:36px;padding:0 var(--sp-4);border:0;border-radius:var(--r-md);
  background:var(--primary);color:#fff;cursor:pointer;transition:background .15s}
button:hover{background:#3B51C9}                 /* white on it 6.55:1 */
button.sec{background:var(--card-2);color:var(--ink);box-shadow:inset 0 0 0 1px var(--line-strong)}
button.sec:hover{background:#38415A}
button.danger{background:var(--danger-fill)}      /* white 4.89:1 */
button.link{background:none;min-height:0;padding:0;color:var(--primary-ink);font-weight:500;text-decoration:underline;text-underline-offset:2px}
button:disabled{opacity:.5;cursor:not-allowed}
.row-actions button{min-height:32px;padding:0 var(--sp-3);font-size:var(--fs-13)}
@media(pointer:coarse){button,.row-actions button{min-height:44px} button.link{padding:10px 0}}
```
Remove the global `button:hover{filter:brightness(1.08)}` (filter on text blurs on Windows ClearType).

### 2.3 Status pill (icon + label, never colour alone)

```css
.pill{display:inline-flex;align-items:center;gap:6px;padding:3px 10px 3px 8px;border-radius:var(--r-pill);
  font:600 var(--fs-13)/1.4 var(--font);white-space:nowrap;vertical-align:middle}
.pill::before{content:"";width:6px;height:6px;border-radius:50%;background:currentColor;flex:none}
.pill.ok  {background:var(--pill-ok-bg);  color:var(--pill-ok-fg)}
.pill.warn{background:var(--pill-warn-bg);color:var(--pill-warn-fg)}
.pill.bad {background:var(--pill-bad-bg); color:var(--pill-bad-fg)}
.pill.info{background:var(--pill-info-bg);color:var(--pill-info-fg)}
.pill.off {background:var(--pill-off-bg); color:var(--pill-off-fg)}
.pill.off::before{display:none}                   /* neutral tags (chip, IP group) carry no state → no dot */
```
JS touch (step 5): in `otaPill()` the `sent` case becomes `pill info` ("updating"); everything else unchanged. The static `<span class="pill bad">rolled back</span>` in the FW modal banner keeps working.

Mapping today's states → pills: online `ok` · offline `off` · blocked `bad` · unmeasured `warn` · slot drift `warn` · queued `warn` · updating `info` · rolled back `bad` · updated `ok` · user active `ok` · disabled `off` · over quota `warn` · expired `bad` · seats full `bad` (the `nm` count).

### 2.4 Row-card table (keeps `<table>` semantics)

```css
table{width:100%;border-collapse:separate;border-spacing:0 var(--sp-2);background:none;border:0;border-radius:0;overflow:visible}
thead th{padding:0 var(--sp-3) var(--sp-1);color:var(--ink-3);font:500 var(--fs-12)/1.4 var(--font);
  text-align:left;text-transform:none;letter-spacing:0;border:0}
tbody td{background:var(--card);box-shadow:var(--shadow-card);border:0;padding:var(--sp-3);
  font-size:var(--fs-13);line-height:var(--lh);vertical-align:middle}
tbody td:first-child{border-radius:var(--r-lg) 0 0 var(--r-lg)}
tbody td:last-child{border-radius:0 var(--r-lg) var(--r-lg) 0;
  background-image:linear-gradient(90deg,var(--card) 0%,var(--row-tint,var(--card)) 100%)}
tbody tr:hover td{background-color:#262B38}
tbody tr:focus-within td{box-shadow:var(--shadow-card),inset 0 0 0 1px var(--line-strong)}
td.num{font-variant-numeric:tabular-nums}
.slot-no{font:600 var(--fs-20)/1 var(--font);color:var(--ink-deco);min-width:2ch;text-align:right}
```
Right-edge status tint — two ways:
- **Zero-JS (step 5a)**: `tbody tr:has(td:nth-child(3) .pill.ok){--row-tint:var(--tint-ok)}` and the same for `.pill.bad`→`--tint-bad`, `.pill.off`→`--tint-off`. Status is the 3rd column in both tables today. Supported Chrome 105+/Safari 15.4+/Firefox 121+; unsupported browsers simply get no tint.
- **Robust (step 5b)**: emit `<tr class="is-ok|is-bad|is-off">` from `viewDevices()` (one expression) and select on the class. Required once the slot column is inserted and the column index shifts.

The old `table{background;border;radius;overflow:hidden}` and `tr:last-child td{border-bottom:none}` rules are deleted; `th,td{border-bottom}` is replaced by the rules above.

### 2.5 Ten-segment bar gauge (pure CSS, `--value`)

```html
<span class="gauge" role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow="70" aria-label="Seats taken"
      style="--value:70;--fill:var(--ok)"></span> <span class="num">7/10</span>
```
```css
.gauge{--value:0;--fill:var(--ok);display:inline-block;width:100px;height:8px;vertical-align:middle;
  background:
    linear-gradient(var(--fill),var(--fill)) 0 0 / calc(var(--value)*1%) 100% no-repeat,
    color-mix(in srgb,var(--fill) 22%,var(--card));          /* same-ramp track (dataviz meter rule) */
  -webkit-mask:repeating-linear-gradient(90deg,#000 0 calc(10% - 2px),transparent calc(10% - 2px) 10%);
          mask:repeating-linear-gradient(90deg,#000 0 calc(10% - 2px),transparent calc(10% - 2px) 10%);
  border-radius:2px}
@supports not (background:color-mix(in srgb,red,blue)){.gauge{background-color:var(--card-2)}}
```
JS: `gauge(pct, fill)` returns the span with `--value` snapped to a segment (`Math.round(pct/10)*10`) and `fill` chosen by threshold: `<70 → --ok`, `70–89 → --warn`, `≥90 → --bad`. The visible number beside it is mandatory (relief rule; the gauge is not the only way to read the value).
Values: devices → seats `active_users/max_users` (shows "—" when `max_users` is 0, i.e. unmeasured); users → `usage_gb/quota_gb` when `quota_gb>0`, else no gauge, just the GB figure.

### 2.6 Round flag + "City, Country"

```html
<span class="geo"><span class="flag" data-cc="TH" aria-hidden="true"></span>Bangkok, Thailand</span>
```
```css
.geo{display:inline-flex;align-items:center;gap:8px}
.flag{width:22px;height:22px;border-radius:50%;background:var(--card-2);display:inline-grid;place-items:center;
  font:600 9px/1 var(--font);color:var(--ink-2);letter-spacing:.02em;overflow:hidden;flex:none}
.flag::before{content:attr(data-cc)}                 /* fallback: ISO code in a round badge */
.flag.emoji::before{content:attr(data-flag);font-size:14px;line-height:22px}
```
JS:
```js
const flagEmoji = cc => /^[A-Z]{2}$/.test(cc||'') ? String.fromCodePoint(...[...cc].map(c=>0x1F1E6+c.charCodeAt(0)-65)) : '';
const FLAGS_OK = !/Windows/.test(navigator.userAgent);   // Segoe UI Emoji ships no flag glyphs: Windows shows "TH" letters
function flag(cc){ const e=FLAGS_OK?flagEmoji(cc):''; return `<span class="flag${e?' emoji':''}" data-cc="${esc(cc||'')}" data-flag="${e}" aria-hidden="true"></span>`; }
```
Windows renders regional-indicator pairs as two letters, which the round badge turns into a deliberate design rather than a glitch; the emoji path is used elsewhere. **Backend note:** `geo.go` stores ip-api's `country` (a name, "Thailand"), not `countryCode`. Add `countryCode` to the ip-api field list and expose `geo.country_code`; until then the badge shows a globe icon (`<use href="#i-globe">`) and no code — never guess a code from the name.

### 2.7 Location link

```js
const loc = `<span class="geo">${flag(g.country_code)}<span>${esc(cityLoc)}</span>${hasPos
  ? ` <a class="maplink" target="_blank" rel="noopener noreferrer" title="${esc(T('openMap'))}"
       href="https://www.openstreetmap.org/?mlat=${n.lat}&amp;mlon=${n.lon}#map=16/${n.lat}/${n.lon}">
       <svg class="ic"><use href="#i-pin"/></svg><span class="mono micro">${esc(n.lat.toFixed(3)+', '+n.lon.toFixed(3))}</span></a>` : ''}</span>`;
```
```css
.maplink{display:inline-flex;align-items:center;gap:4px;color:var(--primary-ink);margin-left:6px}
.micro{font-size:var(--fs-12);color:var(--ink-3)} .maplink:hover .micro{color:var(--primary-ink)}
```
City/country stays as text (what an operator reads), coordinates become the link's secondary line (what a map needs). `rel`, `title` and the OSM URL are unchanged.

### 2.8 KPI card with sparkline and delta pill

```html
<div class="stat" style="--accent:var(--ok)">
  <div class="k">Exit devices online</div>
  <div class="v">12 <small>/ 15</small></div>
  <div class="trend"><!-- spark() --> <span class="delta up good">+2 vs yesterday</span></div>
</div>
```
```css
.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(200px,1fr));gap:var(--sp-3);margin-bottom:var(--sp-6)}
.stat{position:relative;background:var(--card);border-radius:var(--r-lg);padding:var(--sp-4);box-shadow:var(--shadow-card);overflow:hidden}
.stat::after{content:"";position:absolute;inset:0 0 auto 0;height:2px;background:var(--accent,transparent);opacity:.9}
.stat .k{color:var(--ink-2);font-size:var(--fs-13);text-transform:none;letter-spacing:0}
.stat .v{font:600 var(--fs-28)/1.1 var(--font);margin-top:var(--sp-1)}      /* proportional figures on purpose */
.stat .v small{font-size:var(--fs-14);color:var(--ink-3);font-weight:400}
.stat.hero .v{font-size:var(--fs-40)}                                        /* exactly one per view */
.trend{display:flex;align-items:center;justify-content:space-between;gap:var(--sp-2);margin-top:var(--sp-3)}
.spark{width:120px;height:32px;flex:none}
.spark-line{fill:none;stroke:var(--ink-3);stroke-width:2;stroke-linejoin:round;stroke-linecap:round;vector-effect:non-scaling-stroke}
.spark-now{fill:none;stroke:var(--series-1);stroke-width:2;stroke-linecap:round;vector-effect:non-scaling-stroke}
.spark-wash{fill:var(--series-1);opacity:.1}
.spark-dot{fill:var(--series-1);stroke:var(--card);stroke-width:2}          /* 2px surface ring */
.delta{font:600 var(--fs-12)/1.4 var(--font);padding:2px 8px;border-radius:var(--r-pill);background:var(--pill-off-bg);color:var(--pill-off-fg)}
.delta.good{background:var(--pill-ok-bg);color:var(--pill-ok-fg)} .delta.badly{background:var(--pill-bad-bg);color:var(--pill-bad-fg)}
```
```js
function spark(pts, w=120, h=32){
  if(!Array.isArray(pts)||pts.length<2) return '';
  const max=Math.max(...pts), min=Math.min(...pts), span=(max-min)||1, pad=3, L=pts.length-1;
  const x=i=>(pad+i*(w-2*pad)/L).toFixed(1), y=v=>(h-pad-(v-min)*(h-2*pad)/span).toFixed(1);
  const d=pts.map((v,i)=>x(i)+','+y(v)).join(' ');
  return `<svg class="spark" viewBox="0 0 ${w} ${h}" aria-hidden="true" focusable="false">
    <polygon class="spark-wash" points="${x(0)},${h-pad} ${d} ${x(L)},${h-pad}"/>
    <polyline class="spark-line" points="${d}"/>
    <polyline class="spark-now" points="${x(L-1)},${y(pts[L-1])} ${x(L)},${y(pts[L])}"/>
    <circle class="spark-dot" cx="${x(L)}" cy="${y(pts[L])}" r="4"/></svg>`;
}
function delta(cur, prev, upIsGood, label){
  if(prev==null||!isFinite(prev)) return '';
  const d=cur-prev, dir=d>0?'up':d<0?'down':'flat', good=d===0?'':((d>0)===upIsGood?'good':'badly');
  const txt=(d>0?'+':'')+(Math.abs(d)>=100?Math.round(d):+d.toFixed(1))+(label?' '+label:'');
  return `<span class="delta ${dir} ${good}">${esc(txt)}</span>`;
}
```
Per the dataviz stat-tile contract: label, value, delta (signed, vs a named period, colour = direction × whether up is good), 12-point sparkline in the de-emphasis hue with the current period in the accent. Render the card without the trend row when no series exists; never fabricate one.

### 2.9 Activity feed item

```html
<ol class="feed">
  <li class="feed-item" style="--accent:var(--info)">
    <span class="feed-ic"><svg class="ic"><use href="#i-bolt"/></svg></span>
    <div class="feed-body"><div class="feed-t">Firmware 2.4.22 installed</div><div class="feed-s">node-07 · 3C:71:BF:…:A2</div></div>
    <time class="feed-when" datetime="2026-10-21T08:12:00Z">14 min ago</time>
  </li>
</ol>
```
```css
.feed{list-style:none;margin:0;padding:0;display:flex;flex-direction:column;gap:var(--sp-1)}
.feed-item{display:grid;grid-template-columns:32px 1fr auto;gap:var(--sp-3);align-items:center;padding:var(--sp-2) var(--sp-3);border-radius:var(--r-md)}
.feed-item:hover{background:var(--card-2)}
.feed-ic{width:32px;height:32px;border-radius:50%;display:grid;place-items:center;color:var(--accent);
  background:color-mix(in srgb,var(--accent) 16%,var(--card))}
.feed-t{font-size:var(--fs-13);color:var(--ink)} .feed-s{font-size:var(--fs-12);color:var(--ink-3)}
.feed-when{font-size:var(--fs-12);color:var(--ink-3);font-variant-numeric:tabular-nums;white-space:nowrap}
```
Data today: none. Interim (no backend): derive from `/api/devices` — `first_seen` ("New device"), `ota_at`+`ota_status` ("Firmware … installed / rolled back"), `last_seen` for offline boards ("Went offline") — sorted desc, top 8. Proper source: `GET /api/events?limit=20` → `[{at, kind, mac, user, text}]`.

### 2.10 Shell: sidebar (keep) vs top nav

Recommendation: **keep the existing left sidebar** (it is already in the DOM as `<aside>` + `nav a[data-view]`), restyled as mockup 3; do not add a top bar. Reasons: four views plus three utility links fit a rail, the fleet table needs every horizontal pixel so a top bar would steal 56 px of height on a 13" laptop for nothing it can show, and the current routing JS already works on `nav a`.

```css
#app{grid-template-columns:232px 1fr}
aside{background:var(--card);border-right:1px solid var(--line);padding:var(--sp-4) var(--sp-3);gap:2px}
.brand{display:flex;align-items:center;gap:10px;font:600 var(--fs-16)/1.2 var(--font);padding:var(--sp-2) var(--sp-3) var(--sp-5)}
.brand small{font-family:var(--mono);font-size:var(--fs-12);color:var(--ink-3);font-weight:400}
nav a{display:flex;align-items:center;gap:10px;padding:10px var(--sp-3);border-radius:var(--r-md);color:var(--ink-2);position:relative}
nav a:hover{background:var(--card-2);color:var(--ink)}
nav a.active{background:#282F4C;color:var(--primary-ink)}                            /* primary at 16 % over card */
nav a.active::before{content:"";position:absolute;left:-12px;top:8px;bottom:8px;width:3px;border-radius:2px;background:var(--primary)}
aside .util{margin-top:auto;border-top:1px solid var(--line);padding-top:var(--sp-3)}  /* wraps flashLink, gostuiLink, logout */
.head{display:flex;align-items:center;gap:var(--sp-3);margin-bottom:var(--sp-5)}
.head h2{font:600 var(--fs-20)/1.2 var(--font);margin:0 auto 0 0}
.head .attn{margin-right:auto}                                                          /* "3 need attention" pill, derived */
```
Responsive — bottom tab bar on phones (≤ 720 px), four items (bottom-nav ≤ 5 rule):
```css
@media(max-width:720px){
  #app{grid-template-columns:1fr}
  aside{position:fixed;left:0;right:0;bottom:0;z-index:5;flex-direction:row;padding:6px 8px calc(6px + env(safe-area-inset-bottom));
        border-right:0;border-top:1px solid var(--line)}
  .brand,aside .util{display:none}
  nav{display:contents} nav a{flex:1;flex-direction:column;gap:2px;font-size:11px;padding:6px 0;min-height:44px;justify-content:center}
  nav a.active::before{display:none} nav a.active{background:none} nav a.active .ic{color:var(--primary-ink)}
  main{padding:var(--sp-4) var(--sp-4) calc(72px + env(safe-area-inset-bottom))}
}
```
Each `nav a` gets an icon (`i-board`, `i-users`, `i-cpu`, `i-cog`) before its label — labels stay visible on both layouts. On phones the three utility links disappear from the rail; `viewSettings()` already knows both URLs, so it gains a "Links" card with Flash firmware / GOST config UI / Sign out (`<button type="button" class="sec" id="logoutBtn">` wired with `addEventListener` to the existing `logout`). Add `aria-current="page"` to the active nav link (two lines in the nav click handler).

Head row per view: title left; "needs attention" pill (Overview/Devices) next to it; the view's one primary action right (`+ New user`, `Update all online…`); Refresh becomes a `sec` icon button. A client-side filter `<input type="search" placeholder="Filter by name, MAC, IP, city">` on Devices/Users filters rows by `textContent` — no API, no delegated-handler changes.

---

## 3. Charts (inline SVG, no library)

Form choice follows `choosing-a-form.md`: a single value with a trend → stat tile + sparkline; trend over time with two series → area/line with a legend; a single ratio against a limit → meter (the "donut" is a radial meter, never a pie: one ratio, two arcs, number centred). Part-to-whole of fleet states → one horizontal stacked bar with status colours (reserved semantics, icon+label legend).

**Palette (validated).** `node scripts/validate_palette.js "#5B72E6,#DE6815,#249F7D,#A565F2,#EF4A72" --mode dark --surface "#222631"` → all five checks pass (lightness band, chroma, CVD ΔE worst adjacent 12.2, normal-vision ΔE 22.9, contrast ≥ 3:1). The raw mockup hexes fail this test (green `#43D1A7` L 0.775 and orange `#FF862E` L 0.743 sit above the dark band; blue `#455DDC` is 2.77:1 on the card), which is why marks use the snapped steps while pills keep the mockup hexes. Meter: `"#3B4FB0,#5B72E6" --ordinal` passes (monotone, ΔL ≥ 0.06, track 2.11:1 on card). Only two series are ever drawn at once, so the all-pairs cap (3) is never approached.

Chrome rules applied everywhere: gridlines solid 1 px `--grid`; axis text `--axis-ink` 12 px; lines 2 px round join; markers r ≥ 4 with a 2 px `--card` ring; area wash 10 %; legend present for ≥ 2 series, none for one; selective direct labels (series end values only); text in ink tokens, never series colour; every chart has a table twin (`<details>` below it); hover layer with crosshair + tooltip whose labels are set with `textContent`; refetch holds the previous render at `opacity:.5` instead of a skeleton.

### 3.1 Sparklines (KPI row, Overview)

| Card | Value (exists today) | Series needed (14–30 daily points) | Up is good |
|---|---|---|---|
| Exit devices online (**hero**, 40 px) | `broker.nodes_online / nodes_total` | `series.devices_online[]` | yes |
| Traffic today | derived from `series.traffic_bytes` | `series.traffic_bytes[]` | neutral (no delta colour) |
| Dial failures | `dials_err + dials_no_node` | `series.dials_err[]` | no |
| Users active | `users.active / users.total` | `series.users_active[]` | yes |

Cards for "Over quota / expired" and "Idle connections" become a one-line strip under the KPI row (small numbers, no sparkline) — six equal tiles was the current layout's weakest point; four KPIs plus a hero reads.

### 3.2 Area chart — traffic, last 30 days (Overview, full width)

Two series: **downloaded** (`--series-1`) and **uploaded** (`--series-2`). Validated pair; warm/cool, ΔE 29 under protan. Not stacked (the question is "how much each", not the total) — two 2 px lines, each with a 10 % wash under it, 2 px card-coloured ring on the end marker, series end value as the only direct label. Y ticks compact (`0 · 5 GB · 10 GB`), x ticks every 7 days, container height = 180 px plot + 24 px axis band.

```js
function areaChart(rows, keys, colors, fmt){           // rows: [{day, bytes_down, bytes_up}]
  const W=720,H=204,P={l:44,r:12,t:8,b:24},pw=W-P.l-P.r,ph=H-P.t-P.b;
  const max=Math.max(1,...rows.flatMap(r=>keys.map(k=>+r[k]||0)));
  const x=i=>P.l+i*pw/Math.max(1,rows.length-1), y=v=>P.t+ph-(v/max)*ph;
  const ticks=[0,.5,1].map(f=>f*max);
  const grid=ticks.map(t=>`<line x1="${P.l}" x2="${W-P.r}" y1="${y(t)}" y2="${y(t)}" class="grid"/><text x="${P.l-6}" y="${y(t)}" class="tick" text-anchor="end" dominant-baseline="middle">${esc(fmt(t))}</text>`).join('');
  const xt=rows.map((r,i)=>i%7===0?`<text x="${x(i)}" y="${H-6}" class="tick" text-anchor="middle">${esc(r.day.slice(5))}</text>`:'').join('');
  const series=keys.map((k,si)=>{
    const pts=rows.map((r,i)=>x(i).toFixed(1)+','+y(+r[k]||0).toFixed(1)).join(' ');
    const L=rows.length-1, lx=x(L), ly=y(+rows[L][k]||0);
    return `<polygon points="${P.l},${P.t+ph} ${pts} ${x(L)},${P.t+ph}" fill="${colors[si]}" opacity=".1"/>
      <polyline points="${pts}" fill="none" stroke="${colors[si]}" stroke-width="2" stroke-linejoin="round" stroke-linecap="round"/>
      <circle cx="${lx}" cy="${ly}" r="4" fill="${colors[si]}" stroke="var(--card)" stroke-width="2"/>
      <text x="${lx-8}" y="${ly-8}" class="val" text-anchor="end">${esc(fmt(+rows[L][k]||0))}</text>`;
  }).join('');
  return `<svg class="chart" viewBox="0 0 ${W} ${H}" role="img" aria-label="Traffic, last 30 days">${grid}${xt}${series}<line class="xhair" y1="${P.t}" y2="${P.t+ph}" hidden/></svg>`;
}
```
```css
.chart{width:100%;height:auto;display:block}
.chart .grid{stroke:var(--grid);stroke-width:1}
.chart .tick,.chart .val{fill:var(--axis-ink);font:400 12px var(--font)} .chart .val{fill:var(--ink);font-weight:600}
.chart .xhair{stroke:var(--ink-3);stroke-width:1}
.legend{display:flex;gap:var(--sp-4);font-size:var(--fs-13);color:var(--ink-2);margin:0 0 var(--sp-2) 44px}
.legend i{display:inline-block;width:14px;height:2px;background:var(--c);vertical-align:middle;margin-right:6px;border-radius:1px}
.tip{position:absolute;pointer-events:none;background:var(--card-2);border-radius:var(--r-md);padding:8px 10px;font-size:var(--fs-12);box-shadow:var(--shadow-pop)}
.tip b{color:var(--ink);font-size:var(--fs-13)}   /* value leads, label follows */
```
Hover: one `pointermove` on the `<svg>`, snap to nearest index, move the `.xhair` line, fill a `.tip` with one row per series (line-key in series colour, value bold, label muted) via `textContent`; `pointerleave` hides both. Keyboard: `tabindex="0"` on the svg, ←/→ move the index, same tooltip. Below the chart: `<details><summary>Table view</summary><table>…</table></details>`.

**Data needed (does not exist yet):** `GET /api/traffic?days=30` → `[{day:"2026-09-22", bytes_down, bytes_up}]`. The store only has per-user `usage_bytes` rolled monthly, and the broker exposes cumulative `bytes_up/down` per node since boot. The backend needs a sampler (admin-api already polls the broker) writing a `daily_stats(day, mac, bytes_up, bytes_down, online, dials_err, …)` row with **deltas**, resetting the baseline when a counter goes backwards (board reboot). The same table feeds the sparklines in 3.1 (`series.*` on `/api/overview`).

### 3.3 Radial meter — fleet seats used vs free (Overview, right column)

Ratio: `Σ active_users / Σ max_users` over `/api/devices` (exists today; Overview makes one extra call). One hue: fill `--meter-fill`, track `--meter-track`, round cap (the 4 px rounded data-end rule, radial). Centre number 20 px semibold in `--ink`, caption under: "7 of 12 seats taken". No legend (single series). A second meter for "GB this month vs Σ quota" is reasonable; not three.

```js
function donut(pct, label){
  const r=40, C=2*Math.PI*r, v=Math.max(0,Math.min(100,+pct||0));
  return `<svg class="donut" viewBox="0 0 100 100" role="meter" aria-valuemin="0" aria-valuemax="100" aria-valuenow="${Math.round(v)}" aria-label="${esc(label)}">
    <circle cx="50" cy="50" r="${r}" fill="none" stroke="var(--meter-track)" stroke-width="10"/>
    <circle cx="50" cy="50" r="${r}" fill="none" stroke="var(--meter-fill)" stroke-width="10" stroke-linecap="round"
            stroke-dasharray="${(v/100*C).toFixed(2)} ${C.toFixed(2)}" transform="rotate(-90 50 50)"/>
    <text x="50" y="50" text-anchor="middle" dominant-baseline="central" class="donut-num">${Math.round(v)}%</text></svg>`;
}
```
```css
.donut{width:120px;height:120px} .donut-num{fill:var(--ink);font:600 20px var(--font)}
```

### 3.4 Fleet status bar (Overview header line, replaces the text banner)

One horizontal stacked bar, 8 px tall, segments online/offline/blocked/updating in status colours (`--ok`, `--pill-off-fg`, `--bad`, `--info`), 2 px `--page` gaps between segments, legend with dot + label + count under it. Counts exist today (`nodes_online`, `nodes_total`, `blocked`, `ota_status==='sent'`). Status colours are correct here because the segments *mean* good/bad — never reuse them as "series 4" in the traffic chart.

---

## 4. Migration plan (single file, every step test-green)

After every step: `cd admin-api && go test ./... && go vet ./...`, then open the page against a live admin-api and click Edit / Measure / Reveal / Update FW once each (those are the delegated paths the test only string-matches).

Where things go inside `<style>`, in this order, each section headed by a `/* ---- */` comment: tokens → reset/base/type → shell (login, aside, nav, head) → components (button, pill, card/stat, table, gauge, geo, feed, chart) → views → responsive (`@media(max-width:720px)`, `@media(pointer:coarse)`) → motion & a11y (`prefers-reduced-motion`, `forced-colors`).

| # | Step | JS touched | Size |
|---|---|---|---|
| 1 | **Tokens.** Replace the `:root` block with §1 plus the alias block. Add `color-scheme:dark`. Nothing else changes; the page re-skins through the aliases. | none | S |
| 2 | **Base + buttons + pills + focus.** Font stack, `--lh`, sentence-case headers (drop `text-transform`), §2.2 buttons, §2.3 pills (+ `.pill.info`), focus ring `outline:2px solid var(--focus);outline-offset:2px`, `.err` in `--bad-ink`, `.banner` in primary tint. Add the icon sprite and swap ⚡/↗ in the static aside markup. | `otaPill()` `sent` → `pill info` (1 line) | S |
| 3 | **Shell.** §2.10 aside restyle, `aside .util` wrapper div around the three links, icons in `nav a`, head row, bottom tab bar, `aria-current`, Settings "Links" card with `#logoutBtn`. | nav click handler (+2 lines), `viewSettings()` (+1 card), new `#logoutBtn` listener | M |
| 4 | **Overview.** §2.8 KPI cards (hero + 3), §3.4 status bar, §3.3 meter (one extra `/api/devices` call, `Promise.all`), attention pill. Sparklines gated on `o.series` — rendered only when the backend ships it. Replace `onclick="route('overview')"` with an `id`+listener while here. | `viewOverview()` rewritten (~40 lines), helpers `spark`, `delta`, `donut`, `gauge`, `flag` added | M |
| 5a | **Row-card tables, zero JS.** §2.4 table CSS + `:has()` tints on column 3. Both tables become row-cards with no template change. | none | S |
| 5b | **Devices row content.** Add slot numeral as new first cell (`<td class="slot-no">${hasSlot?esc(n.slot):'—'}</td>` + `<th></th>`), `<tr class="is-…">`, §2.6/2.7 geo cell, §2.5 seats gauge replacing the `nmHtml` span, `data-l="…"` labels on each `td` for the phone layout. Keep every marker: `${otaPill(n)}` in the FW cell, `n.chip||'esp32s3'`, `n.slot_mismatch`, `data-act` buttons untouched. Replace `onclick="otaAll()"` with `id="otaAllBtn"` + listener (the `otaAll()` marker survives in the function definition). | `viewDevices()` template (~25 lines) | M |
| 6 | **Users row content.** Same treatment: `<tr class>`, usage gauge when `quota_gb>0`, `data-l` labels. `<th>Device</th>` stays verbatim; `data-user="${un}"` buttons untouched. | `viewUsers()` template (~10 lines) | S |
| 7 | **History views.** §3.2 area chart + §2.9 feed, each with an empty state ("No history yet — recording starts with this version of the admin API") when the endpoint 404s. Ships behind `api('/api/traffic?days=30').catch(()=>null)` so it is safe before the backend lands. | `viewOverview()` (+ two fetches), `areaChart`, `feed` | M (+ backend: sampler, `/api/traffic`, `series` on `/api/overview`, `/api/events`, `geo.country_code`) |
| 8 | **Hygiene.** Remove the remaining inline `onclick=` (`closeModal`, `closeFw`, `saveSettings`) with ids + `addEventListener`; `button:hover` filter removed; the three modals get `--shadow-pop` and `backdrop-filter:blur(2px)`. | 3 listeners | S |
| 9 | **Phone + a11y pass.** 390 px walk-through (§5), `prefers-reduced-motion`, `forced-colors`, Lighthouse a11y ≥ 95. | none | S |

Theming without touching JS logic: every colour decision is a token or a class the JS already emits. The only JS edits in the whole plan are template-literal markup (steps 3–7) and listener registrations (steps 3, 4, 5b, 8); no fetch, state, or handler logic changes. Steps 1, 2, 5a, 9 are pure CSS.

---

## 5. Accessibility & density

**Contrast (computed).** Body `--ink` 13.8:1, secondary 6.4:1, muted text `--ink-3` 4.7:1 (the mockup's `#727C94` is 3.6:1 on the card — fine for 20 px numerals and icons, not for 13 px text, hence the split). Pill pairs 4.51–7.28:1 (§1 table). Buttons: white on primary 5.45:1, on danger `#D9224F` 4.89:1, `sec` text 13.8:1. Chart marks ≥ 3.6:1 on the card; axis text 4.7:1. Focus ring `#8595E8` 5.4:1 on card, 6.7:1 on page. Edge tints (1.07–1.17:1) are decorative and duplicated by pill text.

**Focus.** Keep the existing `:focus-visible` rule, retarget to `--focus`; add `tbody tr:focus-within td` ring so the active row is visible while tabbing through its buttons; `.pill` and `.gauge` are not focusable (no action). Modal trap and `#app.inert` already exist — keep. Add `aria-current="page"` on the active nav link, `role="meter"` on gauges/donut, `aria-label` on icon-only buttons (Refresh), `aria-hidden` on sparklines (value is in text).

**Keyboard access to delegated actions.** All actions are real `<button type="button" data-act>` elements, so Tab/Enter/Space work with the existing `addEventListener('click')` delegation; nothing to add. `button.link` (assign/change/cancel) gets `padding:10px 0` on coarse pointers for a 44 px target and keeps its underline so it is recognisable without colour. Row action buttons: 32 px on desktop, 44 px on touch, ≥ 8 px gaps.

**Phone (390 px) — devices table.** Switch to a card grid instead of a 13-column scroll:
```css
@media(max-width:720px){
  thead{position:absolute;left:-9999px}              /* keep for AT, hide visually */
  tbody tr{display:grid;grid-template-columns:40px 1fr auto;gap:var(--sp-1) var(--sp-3);background:var(--card);
           border-radius:var(--r-lg);padding:var(--sp-3);margin-bottom:var(--sp-2);
           background-image:linear-gradient(90deg,var(--card) 70%,var(--row-tint,var(--card)))}
  tbody td{display:block;background:none;box-shadow:none;padding:0;border-radius:0;background-image:none}
  tbody td[data-l]::before{content:attr(data-l) " ";color:var(--ink-3);font-size:var(--fs-12)}
  td.slot-no{grid-row:1/3} td:nth-child(2){grid-column:2} td:nth-child(4){grid-column:3;grid-row:1;justify-self:end}  /* name, status */
  td:nth-child(3),td:nth-child(6),td:nth-child(10),td:nth-child(11),td:nth-child(12){display:none}   /* kind, wifi, isp, uptime, traffic */
  td.row-actions{grid-column:1/-1;display:flex;flex-wrap:wrap;gap:var(--sp-2);text-align:left}
}
```
Columns that collapse at 390 px (devices, after step 5b's numbering): Kind, WiFi, ISP, Uptime, Traffic hidden; Name+MAC, Status, Slot numeral, Users/seats gauge, Exit IP, Location, Capacity, FW (with `${otaPill(n)}`) and the action row stay. WiFi reveal remains reachable from the Edit modal, so nothing is lost. Users table: Rate and Expires hidden; Username, Status, Usage gauge, Device (`th:nth-child(6)`, not a class), actions stay. Status is a 3-column grid cell at top-right, mirroring mockup 1.

**Thai text.** The stack names `"Leelawadee UI"` (Windows) and `"Noto Sans Thai"` (Android/Linux) explicitly because `Segoe UI` has no Thai glyphs and silent fallback picks Tahoma with different metrics. Rules: line-height 1.5 everywhere (tone marks stack above the ascender), no `text-transform`, no fixed heights on pills/buttons (min-height only), Thai minimum 13 px (`--fs-13`) — the 12 px `micro` size is reserved for Latin digits/MAC/coords; headers and i18n labels never go below 13 px. When `i18n` gets a Thai table, set `<html lang>` from the active language so line-breaking (`word-break` for Thai has no spaces) and font selection are correct; add `overflow-wrap:anywhere` to table cells.

**Motion.** Only two transitions: button background (150 ms) and gauge width (`.gauge{transition:background-size .3s}`), both under `@media(prefers-reduced-motion:reduce){*{transition:none!important}}`. No entrance animations, no hover lifts on cards.

**Forced colours / print.** `@media(forced-colors:active){.pill{border:1px solid CanvasText} .gauge{forced-color-adjust:none;outline:1px solid CanvasText}}`; chart series keep distinct dash patterns only when `forced-colors` is active (`stroke-dasharray` on series 2).
