# Portal locale and SEM content pre-review — 22 October 2026

**This is an AI pre-review. It is not a native-speaker, legal or editorial
sign-off.** It gives native reviewers and the owner a prioritized list to
check before paid marketing. No locale, portal or Go file was changed, and
nothing was deployed. Suggested wordings are reviewer proposals, not decided text.

- Source reviewed: `main` at `4524938`: `admin-api/portal/locales/{th,en,zh-CN,ja,ru,vi}.json`
  (212 keys each), the "Six-language editorial and SEM matrix" in
  [report 07](07-portal-language-seo-sem-analytics.md), [PRODUCT.md](../../PRODUCT.md)
  for the claim boundaries, and `admin-api/portal/portal.js` for how strings are
  assembled.
- Method: one read-only reviewer per language, compared against `en.json` (the
  source the keys were derived from). The lead reviewer then re-read the High
  findings against the files. Items re-read that way are marked ✓.
- Mechanical checks: `node --test tools/portal_i18n_test.cjs` passes (6 groups,
  212 keys × 6). Every 07 draft RSA asset fits Google's limits, with CJK
  characters counted as 2: headlines 12–26 of 30 and descriptions 54–79 of 90.
  Vietnamese assets are measured in NFC. Retyped with a decomposed-Unicode keyboard
  mode, the two descriptions grow to 94 and 97 characters, so paste them as NFC.
- Out of scope: real search volume or CPC (every keyword note is a hypothesis),
  legal adequacy of the consent and AUP text, rendered layout, and
  operator-entered plan names and customer data (intentionally untranslated).

## สรุปสำหรับเจ้าของ

ไม่พบการสัญญา fixed IP, exclusive IP, ความเร็วรับประกัน หรือ SLA ในภาษาใด และทุกภาษาบอกว่าสลิป
หรือการกลับจาก Stripe ไม่ใช่หลักฐานการชำระเงิน สิ่งที่ควรตัดสินใจหรือแก้ก่อนเปิดขาย/โฆษณา:

1. **ข้อบกพร่องในโค้ด (C1):** หลังลูกค้าอัปโหลดสลิป รายการยังแสดง "รอชำระ" และมีปุ่มอัปโหลดอีกครั้ง
   ลูกค้าอาจโอนซ้ำ ต้องแก้ในรอบ source ไม่ใช่แก้คำแปล
2. **คำว่า "บอร์ด" ในภาษารัสเซีย (C14):** `плата` อ่านเป็น "ค่าธรรมเนียม/การชำระ" ได้ เช่น "ได้รับเงินแล้ว
   รอการชำระ" ควรเปลี่ยนเป็น `устройство` ก่อนทำการตลาดภาษารัสเซีย
3. **ข้อความภายในที่ลูกค้าเห็น (C2):** หน้า Help แสดง "นโยบายธุรกิจยังต้องตรวจก่อนเปิดรับเงินจริง" ตลอดเวลา
4. **Keyword ที่สื่อว่าได้ IP เฉพาะ (C11) และคำว่า residential (C12):** ต้องตัดสินใจเชิงธุรกิจก่อนใช้ในโฆษณา
5. **แหล่งข้อความ SEO มีสองชุด (C13):** title/H1 ในรายงาน 07 ไม่ตรงกับที่ใช้งานจริง ควรเลือกชุดเดียว
6. คำแปลรายภาษาด้านล่างให้ผู้ตรวจเจ้าของภาษายืนยันก่อนแก้ แล้วแก้ใน source round ที่มี test
   หลัง soak run 5 ครบ (23 ต.ค. 13:26 +07) เพราะ deploy จะเปลี่ยน broker container

## Cross-language findings

These affect several languages or come from the source copy or code.
Fix them once, at the source, before the per-language polish.

