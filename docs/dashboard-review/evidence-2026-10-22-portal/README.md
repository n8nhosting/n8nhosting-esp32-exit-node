# Portal language and marketing evidence

Unless explicitly marked production below, these artifacts are local development
evidence, independent of deployment, search indexation, ad approval, provider
dashboards or real tracking delivery.
No real campaign, customer, payment, email or analytics property was used.

## Verified local assets and editorial drafts

`og-editorial-verification.json` records six PNG signatures, 1200×630 dimensions,
SHA-256 hashes and visual inspection. The raster and editable SVG sources are
in `admin-api/portal/og/`; `tools/render_portal_og.cjs` reproduces them without
remote requests. All six script families were visually checked for missing
glyphs and clipping. Thirty draft advertising text assets fit the initial
30/90-character limits, counting Chinese/Japanese full-width characters twice.
The actual ad editor and native-language review remain activation checks.

## Integrated browser acceptance

`portal-marketing-acceptance.json` records **11 passing groups**, 14 screenshots,
zero uncaught JavaScript errors and zero unexpected external requests from
`tools/portal_marketing_smoke.cjs`. Its scope includes the real Go service and
isolated SQLite, admin save/reload/conflict/validation/CSV, 18 initial HTML pages,
all six languages, anonymous OG/sitemap requests, independent consent choices,
private document boundaries, withdrawal, configuration revisions and exclusive
GTM integration. The property IDs in the screenshots are deliberately fake.

Every Google/Meta/TikTok SDK request was fulfilled with a local inert script.
The harness checked the application's queued payloads and canonical TikTok
bootstrap fields/instance queue; no downloaded vendor SDK was executed. The
authenticated-identity and identity-error cases explicitly substitute `/me` API
responses. These checks do not prove the internals of a future third-party SDK,
provider-side automatic collection settings, real session delivery or a paid
conversion. A click/redirect/slip never becomes a purchase event.

The selected `admin-settings-*-real-api.png` images show the real local admin
editor at 1440×900 and 375×812 with six test SEO overrides and fake identifiers.
The full local image set remains under `output/playwright/portal-marketing-*`.
The final 11-group run uses runtime source `7a6f884` and includes eight legacy
root/index scenarios: remembered English/Japanese, exactly-once verification
tokens, and sensitive queries with accepted preferences that still load no SDK.
The selected admin screenshots were refreshed from that same final run.

Configuration of real provider IDs, inspection of real consented network
traffic, Search Console crawl checks and deployment verification are separate
gates. Missing operator configuration keeps optional integrations disabled.

## Final account regression

`commerce-i18n-acceptance.json` records **19 passing groups** against embedded source `7419069`: the original 13 commerce/interface groups plus six localized
private account journeys. All 22 screenshots are listed in the manifest; the
selected Japanese account screenshot explicitly uses intercepted synthetic
customer APIs. Credentials, banks, receipts and identities are fixtures.
`portal-th-public-mobile-real-api.png` shows the final CSS (unchanged by later route fixes) with the real isolated
API in its unconfigured state. Every language link measured 44px tall.

`local-verification.json` records the latest 278 admin Go cases, full race checks on the
same Go source before the tiny legacy JavaScript correction, vet,
dictionary/route helper checks and the static design scan. These are test counts,
not proof of live vendor delivery, translations reviewed by native speakers,
indexation, campaign performance or completed hardware acceptance.


## Production evidence

`production-portal-th-desktop.jpg` and `production-portal-en-mobile.jpg` are
viewport captures of the actual public service after runtime `7a6f884` deployed,
with DOM viewports set to 1440x900 and 375x812 respectively; returned JPEG
rasters are 1376x900 and 360x780. Full-page capture timed
out; no synthetic records or fake provider IDs appear in these production images.
`production-verification.json` separates live checks and deployment identity from
local tests. Real provider delivery, search indexation and 24-hour hardware
completion are not claimed. Current runtime and interrupted-collector details
are in report 07; later docs-only pushes do not deploy or reset collection.
