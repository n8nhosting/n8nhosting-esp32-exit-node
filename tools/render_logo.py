"""TomYumProxy logo directions: three icons, outlined Kanit wordmarks, lockups, preview board.

Wordmarks are glyph outlines from Kanit (SIL OFL 1.1, Cadson Demak), shaped with
HarfBuzz so kerning matches the font; the SVGs need no installed font.

    python -m venv .logoenv && .logoenv/Scripts/python -m pip install fonttools uharfbuzz
    # Kanit-Bold.ttf and Kanit-Medium.ttf from https://github.com/google/fonts/tree/main/ofl/kanit
    .logoenv/Scripts/python tools/render_logo.py <fonts dir> docs/brand/tomyumproxy
"""
import pathlib
import sys

import uharfbuzz as hb
from fontTools.pens.svgPathPen import SVGPathPen
from fontTools.pens.transformPen import TransformPen
from fontTools.ttLib import TTFont

FONTS, OUT = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2])
OUT.mkdir(parents=True, exist_ok=True)

# the dish: chili, broth, lemongrass, kaffir lime, coconut cream
C = dict(chili="#E8322B", chili_hi="#FF6A3D", chili_lo="#C91F2B", broth="#F7962E", grass="#FFC84A",
         lime="#46B35E", cream="#FFF3E0", ink="#1C1416", night="#0E1116", proxy_dark="#FF7A3D", proxy_light="#D62B25")


def grads(p):
    return (f'<linearGradient id="{p}chili" x1="0" y1="0" x2="1" y2="1"><stop offset="0" stop-color="{C["chili_hi"]}"/>'
            f'<stop offset="1" stop-color="{C["chili_lo"]}"/></linearGradient>'
            f'<linearGradient id="{p}heat" x1="0" y1="1" x2="0" y2="0"><stop offset="0" stop-color="{C["broth"]}"/>'
            f'<stop offset="1" stop-color="{C["grass"]}"/></linearGradient>')


# 64x64 icons; p prefixes gradient ids so several icons can share one document
def icon_bowl(p):
    return (f'<path d="M7 33.5h50c0 12.7-11.2 22.5-25 22.5S7 46.2 7 33.5z" fill="url(#{p}chili)"/>'
            f'<path d="M7 33.5h50c0 1.9-.3 3.7-.8 5.4H7.8c-.5-1.7-.8-3.5-.8-5.4z" fill="{C["broth"]}" opacity=".9"/>'
            f'<path d="M8.5 31.5c3-6.5 10.5-8 15-4.4-4.8 3.2-10.3 4.6-15 4.4z" fill="{C["lime"]}"/>'
            f'<g fill="none" stroke="url(#{p}heat)" stroke-width="3.4" stroke-linecap="round">'
            f'<path d="M23 27.5c-3.2-3.6 2.6-6.6-.4-11"/><path d="M41 27.5c-3.2-3.6 2.6-6.6-.4-11"/>'
            f'<path d="M32 27.5c-3.6-4.3 3-8.1-.2-14.2"/></g>'
            f'<circle cx="31.6" cy="9.2" r="3.6" fill="{C["grass"]}"/>')


# favicon cut of the bowl (16–32 px): one heavy steam line ending in its node, no leaf
def icon_bowl_small(p):
    return (f'<path d="M5 31h54c0 14-12 25-27 25S5 45 5 31z" fill="url(#{p}chili)"/>'
            f'<path d="M5 31h54c0 2.3-.3 4.5-.9 6.6H5.9C5.3 35.5 5 33.3 5 31z" fill="{C["broth"]}"/>'
            f'<path d="M32 25c-4.6-5.2 3.8-9.6-.4-16.5" fill="none" stroke="url(#{p}heat)" stroke-width="6.5" stroke-linecap="round"/>'
            f'<circle cx="31.4" cy="7.6" r="5.4" fill="{C["grass"]}"/>')