| ID | Finding | Locales | Evidence | Recommended change | Type | Sev |
|---|---|---|---|---|---|---|
| C1 ✓ | After a slip upload the order still shows **Awaiting payment** and the upload button again. **Receipt under review** is never reached. A second upload gets a 409 error, but the customer may transfer money again. | all | `payments_slip.go` `handleSlipUpload` returns `status:"awaiting_review"` only in the upload response. `recordCommerceSlip` (`commerce.go:583`) inserts the slip and leaves `orders.status='pending'`. `listCommerceOrders` (`commerce_http.go:156`) returns `orderCols` with no slip state. `orderPaymentButtons` (`portal.js`) shows the slip button for every `pending` order. The browser smoke intercept (`tools/dashboard_commerce_smoke.cjs:87`) sets `awaiting_review` itself, which hides the gap. | Expose slip state on the customer order list and render `ui.receipt.under.review` instead of the upload button. Add a regression test against the real handler, not the intercept. Interim copy: "Slip submitted. An admin will review it; do not transfer again." | code | High |
| C2 ✓ | An internal pre-launch note is always shown to customers on Help: "· Full business policies and provider details require review before real payments open." | all | `ui.full.business.policies.and.provider.details.require.review.before`, rendered unconditionally in the Help page (`portal.js:118`) | Show it only while payment readiness is false, or replace it with customer-facing text. Owner decides whether it was deliberate. | code + copy | High |
| C3 | "Receipt" names both the customer's uploaded transfer proof and the provider's issued payment receipt, which implies an upload proves payment. | en, zh-CN (`凭证`), ru (`квитанция`), vi (`biên lai`/`biên nhận`). Thai (สลิป/ใบรับเงิน) and Japanese (振込明細/領収書) already separate them. | `ui.bank.transfer.receipt.upload`, `ui.transfer.upload.receipt`, `ui.receipt.file`, `ui.receipt.under.review`, `ui.payment.receipt`, `seo.plans.faq.answer` | Source: "transfer slip" for the upload, "payment receipt" only for the issued document. Then zh `转账凭证`/`付款收据`, ru `чек о переводе`/`квитанция об оплате`, vi `chứng từ chuyển khoản`/`biên nhận thanh toán`. | copy | High |
| C4 | The AUP consent checkbox is built from fragments (`ui.i.accept` + `ui.acceptable.use.policy` + `ui.version` + `ui.current` + `ui.and.will.not.use…`). That breaks grammar in the legal consent line: wrong case in ru, `接受`/`ยอมรับ`/`chấp nhận` repeated in zh/th/vi, and the "and" lost in ja. With no policy version set it reads "version current". | all | `portal.js` consent markup | One template key per locale, e.g. `I accept the {policy} (version {version}) and will not use the service for attacks, fraud, spam or unlawful activity.` This needs code and all six locales. | code + copy | Medium |
| C5 | `ui.pending` is the placeholder for a missing date, but every locale translates it as a status ("รอดำเนินการ", 待处理, 処理待ち, Ожидание). | all | `ui.pending` | Separate key meaning "not set yet" (zh `待定`, ja `未確定`, ru `Ожидается`). | code + copy | Low |
| C6 ✓ | The connection steps say "copy Host, Port, Username and Password", but the on-screen labels are localized. The aria-labels `Host and port`, `Proxy username` and `Proxy password` and the clipboard labels `Host:`/`User:`/`Pass:` are hard-coded in English. | ja, zh-CN, ru, vi (copy); all (code) | `ui.copy.host.port.username.and.password.into.an.http.or.socks5.compa`; `portal.js` | Use the localized field names in the copy, and move the aria-labels and clipboard labels into locale keys. | code + copy | Medium |
| C7 | "Unmetered GB" contradicts the visible usage meter. Thai `ไม่จำกัด GB` reads as "unlimited", which is stronger than the source. ja `従量制限なし` and zh `不限 GB` are unnatural. | en, th, ja, zh-CN | `ui.unmetered.gb.within.the.plan.speed.cap`, `ui.unmetered.gb` | Source: "No data quota (speed cap applies)" / "No quota". | copy | Medium |
| C8 | "Provider" means both the seller and the ISP. Thai 07 ad description 2 `IP อาจเปลี่ยนตามผู้ให้บริการ` therefore says the *seller* changes the IP. Russian readers take `провайдер` as the ISP. ja `提供者` sounds machine-translated. | en, th, ja, ru, vi | `error.permission`, `error.validation`, `ui.invalid.payment.destination.contact.the.provider`, `ui.the.provider.is.setting.up…`, 07 ad descriptions | Use "support"/"we" for the seller and "ISP"/"internet provider" for the network (th `ผู้ให้บริการอินเทอร์เน็ต`, ru `интернет-провайдер`, vi `nhà mạng`, ja `運営者`). | copy | High (th ad), Medium |
| C9 | "Refresh" and "renew" use the same word. | ja (`更新`), vi (`cập nhật`) | `ui.refresh.status`, `ui.if.connection.fails…`, ja 07 ad description 2 | ja `最新の状態を表示` / `再読み込み`; vi `Làm mới trạng thái`. Keep `更新`/`gia hạn` for renew. | copy | Medium |
| C10 ✓ | Four keys are never referenced by portal code, yet are translated without context (Thai `เพิ่ม`, zh `含赠送`). | all | `ui.includes`, `ui.bonus.days.in.this.period`, `ui.total`, `ui.gb.per.plan.period` (no static or dynamic reference in `admin-api/portal/*.js` or Go) | Remove from all six files together; the parity test requires that. | code | Low |
| C11 | Words for "dedicated" carry an exclusive-IP meaning in local search: ja `専用プロキシ`, zh bare `独享`/`独享IP`, vi `proxy riêng`/`IP riêng`, ru `выделенные прокси`. | ja, zh-CN, vi, ru | `seo.plans.keywords`, `seo.home.keywords`, 07 supporting intent, `ui.http.socks5.your.own.connection.credentials` (ja `お客様専用`, zh `您专属`) | Do not bid on these terms. Always say "dedicated board/device" and keep the FAQ disclaimer next to it. Change "your own credentials" to "per-customer credentials". | copy + owner | High (ja keyword), Low–Medium |
| C12 | "Residential" keywords: en `Thai residential network proxy`, th `residential proxy ไทย`, ja `住宅回線`, zh `泰国住宅网络代理`, vi `dân dụng` (wrong word; residential is `dân cư`). In these markets the term suggests rotating residential pools and multi-account use. | en, th, ja, zh-CN, vi | `seo.home.keywords`, 07 supporting intent | Owner decision. Keep it out of ad text unless visible copy explains home-ISP boards with changeable IPs. Meta keywords have no ranking effect. | owner | Medium |
| C13 | The title, H1 and description in report 07 differ from the shipped `seo.home.*`/`seo.plans.*` strings in every locale. | all | 07 per-locale "Title/H1/Description" vs locale JSON | Treat the locale JSON as canonical (it is what ships) and update 07 to match it, or the reverse, in one change. Also give `seo.plans.h1` topic words in each locale (e.g. en "Thai proxy plans: shared and dedicated"). | doc + copy | Medium |
| C14 ✓ | The word for "board". ru `плата` also means "fee/charge" (26 strings), so `ui.kind.shared` renders `Общая плата` ("total fee") and payment messages read "a payment is expected". ja `デバイス` reads as the customer's own device allowance. vi `bo mạch` is unusual in retail. en never defines "board". | ru (High), ja, vi, en | `ui.kind.shared`, `ui.kind.dedicated`, `ui.waiting.for.board.capacity`, `ui.payment.received.waiting…`, `ui.one.board.reserved.for.one.account` | ru `устройство` everywhere, with one help line explaining it is an ESP32 board. ja `出口デバイス`. en: define "board" once or say "device" in customer copy. vi: native buyer test of `bo mạch` vs `thiết bị`. | copy | High (ru) |

