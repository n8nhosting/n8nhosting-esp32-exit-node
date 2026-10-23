# Evidence — sales page v2 and admin Overview (23 Oct 2026)

Real screenshots of the uncommitted working tree on `5d5bee4`, taken in
Chromium (Playwright) against `tools/dashboard_fixture.cjs`: the real admin-api
binary on an isolated SQLite file with a fake broker. Not production.

- Plans "Shared Starter", "Shared Plus" and "Dedicated Board" were created
  through the admin API for the screenshot; prices are fixture values, not
  offers.
- Orders, the slip and the subscription come from `tools/commerce_seed.py`.
  They are marked LOCAL QA, so the revenue figures are synthetic.
- The portal readiness state is "orders closed", because no provider is
  configured in the fixture. That is why every landing shot shows the
  closed banner, the step 2 badge and "View plan details".
- `landing-en-1440-no-plans-full.png` was taken before any plan existed.

| File | Shows |
|---|---|
| `landing-en-1440-no-plans-full.png` | Whole page, no active plans (empty state) |
| `landing-th-1440-top.png`, `landing-{en,zh-CN,ja,ru,vi}-1440-top.png` | First viewport per locale |
| `landing-th-1440-with-plans-full.png` | Whole Thai page with three active plans |
| `landing-ru-1440-plans.png` | Plan cards and comparison with the Russian "устройство" wording |
| `landing-th-390-top.png`, `landing-th-390-plans.png`, `landing-{ja,zh-CN}-390-top.png`, `landing-th-320-top.png` | Phone widths |
| `admin-login-1440.png` | Sign-in with the fleet map backdrop |
| `admin-overview-1440-top.png`, `admin-overview-1440-full.png` | Overview: business + network rows, traffic, top users, fleet map, tiles, pipeline |
| `admin-overview-390-top.png`, `admin-overview-390-map.png` | Overview on a phone |
| `admin-devices-1440.png`, `admin-users-1440.png`, `admin-sales-orders-1440.png` | Existing pages with the shared styling only |

The owner's "before" screenshots and the AI concept images are in
[`../../proxy-server-salespage-handoff/references/`](../../proxy-server-salespage-handoff/references/).
