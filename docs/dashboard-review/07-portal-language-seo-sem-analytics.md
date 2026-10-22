# Portal languages, discoverability and measurement — 22 October 2026

This implementation extends the established customer portal with Thai, English,
Simplified Chinese, Japanese, Russian and Vietnamese. It preserves the existing
plans, identity, payment, own-account and per-board capacity contracts. This
document is the content and activation handoff, not evidence of indexed pages,
ad approval, provider delivery or an active campaign.

## Content and search contract

Each language has its own crawlable public URL, localized initial HTML title,
description, H1 and explanatory body. A public page must remain useful without
JavaScript; a translated navigation shell around an untranslated main body is
insufficient. Language links are ordinary links; do not redirect by IP or browser
language in a way that hides another language. All six variants list themselves
and each other with absolute reciprocal `hreflang` links, plus `x-default`.
Canonical URLs point to the corresponding language page, without query strings
or fragments. These rules follow [Google's localized-version guidance](https://developers.google.com/search/docs/specialty/international/localized-versions)
and [multilingual-site guidance](https://developers.google.com/search/docs/specialty/international/managing-multi-regional-sites).

Use keywords naturally in titles, H1, introductory copy, package explanations,
internal links and relevant FAQs. Do not repeat synonyms mechanically. A keyword
metadata field may serve editorial/export needs, but Google's `meta keywords`
tag has **no indexing or ranking effect**. See [Google's supported meta tags](https://developers.google.com/search/docs/crawling-indexing/special-tags).
No position, traffic, CPC, search volume or conversion performance is promised.
The matrix below is a set of editorial and advertising hypotheses, not a Keyword
Planner export or evidence of native-speaker search demand.

Public landing pages can be indexed; account, sign-in, verification, orders,
credentials, receipts and portal API resources remain private/noindex. A
`noindex` response is not authentication and a robots.txt disallow cannot replace
it: a crawler must be able to read the directive to apply it. See [Google's
noindex documentation](https://developers.google.com/search/docs/crawling-indexing/block-indexing).
Never put customer or token-bearing URLs in a sitemap, canonical, hreflang,
structured data or share metadata.

Use `WebSite`/`Organization` structured data only for facts actually present.
Do not publish invented ratings, testimonials, stock, an exclusive IP, fixed IP,
an SLA or a frozen draft price. Dedicated reserves one board, including when
other boards share its public IP. Rate limits are caps; actual throughput depends
on the measured board/network. The seller baseline is [PRODUCT.md](../../PRODUCT.md)
and the [commerce acceptance report](06-platform-commerce-acceptance.md).

## Six-language editorial and SEM matrix

Recommended public destinations are `/portal/th/`, `/portal/en/`,
`/portal/zh-CN/`, `/portal/ja/`, `/portal/ru/` and `/portal/vi/`.
Keep the corresponding locale in the final URL of an ad. These translations are
implementation-ready drafts; local-language commercial review and real search
term data should refine them before paid expansion.

### ภาษาไทย — th

- Primary intent: `พร็อกซีไทย`, `เช่า proxy ไทย`.
- Supporting intent: `พร็อกซีเครือข่ายไทย`, `แพ็กเกจ proxy`, `พร็อกซีแบบแชร์`, `พร็อกซีบอร์ดเฉพาะ`.
- Title: **พร็อกซีไทยและแพ็กเกจเครือข่ายไทย | Proxy Server**.
- H1: **พร็อกซีเครือข่ายไทย จัดการได้ในบัญชีเดียว**.
- Description: เลือกแพ็กเกจพร็อกซีเครือข่ายไทย ดูการใช้งานและต่ออายุในบัญชีของคุณ ตรวจสอบข้อจำกัดความเร็วและความพร้อมก่อนสั่งซื้อ.
- Draft ad headlines: `พร็อกซีเครือข่ายไทย` / `เลือกแพ็กเกจ Proxy` / `ดูการใช้งานในบัญชี`.
- Draft ad descriptions: `เลือกแพ็กเกจตามการใช้งาน ดูเงื่อนไขและความพร้อมก่อนสั่งซื้อ` / `ดูการใช้งานและต่ออายุผ่านบัญชีของคุณ IP อาจเปลี่ยนตามผู้ให้บริการ`.
- Initial negative concepts: `ฟรี`, `แจก proxy`, `แคร็ก`, `แฮก`, `สแปม`, `หลบแบน`, `ดาวน์โหลดหนัง`.
- Visible FAQ intent: Dedicated ต่างจาก Shared อย่างไร? IP คงที่หรือไม่? ความเร็วจริงขึ้นอยู่กับอะไร?

### English — en

- Primary intent: `Thai proxy`, `Thailand network proxy`.
- Supporting intent: `Thailand residential network proxy`, `Thai proxy plans`, `shared Thai proxy`, `dedicated board proxy`.
- Title: **Thai network proxy plans | Proxy Server**.
- H1: **Thai network proxies, managed in your account**.
- Description: Explore Thai network proxy plans, review usage and renew in your own account. Check rate limits, measured capacity and availability before purchase.
- Draft ad headlines: `Thai Network Proxies` / `Compare Proxy Plans` / `Manage Access In One Place`.
- Draft ad descriptions: `Explore Thai network proxy plans. Check capacity and terms before buying.` / `Review usage and renew in your account. ISP-assigned IP addresses may change.`
- Initial negative concepts: `free`, `cracked`, `hack`, `spam`, `ban evasion`, `torrent`, `public proxy list`.
- Visible FAQ intent: What does dedicated reserve? Is the exit IP fixed? Why can throughput vary?

### 简体中文 — zh-CN

- Primary intent: `泰国代理`, `泰国网络代理`.
- Supporting intent: `泰国住宅网络代理`, `泰国代理套餐`, `共享代理套餐`, `独享设备代理`.
- Title: **泰国网络代理套餐 | Proxy Server**.
- H1: **泰国网络代理，账户管理一目了然**.
- Description: 查看泰国网络代理套餐，在您的账户中查看用量并续费。购买前请确认限速、设备实测容量和可用情况。
- Draft ad headlines: `泰国网络代理` / `查看代理套餐` / `账户管理更清晰`.
- Draft ad descriptions: `选择适合需求的泰国代理套餐。购买前查看容量与使用条款。` / `在账户中查看用量并续费。运营商分配的 IP 地址可能变化。`
- Initial negative concepts: `免费`, `破解`, `黑客`, `垃圾邮件`, `绕过封禁`, `公共代理列表`, `盗版`.
- Visible FAQ intent: 独享套餐预留什么？IP 地址固定吗？实际速度为何会变化？

### 日本語 — ja

- Primary intent: `タイ プロキシ`, `タイ ネットワーク プロキシ`.
- Supporting intent: `タイ 住宅回線 プロキシ`, `タイ プロキシ プラン`, `共有プロキシ`, `専用ボード プロキシ`.
- Title: **タイのネットワークプロキシプラン | Proxy Server**.
- H1: **タイのネットワークプロキシを、アカウントで管理**.
- Description: タイのネットワークプロキシプランを比較。利用量の確認と更新をアカウントで行えます。購入前に速度制限、実測容量、空き状況をご確認ください。
- Draft ad headlines: `タイのプロキシ` / `プランを比較` / `利用量を確認`.
- Draft ad descriptions: `タイのネットワークプロキシ。購入前に容量と条件をご確認ください。` / `アカウントで利用量を確認し更新できます。IPは変更される場合があります。`
- Initial negative concepts: `無料`, `クラック`, `ハッキング`, `スパム`, `BAN回避`, `公開プロキシ一覧`, `海賊版`.
- Visible FAQ intent: 専用プランで何が予約されますか？IPは固定ですか？実効速度は何で変わりますか？

### Русский — ru

- Primary intent: `прокси Таиланд`, `тайские прокси`.
- Supporting intent: `прокси тайской сети`, `тарифы прокси Таиланд`, `общие прокси Таиланд`, `прокси с выделенной платой`.
- Title: **Прокси тайской сети: тарифы | Proxy Server**.
- H1: **Прокси из Таиланда — управление в вашем кабинете**.
- Description: Выберите тариф прокси тайской сети, следите за трафиком и продлевайте доступ в кабинете. До покупки проверьте ограничения скорости и наличие мест.
- Draft ad headlines: `Прокси из Таиланда` / `Сравните тарифы` / `Трафик в личном кабинете`.
- Draft ad descriptions: `Тарифы прокси тайской сети. Проверьте доступность и условия перед покупкой.` / `Смотрите расход трафика и продлевайте доступ. IP провайдера может измениться.`
- Initial negative concepts: `бесплатно`, `взлом`, `спам`, `обход бана`, `торрент`, `список бесплатных прокси`, `пиратский`.
- Visible FAQ intent: Что резервирует выделенный тариф? Постоянный ли IP? От чего зависит скорость?

### Tiếng Việt — vi

- Primary intent: `proxy Thái Lan`, `proxy mạng Thái Lan`.
- Supporting intent: `proxy mạng dân cư Thái Lan`, `gói proxy Thái Lan`, `proxy dùng chung`, `proxy bo mạch riêng`.
- Title: **Gói proxy mạng Thái Lan | Proxy Server**.
- H1: **Proxy mạng Thái Lan, quản lý trong tài khoản của bạn**.
- Description: Chọn gói proxy mạng Thái Lan, xem mức sử dụng và gia hạn trong tài khoản. Kiểm tra giới hạn tốc độ, dung lượng đo thực tế và chỗ trống trước khi mua.
- Draft ad headlines: `Proxy Mạng Thái Lan` / `So Sánh Các Gói Proxy` / `Xem Mức Sử Dụng`.
- Draft ad descriptions: `Chọn gói proxy mạng Thái Lan. Kiểm tra chỗ trống và điều khoản trước khi mua.` / `Xem mức sử dụng và gia hạn trong tài khoản. IP do nhà mạng cấp có thể thay đổi.`
- Initial negative concepts: `miễn phí`, `bẻ khóa`, `hack`, `spam`, `né lệnh cấm`, `danh sách proxy miễn phí`, `lậu`.
- Visible FAQ intent: Gói riêng đặt trước gì? IP có cố định không? Tốc độ thực tế phụ thuộc vào đâu?

## Advertising handoff

Use one language per ad group with the corresponding localized landing page;
separate brand intent from network/proxy-plan intent. Start with tightly scoped
phrase/exact concepts, then review actual search terms before expanding. This
is an implementation recommendation, not a claim that broad match is invalid:
[Google explains the match options and their behavior](https://support.google.com/google-ads/answer/7478529?hl=en).
Review [negative keyword matching](https://support.google.com/google-ads/answer/2453972?hl=en)
because negative match behavior differs from positive matching. Add translations,
spellings and irrelevant technical-support intent only when the real query data
supports exclusion; avoid excluding the legitimate word `proxy` itself.

The drafts contain three headlines and two descriptions per locale. Google
responsive search ads permit 30-character headlines and 90-character descriptions;
Chinese/Japanese double-width characters count as two. Validate in the live Ads
editor before publishing and add more distinct assets after language review.
See [official RSA specifications](https://support.google.com/google-ads/answer/7684791?hl=en).
No ad account, campaign, billing, budget or spend is created by this repository.
Keyword Planner volumes, geography, budget and approved policy eligibility remain
merchant decisions. Do not market evasion, deception or unapproved use cases.

Do not call a CTA click, account request, Checkout redirect, uploaded slip or
unverified callback a purchase. The portal's existing backend payment proof
remains authoritative. A future advertising conversion must originate from that
verified transition, have durable deduplication and respect consent. Provider
purchase events are intentionally outside the current public-page measurement
boundary. [GA4's event definitions](https://support.google.com/analytics/answer/9267735?hl=en)
distinguish beginning checkout from completing a purchase.

## Open Graph artwork

Six locale-specific **1200 × 630 PNGs** are generated with code-native SVG and
the existing dark palette. Sources live beside the raster files at
`admin-api/portal/og/portal-<locale>.{svg,png}`. Run
`node tools/render_portal_og.cjs` on a machine with the documented Playwright
Chromium and Thai/CJK fonts to reproduce them. No generated photo, remote font,
customer screenshot or third-party logo is included.

Use absolute HTTPS `og:url` and `og:image`, localized `og:title`, description,
locale and image alt text; set image type `image/png`, width `1200`, height `630`.
Also emit a large-image social card. The image is shared only with its public
language page and contains no draft price or invented metric. The [Open Graph
protocol](https://ogp.me/) defines the required title/type/image/url fields and
structured image metadata. Social crawlers need real initial HTML; client-only
head mutation is not sufficient evidence of a working share preview.

## Analytics and pixel activation

The backend manages provider identifiers and explicit enable switches, not
arbitrary JavaScript/HTML. IDs are public routing identifiers, not API secrets.
Reject unsupported IDs, URLs, script markup and control characters. Defaults
remain disabled. Do not put third-party code on admin pages or private account,
sign-in, verification, receipt, order or connection-detail pages.

Consent categories are independent analytics and marketing. Necessary identity,
security, CSRF and consent-preference storage remain available without optional
consent. Reject-all must be as accessible as accept-all. Show the current choice
and let visitors reopen and withdraw it. Denial, missing consent, failed storage
or unavailable settings must fail closed for optional tracking. Use basic
consent mode: no provider script, pixel, iframe, preconnect or cookieless event
before the relevant opt-in. Google distinguishes [basic and advanced consent](https://developers.google.com/tag-platform/security/concepts/consent-mode);
advanced mode can send pings despite denied storage, so it is not this default.

Set Google consent states before configurations and update them when the choice
changes, including grant-to-denied. Keep advertising user data and personalization
denied when not expressly supported; disable Google signals and advertising
personalization for analytics-only use. The app must not infer marketing consent
from analytics consent. See [Google's consent implementation guide](https://developers.google.com/tag-platform/security/guides/consent).

### Provider and CSP reference

| Provider | Source or destination | Activation constraint |
|---|---|---|
| Google tag / GA4 | `https://www.googletagmanager.com/gtag/js?id=G-…`; collection under `*.google-analytics.com` | Analytics opt-in; manual allowlisted public page events; disable auto page/history/form/search collection in property |
| Google Ads | Google tag, `www.googleadservices.com`, `www.google.com`, `googleads.g.doubleclick.net`, `pagead2.googlesyndication.com`, `ad.doubleclick.net` | Marketing opt-in; no purchase event without verified backend money; linked Ads features require additional CSP |
| Google Tag Manager | `https://www.googletagmanager.com/gtm.js?id=GTM-…` | Marketing opt-in and explicit operator container control; tags can execute arbitrary logic, so consent and public-scope enforcement need container review |
| Meta Pixel | `https://connect.facebook.net/en_US/fbevents.js`; events commonly to `www.facebook.com/tr/` | Marketing opt-in; no automatic advanced matching or DOM/form scraping; official implementation/reference fetch returned 429 during research, so do not describe undocumented suppression flags as verified |
| TikTok Pixel | `https://analytics.tiktok.com/i18n/pixel/events.js`; SDK-selected collection origins | Marketing opt-in; no advanced matching; reviewed cookie-consent API; production must verify current endpoint set before enabling |

Google publishes the per-feature CSP directives in its [official CSP guide](https://developers.google.com/tag-platform/security/guides/csp).
Keep the current restrictive policy on private responses and add only the
origins needed for explicitly enabled public features. Do not add wildcard
scripts, `unsafe-eval` or arbitrary Custom HTML to make a broken container work.
Preview/debug resources are not production requirements. A restrictive CSP can
block optional delivery safely; that is preferable to silently widening access.

TikTok's [official cookie guide](https://ads.tiktok.com/resources/help/article/how-to-use-cookies-with-tiktok-pixel)
requires a mechanism to stop cookie behavior on decline where consent applies;
its [consent API documentation](https://business-api.tiktok.com/portal/docs/pixel-cookie-consent-mode/v1.3)
shows hold/grant/revoke operations. A recent [official IPv6 endpoint note](https://ads.tiktok.com/resources/help/article/about-ipv6-support-for-tiktok-pixel?lang=en)
lists an additional origin, demonstrating why current network validation is
necessary instead of assuming a static endpoint list. That origin is not a
reason to broaden this application's policy preemptively.

### Data boundary and operator settings

Only a locale, a fixed public page name and an allowlisted public action belong
in frontend measurement. Never send email, account/customer/user/order/session
IDs, tokens, password, proxy connection details, MAC, arbitrary form values,
free-text notes, raw query strings, fragments, full referrer or error text.
Construct a clean same-origin public page URL; don't pass `location.href`.
An arbitrary UTM value can also contain PII, so discard it rather than blindly
forwarding it. Consent authorizes the stated purpose; it does not make secrets
safe to transmit.

Third-party SDKs can inspect a live document outside the app's event wrapper.
Therefore a sanitized event payload alone is insufficient. Do not load SDKs in
a document that later renders private forms/details. Use full navigation to a
fresh private document before login, verification or an account route, and do
not retain provider scripts/listeners across that boundary. A magic-link URL
must never enter an SDK-bearing page. A content-security header on the initial
public document cannot be tightened retroactively merely by changing a hash.

**GA4 property activation step:** disable Enhanced Measurement's page changes
based on browser history, forms, site search and other automatic collection
before enabling. Setting `send_page_view:false` only suppresses the configuration
pageview; it does not disable enhanced history events. This is explicitly
documented in [GA4 manual pageview guidance](https://developers.google.com/analytics/devguides/collection/ga4/views).
Do not invent an `enhanced_measurement:false` gtag option as a replacement.
Keep automatic data collection/advanced matching disabled in Meta/TikTok/GTM
provider interfaces as well. App tests with mocked SDKs verify the application's
calls, not the changing internals of downloaded third-party code.

Withdraw consent by updating applicable provider consent, blocking subsequent
events, clearing only relevant optional app/provider cookies where accessible,
and reloading into a clean document. Removing a script element alone does not
unregister listeners or timers. Provider delivery, browser blocking and privacy
review must be checked with the owner's real IDs after configuration. No
analytics ID, merchant secret or campaign is invented for acceptance.

## Acceptance and production checks

1. For all six public URLs, fetch initial HTML with JavaScript disabled. Verify
   meaningful localized H1/body/title/description, one self-canonical and six
   reciprocal alternate links plus x-default. Check valid UTF-8, lang, localized
   FAQ constraints and HTTP status. Query/hash inputs cannot alter canonical or
   insert markup. Unknown locales must not serve an indexable duplicate.
2. Fetch the sitemap and robots policy; only public canonical pages appear.
   Private/account/API responses include noindex and require identity as before.
   Search Console URL Inspection after deployment verifies crawler-visible HTML;
   submission and indexation are separate outcomes.
3. Request all six OG URLs anonymously; verify PNG signature, 1200×630, MIME,
   absolute share metadata and localized image alt. Open the raster files and
   check Thai/CJK/Russian/Vietnamese text for clipping or missing glyphs.
4. Use 1440×900 and 375×812 for every locale, including a long dynamic package
   name, zero capacity, error, login disabled, authentication expiry, order/slip,
   account usage and renewal. Check keyboard, language selector accessible name,
   preserved safe route, focus, reduced motion and no horizontal page overflow.
5. With all providers enabled and mocked IDs, assert **zero** provider network
   requests before choice or after reject. Grant analytics only: no ad provider.
   Grant marketing only: no GA4 analytics event. Change choices, reload and
   verify persistence. Corrupt stored consent must not become consent.
6. Inject synthetic secrets into query/hash/referrer, forms and mocked API data.
   Confirm no provider requests on token/private pages and no secrets in any
   emitted payload. Navigate from an opted-in public page to sign-in/account:
   the old SDK-bearing document must be replaced before private content appears.
7. With owner-configured test IDs, inspect real provider requests and dashboards
   on public pages only. Verify property automatic collection is disabled,
   withdrawal stops later requests, blocked CSP causes honest non-delivery and
   no duplicate direct/GTM installation. Do not send a fake purchase event.

## Evidence and limitations

Official primary documentation above was checked on 22 October 2026. Google
and TikTok guides were accessible; Meta developer documentation was throttled
with HTTP 429, so its current SDK-specific suppression controls are not claimed
as verified. Internal context came from the supplied repository and acceptance
reports; CRM, private message and ad-account connectors were unnecessary for
this bounded implementation. No search-volume source or native-language
commercial reviewer was consulted. Public copy is a truthful draft, provider
network tests require owner IDs, and no indexing/ranking/ad-spend result is
inferred from local code or a deployment. The renderer successfully produced
all six 1200×630 PNGs; visual checks include every script family.

## Implemented controls and local acceptance

Admin **Settings → Portal marketing** edits SEO overrides per language and page,
previews the default or overridden title/description, and exports an 18-row CSV
with localized keyword themes and landing paths. Empty overrides retain the
translated defaults. The editor keeps unsaved values when changing language/page;
stale concurrent saves return 409 rather than overwrite another operator.

Typed IDs are supported for GA4, Google Ads, Meta Pixel, TikTok Pixel or an
exclusive GTM container. The Google Ads conversion label is stored for a future
verified-conversion integration; this release emits no purchase conversion.
All integrations start disabled and blank. Analytics and advertising choices are
independent; GTM requires both and operator-managed consent inside its container.
Configuration revisions require fresh consent. Withdrawal clears accessible
optional cookies and reloads a document without vendor listeners. A failed
identity check is unknown and does not authorize tracking.
Only clean public paths and locale enter app-managed measurement. UTM values,
ad click identifiers and arbitrary query/fragment values are not forwarded;
cross-page campaign attribution is not implemented in this release. SEM output
is landing-page content and editorial/export drafts, not an activated campaign
or a verified conversion pipeline.

The 212-key dictionaries contain 1,272 translations. Fixed UI, statuses,
validation, account/slip/receipt flows, dates/numbers/THB and public SEO copy are
localized. Operator-entered plan names/descriptions, bank details and customer
data stay byte-for-byte authoritative; they are not silently machine-translated.

Final local results:

- Final admin: 278 cases / 149 top-level tests pass (13.199s); all 278 under race
  pass (47.610s); vet passes. This includes a final correction for localized
  private-page metadata across 36 routes. The full race run used the same Go
  source before the final legacy-entry JavaScript correction. Broker tests (cached) and vet pass;
  broker source unchanged.
- I18n helper: 6 groups pass for key parity, placeholders, routing and privacy.
- Marketing browser suite: 11 groups pass, 14 screenshots. Real Go/SQLite/SSR
  with inert locally fulfilled SDKs; zero uncaught JS errors/real vendor requests.
  Includes bare/index legacy URLs, remembered language, verification-token
  transfer and sensitive-query tracking suppression.
- Final commerce/account suite: 19 groups pass, 22 screenshots, including all six
  private-language journeys and the original 13 groups. Explicitly substituted
  customer APIs use synthetic records. All six public language links measure
  44px tall after the final CSS correction; desktop/mobile have no page overflow.
- Static Impeccable: no non-advisory findings. Dashboard has 83 inherited token
  advisories; portal has 11 typography/print-color advisories. Not a full WCAG audit.

See [scoped evidence and screenshots](evidence-2026-10-22-portal/README.md).
Source, deployment and hardware collection state are recorded separately below.

## Production delivery and follow-up

Runtime source: `7a6f8843e8ff9741de7745fe241b8ce6b66836fa` on `main`.
Coolify deployment `jokgjuch6v5y7lvkjz9iuyjo` reports **Success**. Its import log
identifies that exact source; all four services started by **22 October 2026,
13:25:38 +07**. Current broker, admin-api, gost and flasher container names end
in `20261022T062427`. Later evidence/documentation commits do not replace this
runtime unless a new deployment is deliberately started.

The public, non-secret `PORTAL_BASE_URL` is now explicitly configured as
`https://admin.jbacteam.com/portal/`; canonical URLs never trust request headers.
The pre-v8 online SQLite backup passed integrity/schema/count checks and is
retained privately on the server at
`/root/exitnode-pre-v8-20261022-portal/platform.db` (directory 0700, file 0600).
The live database passes integrity checks at schema 8, with 4 legacy users,
4 device records, 3 draft plans and no customers, subscriptions or orders.
All four legacy user contracts match the backup.

Production checks on this runtime passed all 18 initial public HTML pages
(canonical, seven hreflang links, OG and meaningful heading/body), all 36 private
localized titles/noindex/no-canonical/no-OG responses, and an 18-URL public
sitemap. Cookie-free HTTP requests return 200 for the public portal and
`/robots.txt`. Browser checks at 1440x900 and 375x812 show no horizontal page overflow.
Both bare `/portal/` and `/portal/index.html` return to the remembered English
clean URL with matching English UI and metadata. The selected production
screenshots show the real unconfigured service, not the synthetic account or
fake tracking identifiers used in local tests. Full-page capture timed out in
the live browser; production images are viewport captures.

Marketing remains `enabled:false`, with empty analytics and SEO overrides.
The app sends no real provider traffic and created no ad campaign or spend.
The edge may inject its own Cloudflare beacon markup; application CSP does not
allow `static.cloudflareinsights.com`, and no edge security/analytics setting
was weakened. This application verification is not a claim about all
infrastructure telemetry.

Login, Stripe and bank-transfer readiness remain false pending the owner's
email, Turnstile and payment configuration. No secrets, live customer records,
real payment, email or purchase event were used for acceptance. Native-language
commercial review, Search Console crawl/indexation and consented real-provider
network/dashboard verification remain operator activation checks. These checks
are not prerequisites for browsing the six-language public content.

One concurrent, audited `device.measured` event from the previous runtime at
**13:07:18 +07** changed board AEC0 capacity from 2.4 to 1.4 Mbps while the first
portal deployment was building. The portal work issued no production measure
request. The current measured result is preserved; routing, names and board
kinds are unchanged. This is an explicit exception to any claim that every
board setting matches the earlier backup.

### Hardware evidence continuity

No firmware was built, flashed or promoted in this portal task. All three boards
remain online on testing firmware 2.4.25 with OTA status `ok`; stable 2.4.21 and
the prior archives remain available. Broker replacement reconnects mux links
without a firmware update. Each collector is scoped to its broker's container
network namespace, so every runtime deployment needs a new collector directory.

| Run | Interval (+07) | Samples / failed polls | Result |
|---|---|---|---|
| 2 | 22 Oct 06:11:17 to 13:06:57 | 416 / 0 | Interrupted; 6h55m40s, all three online in all samples, zero uptime/counter resets |
| 3 | 22 Oct 13:11:32 to 13:15:51 | 5 / 0 | Interrupted before metadata correction deployment |
| 4 | 22 Oct 13:19:04 to 13:24:03 | 5 / 0 | Interrupted before legacy-route correction deployment |
| 5 | **22 Oct 13:26:02 onward** | First 2 / 0 | **Running**, all three online in the first two samples |

Interrupted runs explicitly record `completed:false` and remain private server
evidence. Current container: `exitnode-soak-20261022-v2425-i18n`, output
`/root/exitnode-soak-20261022-0550/evidence/run-5/`, started
`2026-10-22T06:26:02.944Z`; expected duration finish is **23 October 2026,
13:26:02 +07** if uninterrupted. A 24-hour hardware pass is **not** claimed.
Read actual results after completion; a future configuration deployment that
replaces the broker must start run 6 or another new output directory.