## Per-language findings

High and Medium findings not already covered above. "Current" quotes the
relevant excerpt exactly. Low items follow each table.

### th — Thai

| Key | Current | Issue | Suggested | Sev |
|---|---|---|---|---|
| `ui.if.an.order.still.awaits.payment.after.returning.from.stripe.wait` ✓ | "…ไม่ต้องชำระซ้ำทันที" | "No need to" softens the source "Do not pay again", which raises the double-payment risk | "…กรุณารอการยืนยันแล้วกดอัปเดตสถานะ อย่าเพิ่งชำระซ้ำ" | High |
| 07 ad description 2 | "…IP อาจเปลี่ยนตามผู้ให้บริการ" | Reads as the seller changing the IP (C8) | "…IP อาจเปลี่ยนตามผู้ให้บริการอินเทอร์เน็ต" (check length in the Ads editor) | High |
| `ui.paid.through` | "ชำระถึง" | Can read as a due date ("pay by") | "ชำระครอบคลุมถึง" | Medium |
| `ui.a.seat.is.held.for.15.minutes…` | "จอง Seat 15 นาที ก่อนชำระเงิน…" | Reads as an instruction to the user | "ระบบจะกัน Seat ไว้ให้ 15 นาทีเพื่อรอชำระเงิน…" | Medium |
| `ui.order.is.waiting.for.capacity.no.payment.has.been.made` | "…ยังไม่ชำระเงิน" | Sounds like "you haven't paid" instead of "nothing was charged" | "คำสั่งซื้อกำลังรอบอร์ดว่าง ยังไม่มีการเรียกเก็บเงิน" | Medium |
| `ui.a.seat.on.a.board.with.measured.capacity` / `seo.home.body` | "Seat" vs "ที่นั่ง" | One concept, two words | "Seat" everywhere | Medium |
| `ui.speed.depends.on.the.board…`, `ui.availability.is.checked…` | "Rate cap…", "public IP…; …connections" | English mixed in, and a semicolon | "ความเร็วสูงสุดของแพ็กเกจ", "IP สาธารณะ", "การเชื่อมต่อ"; no semicolon | Medium |
| `ui.acceptable.use.policy` (+2 keys) | "นโยบายการใช้งานที่ยอมรับได้" / "ข้อกำหนด" | Calque, and three names for one policy | "นโยบายการใช้งาน" everywhere | Medium |
| `ui.one.time.email.sign.in.link.valid.for.15.minutes` | "ใช้ลิงก์ครั้งเดียว อายุ 15 นาที · Email sign-in link" | Imperative, with an English tail | "ลิงก์เข้าสู่ระบบทางอีเมล ใช้ได้ครั้งเดียวภายใน 15 นาที" | Medium |
| `seo.home.body` | "บริการออกผ่านเครือข่าย…" | Unnatural | "การเชื่อมต่อออกอินเทอร์เน็ตผ่านเครือข่ายของผู้ให้บริการอินเทอร์เน็ตในไทย…" | Medium |
| `seo.home.title` / 07 title | "แพ็กเกจเครือข่ายไทย" (07) | Reads like a mobile data package. The primary intent "เช่า proxy ไทย" never appears on a page. | Test "เช่าพร็อกซีไทย HTTP / SOCKS5 \| Proxy Server" against real search terms (hypothesis; "พร็อกซี่" spelling too) | Medium |