def icon_chili(p):
    return (f'<g fill="none" stroke="url(#{p}heat)" stroke-width="3.4" stroke-linecap="round">'
            f'<path d="M33 9.5a14 14 0 0 1 13.5 13.5"/><path d="M33 2.5a21 21 0 0 1 20.5 20.5"/></g>'
            f'<circle cx="34.5" cy="21.5" r="3.4" fill="{C["grass"]}"/>'
            f'<path d="M14.5 27c-.6 13.5 10.4 25.4 30 29.6 4.6 1 8.3-.2 6.8-2.7-.7-1.2-2.9-1.4-5.4-2C33.4 48.8 25.6 39.6 25 27.2c-.2-3.7-10.3-3.9-10.5-.2z" fill="url(#{p}chili)"/>'
            f'<path d="M11.8 28.4c.4-6.6 15.6-6.6 16 0-4-2.2-12-2.2-16 0z" fill="{C["lime"]}"/>'
            f'<path d="M19.8 23.4c0-4.6 1.8-7.6 6-9.2" fill="none" stroke="{C["lime"]}" stroke-width="3.2" stroke-linecap="round"/>')


def icon_pin(p):
    return (f'<path d="M32 59S11 38.8 11 25.3C11 13.5 20.4 4.5 32 4.5s21 9 21 20.8C53 38.8 32 59 32 59z" fill="url(#{p}chili)"/>'
            f'<path d="M32 10.5c4.2 6 9.6 9.4 9.6 17 0 6.1-4.3 10.2-9.6 10.2s-9.6-4.1-9.6-10.2c0-4.4 2.4-7.3 4.6-9.6.3 3.2 1.6 5 3.3 5.6-.6-4.8.1-9.1 1.7-13z" fill="url(#{p}heat)"/>'
            f'<path d="M32 23.8c2 2.6 4.1 4.3 4.1 7.4 0 2.6-1.8 4.3-4.1 4.3s-4.1-1.7-4.1-4.3c0-2.7 2.2-4.4 4.1-7.4z" fill="{C["cream"]}"/>')


ICONS = {"bowl": icon_bowl, "chili": icon_chili, "pin": icon_pin}
SMALL = {"bowl": icon_bowl_small}


def shape_path(font_file, text, size, x0, baseline, tracking=0.0):
    """Outline of text set at size px, starting at x0 on baseline, with tracking in em; returns (svg path d, advance)."""
    blob = hb.Blob.from_file_path(str(font_file))
    face = hb.Face(blob)
    font = hb.Font(face)
    buf = hb.Buffer()
    buf.add_str(text)
    buf.guess_segment_properties()
    hb.shape(font, buf, {"kern": True, "liga": True})
    tt = TTFont(str(font_file))
    upem = tt["head"].unitsPerEm
    gs = tt.getGlyphSet()
    order = tt.getGlyphOrder()
    scale = size / upem
    pen = SVGPathPen(gs, ntos=lambda v: f"{v:.2f}".rstrip("0").rstrip("."))
    x = 0
    for info, pos in zip(buf.glyph_infos, buf.glyph_positions):
        tp = TransformPen(pen, (scale, 0, 0, -scale, x0 + (x + pos.x_offset) * scale, baseline - pos.y_offset * scale))
        gs[order[info.codepoint]].draw(tp)
        x += pos.x_advance + tracking * upem
    return pen.getCommands(), x * scale


def svg(w, h, body, label):
    return (f'<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 {w:.1f} {h:.1f}" width="{w:.0f}" height="{h:.0f}" role="img" aria-label="{label}">'
            f'<title>{label}</title>{body}</svg>\n')


def lockup(kind, theme, tagline=True):
    p = f"{kind}{theme}"
    top, proxy = (C["cream"], C["proxy_dark"]) if theme == "dark" else (C["ink"], C["proxy_light"])
    tag = "#A0A9C0" if theme == "dark" else "#6B6264"
    size, gap = 56, 18
    d1, w1 = shape_path(FONTS / "Kanit-Bold.ttf", "TomYum", size, 64 + gap, 50)
    d2, w2 = shape_path(FONTS / "Kanit-Medium.ttf", "Proxy", size, 64 + gap + w1 + 1, 50)
    body = f'<defs>{grads(p)}</defs><g transform="translate(0 6)">{ICONS[kind](p)}</g><path d="{d1}" fill="{top}"/><path d="{d2}" fill="{proxy}"/>'
    width = 64 + gap + w1 + 1 + w2 + 4
    height = 76
    if tagline:
        d3, _ = shape_path(FONTS / "Kanit-Medium.ttf", "THAI NETWORK PROXY", 12.5, 64 + gap + 2, 70, tracking=0.16)
        body += f'<path d="{d3}" fill="{tag}"/>'
    return svg(width, height, body, "TomYumProxy")


