"""Render the sales-page hero art: Thailand as a dot matrix with exit nodes.

Writes admin-api/portal/landing-map.svg, which the portal inlines into the
home page. The outline is a coarse hand-traced border (lon, lat), so the
dots read as Thailand without claiming survey accuracy. Styling comes from
portal.css classes (the portal CSP forbids inline styles).

    python tools/render_landing_map.py
"""
import math
import pathlib

BORDER = [  # clockwise from Mae Sai: Mekong, Cambodia, Gulf coast, Malaysia, Andaman coast, Myanmar
    (99.88, 20.43), (100.09, 20.35), (100.58, 20.17), (100.40, 20.26), (100.75, 19.95), (101.26, 19.58),
    (101.20, 18.80), (101.05, 17.90), (101.67, 17.90), (102.10, 18.20), (102.75, 17.88), (103.30, 18.20),
    (103.95, 18.33), (104.40, 17.85), (104.78, 17.40), (104.75, 16.55), (105.00, 16.10), (105.50, 15.32),
    (105.62, 14.90), (105.20, 14.33), (104.50, 14.38), (103.70, 14.38), (103.00, 14.25), (102.55, 13.60),
    (102.35, 13.00), (102.50, 12.60), (102.75, 12.20), (102.92, 11.65), (102.55, 12.05), (102.25, 12.40),
    (101.90, 12.60), (101.45, 12.68), (100.92, 12.67), (100.88, 12.95), (100.95, 13.35), (100.60, 13.52),
    (100.25, 13.45), (100.00, 13.30), (99.95, 12.80), (99.95, 12.40), (99.80, 11.80), (99.55, 11.20),
    (99.18, 10.50), (99.20, 9.90), (99.33, 9.14), (99.85, 9.20), (100.05, 8.60), (100.25, 8.40),
    (100.35, 7.70), (100.60, 7.20), (101.00, 6.95), (101.30, 6.90), (101.70, 6.70), (102.08, 6.25),
    (101.95, 5.85), (101.70, 5.75), (101.30, 5.60), (100.95, 6.25), (100.40, 6.65), (100.10, 6.50),
    (99.95, 6.85), (99.65, 7.15), (99.45, 7.55), (99.00, 7.90), (98.75, 8.10), (98.40, 7.80),
    (98.25, 8.40), (98.30, 9.00), (98.60, 9.90), (98.75, 10.35), (99.15, 11.20), (99.65, 11.80),
    (99.20, 12.50), (99.15, 13.20), (98.65, 14.00), (98.20, 15.00), (98.40, 15.30), (98.60, 15.80),
    (98.55, 16.70), (97.85, 17.60), (97.55, 18.30), (97.35, 18.55), (97.75, 19.35), (97.80, 19.60),
    (98.20, 19.75), (98.60, 19.85), (99.05, 19.85), (99.55, 20.15),
]
CITIES = {  # exit-node markers; Bangkok is the hub the arcs leave from
    "bangkok": (100.50, 13.75), "chiang-mai": (98.99, 18.79), "khon-kaen": (102.83, 16.44),
    "korat": (102.10, 14.97), "udon": (102.79, 17.41), "phuket": (98.39, 7.95), "hat-yai": (100.47, 7.00),
    "pattaya": (100.88, 12.93),
}
STEP = 0.24          # degrees between dots
SCALE = 30           # svg units per degree
LON0, LON1, LAT0, LAT1 = 97.0, 106.0, 5.3, 20.8
PAD_RIGHT = 150      # room on the right for the arcs to the customer side


def inside(lon, lat):
    hit = False
    for (x1, y1), (x2, y2) in zip(BORDER, BORDER[1:] + BORDER[:1]):
        if (y1 > lat) != (y2 > lat) and lon < x1 + (lat - y1) * (x2 - x1) / (y2 - y1):
            hit = not hit
    return hit


def xy(lon, lat):
    return round((lon - LON0) * SCALE, 1), round((LAT1 - lat) * SCALE, 1)


def main():
    width = round((LON1 - LON0) * SCALE) + PAD_RIGHT
    height = round((LAT1 - LAT0) * SCALE)
    dots = []
    lat = LAT1
    while lat > LAT0:
        lon = LON0
        while lon < LON1:
            if inside(lon, lat):
                dots.append(xy(lon, lat))
            lon += STEP
        lat -= STEP
    path = "".join(f"M{x:g} {y:g}h0" for x, y in dots)
    hx, hy = xy(*CITIES["bangkok"])
    arcs = []
    for i, ty in enumerate((height * 0.18, height * 0.42, height * 0.66)):
        tx = width - 24
        cx, cy = (hx + tx) / 2, min(hy, ty) - 120 + i * 40
        arcs.append(f'<path class="map-arc arc-{i + 1}" d="M{hx:g} {hy:g}Q{cx:.0f} {cy:.0f} {tx:.0f} {ty:.0f}"/>'
                    f'<circle class="map-peer" cx="{tx:.0f}" cy="{ty:.0f}" r="7"/>')
    nodes = []
    for name, (lon, lat) in CITIES.items():
        x, y = xy(lon, lat)
        hub = " hub" if name == "bangkok" else ""
        nodes.append(f'<g class="map-node{hub}"><circle class="map-pulse" cx="{x:g}" cy="{y:g}" r="7"/>'
                     f'<circle class="map-core" cx="{x:g}" cy="{y:g}" r="{6 if hub else 4}"/></g>')
    svg = (f'<svg class="landing-map" viewBox="0 0 {width} {height}" xmlns="http://www.w3.org/2000/svg" '
           f'focusable="false" aria-hidden="true">'
           f'<defs><linearGradient id="arcGlow" x1="0" x2="1"><stop offset="0" stop-color="#7c8cff"/>'
           f'<stop offset="1" stop-color="#f3c77a"/></linearGradient>'
           f'<radialGradient id="mapHalo"><stop offset="0" stop-color="#455ddc" stop-opacity=".35"/>'
           f'<stop offset="1" stop-color="#455ddc" stop-opacity="0"/></radialGradient></defs>'
           f'<circle cx="{hx:g}" cy="{hy:g}" r="{width * 0.42:.0f}" fill="url(#mapHalo)"/>'
           f'<path class="map-dots" d="{path}"/>{"".join(arcs)}{"".join(nodes)}</svg>\n')
    out = pathlib.Path(__file__).resolve().parent.parent / "admin-api" / "portal" / "landing-map.svg"
    out.write_text(svg, encoding="utf-8", newline="\n")
    print(f"{out}: {len(dots)} dots, {len(svg)} bytes, viewBox {width}x{height}")


if __name__ == "__main__":
    main()