Low: `ui.one.board.reserved.for.one.account` ("สงวนบอร์ด 1 ตัวให้บัญชีเดียว"),
`ui.open.customer.portal` ("เปิดหน้าบัญชีลูกค้า"), `seo.plans.body`, `seo.plans.h1`
("เลือกแพ็กเกจพร็อกซีไทย"), `seo.home.description`, `ui.payment.recorded`,
`ui.real.payments.are.not.enabled` ("· ยังไม่รับชำระเงินจริง"). Possible extra
negatives: "proxy ฟรี", "ปลดแบน", "เลี่ยงแบน", "ปั๊มยอด", "ทอเรนต์", "แฮ็ก". Proxy is
spelled three ways (พร็อกซี / Proxy / proxy). Visual check: AUP version dates may
render in Gregorian ISO while other dates use the Buddhist era.

### en — English (source)

| Key | Current | Issue | Suggested | Sev |
|---|---|---|---|---|
| `ui.awaiting.review` / `ui.approved` ✓ | "Awaiting review" / "Approved" | Any unknown status falls back to "Awaiting review" (`portal.js` `status()`). No Go code sets `approved`; an approved slip becomes `paid`. | Fallback "Status unavailable. Refresh."; "Payment approved" or drop it | Medium |
| `ui.change.password` | "Change password" | The button generates a new password | "Generate new password" | Medium |
| `ui.the.old.password.will.no.longer.reconnect…` | "The old password will no longer reconnect…" | Passwords don't reconnect; likely to mistranslate | "After this, the old password stops working. Enter the new one in every app that uses this proxy." | Medium |
| `ui.measured.board.capacity` | "Measured board capacity" | Shown as the value after "Speed cap:" | "No plan cap (limited by device capacity)" | Medium |
| `ui.a.seat.on.a.board.with.measured.capacity` | "A seat on a board with measured capacity" | Jargon; never says the device is shared | "One of up to 3 seats on a shared device, set by its measured capacity" | Medium |
| `seo.home.description` | "…Compare shared and dedicated boards with measured capacity…" | 171 characters, so likely truncated in results | "HTTP and SOCKS5 proxy plans on Thai networks. Compare shared and dedicated devices, speed caps and quotas, then manage renewals in one account." | Medium |
| `ui.no.seat.is.available.for.this.order.payment.is.not.open`, `ui.no.board.has.available.capacity.for.this.plan`, `ui.payment.received.waiting.for.a.board…`, `ui.a.seat.is.held.for.15.minutes…` | "No board has available capacity for this plan." etc. | Jargon with no next step; Dedicated holds a whole device, not a seat | "This plan is full right now. Try again later or choose another plan."; "After you create the order, your seat or device is held for 15 minutes…" | Medium |
| `ui.manage.multiple.plans…` | "…No admin token is required." | Operator jargon in customer copy | Delete the sentence | Medium |