def tile(kind, theme, small=False):
    p = f"{kind}t{theme}{'s' if small else ''}"
    bg = C["night"] if theme == "dark" else C["cream"]
    draw = (SMALL.get(kind) if small else None) or ICONS[kind]
    body = f'<defs>{grads(p)}</defs><rect width="64" height="64" rx="15" fill="{bg}"/><g transform="translate(6 6) scale(.8125)">{draw(p)}</g>'
    return svg(64, 64, body, "TomYumProxy")


def thai_lockup(kind):
    p = f"{kind}th"
    d1, w1 = shape_path(FONTS / "Kanit-Bold.ttf", "ต้มยำ", 50, 82, 52)
    d2, w2 = shape_path(FONTS / "Kanit-Medium.ttf", "พร็อกซี", 50, 82 + w1 + 8, 52)
    body = f'<defs>{grads(p)}</defs><g transform="translate(0 8)">{ICONS[kind](p)}</g><path d="{d1}" fill="{C["cream"]}"/><path d="{d2}" fill="{C["proxy_dark"]}"/>'
    return svg(82 + w1 + 8 + w2 + 4, 80, body, "ต้มยำพร็อกซี")


files = {}
for kind in ICONS:
    files[f"{kind}-icon.svg"] = svg(64, 64, f"<defs>{grads(kind)}</defs>{ICONS[kind](kind)}", "TomYumProxy")
    for theme in ("dark", "light"):
        files[f"{kind}-lockup-{theme}.svg"] = lockup(kind, theme)
        files[f"{kind}-tile-{theme}.svg"] = tile(kind, theme)
    files[f"{kind}-lockup-th-dark.svg"] = thai_lockup(kind)
    if kind in SMALL:
        for theme in ("dark", "light"):
            files[f"{kind}-favicon-{theme}.svg"] = tile(kind, theme, small=True)
for name, text in files.items():
    (OUT / name).write_text(text, encoding="utf-8")

# preview board: each direction at lockup size, as app tiles and at favicon sizes, on dark and light
rows = []
for kind, title in (("bowl", "1 · Steam Signal (ชามต้มยำ)"), ("chili", "2 · Chili Signal (พริก + สัญญาณ)"), ("pin", "3 · Hot Pin (หมุดไฟ)")):
    pick = lambda s, theme: f"{kind}-favicon-{theme}.svg" if kind in SMALL and s <= 32 else f"{kind}-tile-{theme}.svg"
    sizes = "".join(f'<img src="{pick(s, "dark")}" width="{s}" height="{s}" alt="">' for s in (16, 24, 32, 48, 64, 128))
    rows.append(f'<section><h2>{title}</h2><div class="pair"><div class="dark"><img src="{kind}-lockup-dark.svg" height="76" alt="">'
                f'<img src="{kind}-lockup-th-dark.svg" height="64" alt=""><div class="sizes">{sizes}</div></div>'
                f'<div class="light"><img src="{kind}-lockup-light.svg" height="76" alt=""><div class="sizes">'
                + "".join(f'<img src="{pick(s, "light")}" width="{s}" height="{s}" alt="">' for s in (16, 32, 64, 128)) +
                '</div></div></div></section>')
(OUT / "board.html").write_text(
    '<!doctype html><meta charset="utf-8"><title>TomYumProxy logo directions</title><style>'
    'body{margin:0;padding:32px;background:#0b0d12;color:#e8eaf0;font:14px "Segoe UI",sans-serif}h1{font-size:20px;margin:0 0 20px}'
    'section{margin-bottom:28px}h2{font-size:14px;font-weight:600;margin:0 0 10px;color:#c9d0e4}.pair{display:grid;grid-template-columns:1.25fr 1fr;gap:16px}'
    '.dark,.light{display:grid;gap:18px;padding:26px;border-radius:14px}.dark{background:#0E1116;border:1px solid #ffffff17}.light{background:#FFF8EE}'
    '.sizes{display:flex;gap:14px;align-items:flex-end}</style><h1>TomYumProxy — logo directions</h1>' + "".join(rows), encoding="utf-8")
print("\n".join(sorted(files)))
