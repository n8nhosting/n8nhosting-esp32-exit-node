# Portal international marketing

The operator requests full multilingual `/portal`, SEO/SEM/Open Graph and
localized keywords, with operator-managed analytics/pixels. Six languages were
confirmed: Thai, English, Simplified Chinese, Japanese, Russian and Vietnamese.
The previously approved dark identity and delegated implementation choices apply.

## Architecture and behavior

Keep Go + SQLite, the existing customer contracts and single-file admin console.
Use `/portal/{th,en,zh-CN,ja,ru,vi}/` with public plans/help pages rendered on the
server. This is preferable to JS-only metadata (poor crawler/share coverage) or
six separate applications (duplicate account/payment logic). Keep old `/portal/`
and hash links compatible. All account/auth/order/credential routes are noindex.

Every locale has real translated UI, formats, statuses, errors, help and public
copy; customer-authored plan names and descriptions remain authoritative data.
Use a visible language selector and preserve the current task when changing it.
No IP-based forced language redirects. Full document navigation separates public
marketing from private account pages so a tracking SDK cannot remain resident
on a page displaying credentials or payment/account forms.

Server metadata includes title/description, canonical URL, reciprocal hreflang,
OG/Twitter preview, locale-specific 1200x630 artwork and a public-only sitemap.
Only an explicitly configured HTTPS portal origin supplies absolute URLs; an
unconfigured origin stays noindex. Do not trust Host headers for canonical URLs.
Visible localized headings/body support the keyword intent. Meta keywords are
editorial configuration, not a claim of Google ranking benefit. SEM deliverables
are draft language-specific keyword/ad/negative-keyword groups and landing links;
no ad accounts, campaigns or spending are created.

## Settings and tracking

Migration v8 stores a single validated marketing settings record. Admin-only
GET/PUT `/api/marketing` manages locale/page SEO and typed provider IDs for GA4,
Google Ads, Meta, TikTok or a mutually exclusive GTM container. No arbitrary
HTML/JavaScript snippet fields. Public `/portal/api/marketing` returns only public
IDs/configuration revision. Provider configuration starts disabled. Changes are
audited after successful writes and outside the SQLite transaction.

Use basic consent: no analytics/advertising requests before a choice. Separate
analytics and advertising choices, reject as easily as accept, and provide a
persistent preferences control. Revision changes invalidate the saved choice.
Only unauthenticated public home/plans/help can load tags. Private documents keep
the restrictive existing CSP. Strip query/hash before loading a tag; explicit
events contain only a public route and locale, never identity, credentials,
order/slip details, token, search text or arbitrary URL parameters. Page views
are not purchase conversions. Real purchase conversions require separate verified
server-side provider work and are not invented from clicks/return redirects.

Google property enhanced measurement and external container auto collection must
be reviewed during operator activation. Typed IDs do not certify provider account
setup or prevent an operator's external GTM container from collecting extra data.

## Verification and deployment

Test migration from v7 and rollback, authenticated settings/persistence/escaping,
every locale URL without JS, hreflang/canonical/OG/sitemap/robots/private noindex,
full dictionary key parity, localized account/purchase/error flows, query/token
scrubbing, consent accept/reject/revoke/config-revision behavior, no provider loads
on private/authenticated pages, responsive screenshots and keyboard controls.
Keep synthetic provider/browser tests distinct from live vendor delivery.

Production deployment replaces the broker container and interrupts the current
hardware recorder's namespace. Preserve run-2 as interrupted evidence and start
a fresh full-duration recorder after deployment. Never label an interrupted run
as a passed 24-hour soak. No firmware change or release promotion is needed.