Low: `ui.order.created.choose.payment.in.the.order.list`,
`ui.status.from.payment.confirmations.and.receipt.reviews`, developer wording in
`ui.the.link.has.no.token…`, `ui.sign.in.at.the.configured.customer.portal`,
`ui.use.the.port.assigned…`. Possible extra negatives: `static ip`, `dedicated ip`,
`unlimited`, `bypass`, `unblock`, `anonymous`. Ad headline 3 is vague (e.g. "Shared
or Dedicated Plans"). Terms to standardize: "speed cap" (vs rate cap / Mbps cap),
one wording for the bank-transfer method, `ui.payment.receipt` vs
`ui.payment.receipt.136`, `ui.version` vs `ui.version.119`.

### zh-CN — Simplified Chinese

| Key | Current | Issue | Suggested | Sev |
|---|---|---|---|---|
| 07 negative keywords | `免费`, `破解`, `黑客`, `垃圾邮件`, `绕过封禁`, `公共代理列表`, `盗版` | Leaves out the main misuse searches: firewall circumvention, multi-account/anti-detect, ban workarounds | Hypotheses to add: `翻墙` `科学上网` `VPN` `梯子` `机场` `加速器` `防关联` `多开` `养号` `指纹浏览器` `刷单` `防封` `解封` `免费代理IP` `代理IP池`; `公共代理列表` → `免费代理列表` | High |
| 07 primary intent | `泰国代理` | 代理 also means agent or distributor | Primary `泰国代理IP`, `泰国代理服务器`, `泰国SOCKS5代理`; negatives `代理商` `总代理` `加盟` `招商` `代购` `签证` `代办` `注册公司` | Medium |
| `ui.active` | `可用` | Reads as "available" and clashes with seat availability | `生效中` | Medium |
| `seo.home.body` | `访问通过泰国互联网运营商网络出口…` | Literal | `流量经由泰国本地互联网运营商网络出口，适用于经授权的测试与验证。` | Medium |

Low: `seo.plans.keywords` (`泰国共享代理`, `泰国代理包月`), always `独享设备` rather than
bare `独享`, `ui.invalid.payment.destination…` (`收款账户信息无效`), `ui.approved` /
`ui.rejected` (`审核通过` / `审核未通过`), `ui.a.seat.on.a.board…`, `ui.payment.is.not.available.yet…`,
07 ad headline 3 (`在线查看用量与续费`), ad description 1, `……` ellipsis and full-width
`主机：端口`. Owner note: whether marketing to buyers in mainland China is lawful and
ad-policy eligible is not assessed.

### ja — Japanese

| Key | Current | Issue | Suggested | Sev |
|---|---|---|---|---|
| `ui.availability.is.checked…` ✓ | "…公開IPは他のデバイスと共有され、ISPにより変わる場合があります。" | Says the IP *is* shared; the source says *may be* | "公開IPは他のデバイスと共有される場合があり、ISPによって変わることもあります。" | High |
| `ui.receipt.added.to.the.review.queue` ✓ | "振込明細の確認を受け付けました。" | Can read as "your slip was confirmed" | "振込明細を受け付けました。管理者の確認後に支払いが確定します。" | High |
| `seo.plans.keywords` | "…専用プロキシ, …月額プロキシ" | Exclusive-IP meaning (C11); 月額 implies monthly billing | "タイ プロキシ 料金, タイ プロキシ プラン, 共有プロキシ, SOCKS5 プロキシ 料金" | High |
| `ui.quota.used` (+ `seo.*`) | "使用済み容量" | 容量 is used for both the GB quota and measured capacity | Use 通信量 for quota: "使用済み通信量" | Medium |
| `ui.payment.is.not.available.yet…` | "…この間、決済は行われません。" | この間 can read as "the other day" | "準備が整うまで料金は請求されません。" | Medium |
| `ui.paid.through` | "支払い済み期限" | Unnatural | "支払い済み期間の終了日" | Medium |
| `seo.plans.faq.answer`, `seo.home.body`, `seo.help.body` | "…予約期限後の支払いは空きを待つ場合があります。" etc. | Ungrammatical or mismatched clauses; the ban on sharing credentials is buried in a 〜せず clause | See the reviewer's rewrites, e.g. "認証情報は他人と共有しないでください。" | Medium |
| `ui.a.seat.is.held…`, `ui.if.an.order.still.awaits.payment…` | "…決済ページから戻っても入金確認にはなりません。" | Literal; the meaning of "still" is lost | "…戻っただけでは支払いは確定しません。"; "「支払い待ち」のままの場合は…" | Medium |
| `ui.request.new.link` / `ui.if.you.did.not.request…` | "新しいリンクを依頼" | 依頼 sounds literal | "リンクを再送信" / "心当たりがない場合は、このメールを無視してください。" | Medium |
| `ui.and.will.not.use…` | "…迷惑行為…" | Not "spam", and inconsistent with `スパム` elsewhere | "スパム" | Medium |

Low: `ui.payment.record.not.a.tax.invoice`, `ui.bank.transfer.with.receipt`
("銀行振込（振込明細を送信）"), number spacing ("合計{count}日"), the
HTTP / SOCKS5 separator, "Wi-Fi". Possible extra negatives: "フリー",
"フリープロキシ", "規制回避". Plain-form headings mixed with です・ます.

### ru — Russian

The `плата` items (C14) come first. Every row in this first group is High.

| Key | Current | Suggested |
|---|---|---|
| `ui.kind.shared` / `ui.kind.dedicated` ✓ | "Общая плата" / "Выделенная плата" | "Общее устройство" / "Выделенное устройство" |
| `ui.payment.received.waiting.for.a.board…` | "Оплата получена. Ожидается плата; …мощности." | "Оплата получена. Ожидаем свободное устройство; распределение зависит от фактической пропускной способности." |
| `ui.a.seat.is.held…` | "…может потребоваться ожидание платы. Возврат со страницы оплаты…" | "…возможно ожидание свободного устройства. Переход обратно со страницы оплаты не подтверждает платёж." (Возврат also means "refund") |
| `seo.plans.faq.answer` | "Сначала нужны подтверждение оплаты и выделение платы…" | "Сначала оплата должна быть подтверждена, а устройство — выделено…" |
| `ui.one.board.reserved.for.one.account` | "Одна плата закреплена за одним аккаунтом" | "Одно устройство закреплено за одним аккаунтом" |
| `ui.waiting.for.board.capacity` ✓ | "Ожидает свободной платы" | "Ожидает свободного места" |
| `seo.home.description` | "…Общие и выделенные платы с измеренной мощностью…" | "Прокси через сети Таиланда по HTTP и SOCKS5. Общие и выделенные устройства с измеренной пропускной способностью. Тарифы и продление — в личном кабинете." |

| Key | Current | Issue | Suggested | Sev |
|---|---|---|---|---|
| capacity terms (9 keys) | "мощность" | Means electrical power | "пропускная способность" for throughput, "свободные места" for availability | Medium |
| `ui.no.board.has.available.capacity.for.this.plan`, `ui.order.is.waiting.for.capacity…` | "…нет свободной платы." / "Заказ ожидает мощности…" | Ambiguous or calqued | "…сейчас нет свободных мест." / "Заказ ожидает свободного места. Оплата не проводилась." | Medium |
| `ui.access.is.assigned.to.a.board…`, `ui.availability.is.checked…`, `seo.home.body` | "…меняться у интернет-провайдера/провайдера/ISP" | Calque, and Latin "ISP" | "IP-адрес назначает интернет-провайдер, он может меняться." | Medium |
| `ui.payment.record.not.a.tax.invoice` | "…не налоговый счёт." | Calque | "Подтверждение оплаты. Не является счётом-фактурой." | Medium |
| `seo.home.title`, `seo.home.h1`, `seo.plans.h1`, `seo.help.title` | "Прокси Таиланда…" / "Выбрать тариф" | Awkward genitive; infinitive H1 | "Тайские прокси HTTP и SOCKS5 \| Proxy Server" / "Тарифы на прокси из Таиланда" | Medium |
| `seo.help.description` | "…Проверка квоты, срока и правила использования." | Case error | "…Как проверить квоту, срок действия и правила использования." | Medium |
| keywords / 07 negatives | "выделенная плата прокси"; "бесплатно", "обход бана"… | Not real phrases; inflected variants are missing | Hypotheses: "купить прокси Таиланд", "SOCKS5 прокси Таиланд"; negatives "бесплатные", "бесплатный", "обход блокировок", "антидетект", "мультиаккаунт", "накрутка", "брут", "чекер" | Medium |

Low: `ui.speed.depends.on.the.board…` (drops "with other boards"; "Wi‑Fi"),
`ui.pending`, `ui.transfer.upload.receipt`, `ui.payment.recorded`, `ui.create.order`
(renders "Создать заказ 1 000,00 ฿"; "Оформить заказ на"), 07 title and ad assets.
Code note: units are rendered by `portal.js` as GB/MB/Mbps while the copy says
ГБ/Мбит/с, so scripts mix on one line.

### vi — Vietnamese

| Key | Current | Issue | Suggested | Sev |
|---|---|---|---|---|
| `ui.invalid.payment.destination.contact.the.provider` ✓ | "Địa chỉ thanh toán không hợp lệ…" | Means "billing address", not the receiving account | "Thông tin tài khoản nhận tiền không hợp lệ. Vui lòng liên hệ nhà cung cấp." | High |
| `consent.body` ✓ | "Chọn cho phép đo lường và quảng cáo…" | Drops "whether", so it nudges toward consent | "Bạn có thể cho phép hoặc từ chối đo lường và quảng cáo…" | High |
| `ui.jpg.png.or.pdf.up.to.5.mb.uploads.enter.a.review.queue…` | "…Tải lên sẽ vào hàng chờ duyệt, chưa xác nhận…" | "chưa" (not yet) implies the upload will later confirm payment | "…Tệp sẽ vào hàng chờ duyệt; việc tải lên không có nghĩa là thanh toán đã được xác nhận." | Medium |
| `ui.a.seat.is.held…`, `ui.if.an.order.still.awaits.payment…` | "Giữ chỗ 15 phút trước khi thanh toán…" | Reads as "hold 15 minutes before paying"; unclear who updates | "Chỗ được giữ 15 phút để bạn thanh toán…"; "…hãy chờ hệ thống xác nhận rồi làm mới trạng thái." | Medium |
| capacity strings: `ui.waiting.for.board.capacity`, `ui.no.board.has.available.capacity…`, `ui.order.is.waiting.for.capacity…` | "Chờ năng lực bo mạch" | "năng lực" is a calque | "Chờ bo mạch còn chỗ"; "Đơn đang chờ chỗ trống. Bạn chưa bị thu tiền." | Medium |
| Dedicated/Shared in running text | "Dedicated dành riêng một bo mạch…" | The badges are Vietnamese ("Bo mạch riêng/dùng chung"); "bo" is truncated | "Gói bo mạch riêng (Dedicated) giữ một bo mạch cho bạn…" | Medium |
| 07 ad headlines, description, FAQ intent | "Proxy Mạng Thái Lan"; "dung lượng đo thực tế"; "Gói riêng đặt trước gì?" | Title Case is unnatural; "dung lượng" means data volume; "đặt trước" means pre-order | Sentence case; "công suất đo thực tế của bo mạch"; "Gói Dedicated (bo mạch riêng) dành cho bạn những gì?" | Medium |
| `seo.plans.keywords` / 07 supporting | "proxy bo mạch riêng", "giá SOCKS5" | Not realistic searches | Hypotheses: "thuê proxy Thái Lan", "mua proxy Thái Lan", "giá proxy SOCKS5" | Medium |

Low: `ui.connection.guide.subtitle` ("HTTP / SOCKS5 và quy định sử dụng"),
`ui.request.received…` ("kể từ khi được gửi"), `ui.paid.through` ("Đã thanh toán
đến"), `ui.speed.depends…` ("máy chủ đích"), `seo.help.title` (62 characters).
Possible extra negatives: "vượt chặn", "lách chặn", "fake ip", "proxy free", "crack".
Order is rendered three ways (đơn / đơn hàng / đơn mua). Owner note: PromptPay and
Thai bank transfer are cross-border for Vietnamese buyers; whether they can actually
pay was not assessed.

## SEO length notes

Google truncates by pixel width, so these are advisories, not limits. The longest
meta descriptions are en home (171 characters), vi home (156) and th home (147).
The longest titles are vi help (62) and th/ru help (60). CJK titles and descriptions
are short in characters but about twice as wide.

## Suggested order of work

1. Owner decisions: C2 wording, C11/C12 positioning, the C13 canonical source, the
   zh-CN market/legal question, and Vietnamese payment reachability.
2. A source round, after soak run 5 finishes: C1 with a real-handler regression, C4,
   C5, C6 and C10. Code changes need the normal `go test`, i18n test and browser suites.
3. Source copy (en) for C3, C7, C8 and C14, then each locale's rows above, each
   confirmed by a native reviewer.
4. Update report 07's matrix to match whatever ships (C13), then validate the ad
   assets in the live Ads editor before any spend.
