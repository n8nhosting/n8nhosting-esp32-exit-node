package main

import (
	"html"
	"strings"
	"testing"
)

// The home page is a server-rendered sales page: hero, benefit strip, three
// steps, live cards for the active plans, shared vs dedicated, use cases, FAQ
// and a closing call to action, in every locale and without JavaScript.
func TestPortalLandingRendersActivePlans(t *testing.T) {
	s, mux := portalTestServer(t)
	commerceInsertPlan(t, s, Plan{ID: "shared-30", Name: "Shared 30", Days: 30, BonusDays: 3, QuotaGB: 50, RateMbps: 10, Kind: "shared", PriceSatang: 29900, Active: true})
	commerceInsertPlan(t, s, Plan{ID: "dedicated-30", Name: `Dedicated <script>alert(1)</script>`, Days: 30, RateMbps: 20, Kind: "dedicated", PriceSatang: 129000, Active: true})
	commerceInsertPlan(t, s, Plan{ID: "draft", Name: "Hidden draft", Days: 7, Kind: "shared", PriceSatang: 100, Active: false})

	for _, locale := range portalLocales {
		d := portalDictionary(locale)
		body := marketingRequest(mux, "GET", "/portal/"+locale+"/", "", false).Body.String()
		for _, want := range []string{`class="public-content landing"`, `class="hero-art"`, `data-photo-slot="hero"`, `<svg`, `class="landing-benefits"`, `id="landingSteps"`, `id="landingPricing"`, `id="landingCompare"`, `id="landingUsecases"`, `id="landingFaq"`, `id="landingCta"`, `href="/portal/` + locale + `/plans"`, `href="/portal/` + locale + `/help"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s home lacks %s", locale, want)
			}
		}
		for _, key := range []string{"landing.eyebrow", "landing.proof.1", "landing.proof.4", "landing.card.illustration", "landing.step.1.title", "landing.step.3.body", "landing.pricing.title", "landing.compare.intro", "landing.shared.body", "landing.dedicated.body", "landing.dedicated.note", "landing.usecase.3", "landing.faq.intro", "landing.faq.2.answer", "landing.cta.title"} {
			if d[key] == "" || !strings.Contains(body, html.EscapeString(d[key])) {
				t.Fatalf("%s home lacks %s", locale, key)
			}
		}
		if n := strings.Count(body, `data-plan="`); n != 2 {
			t.Fatalf("%s home shows %d plan cards, want the 2 active ones", locale, n)
		}
		if strings.Contains(body, "Hidden draft") || strings.Contains(body, "<script>alert") {
			t.Fatalf("%s home leaks an inactive plan or unescaped name", locale)
		}
		if !strings.Contains(body, "฿299") || !strings.Contains(body, "฿1,290") {
			t.Fatalf("%s home prices", locale)
		}
		if !strings.Contains(body, html.EscapeString(strings.ReplaceAll(d["ui.count.days"], "{count}", "30"))) {
			t.Fatalf("%s home plan duration", locale)
		}
		if !strings.Contains(body, html.EscapeString(strings.ReplaceAll(d["ui.count.bonus"], "{count}", "3"))) {
			t.Fatalf("%s home plan bonus days", locale)
		}
		// the dedicated plan card carries the public-IP caveat next to the purchase decision
		dedicated := body[strings.Index(body, `data-plan="dedicated-30"`):]
		dedicated = dedicated[:strings.Index(dedicated, "</article>")]
		if !strings.Contains(dedicated, html.EscapeString(d["landing.dedicated.note"])) {
			t.Fatalf("%s dedicated plan card lacks the public IP caveat", locale)
		}
		if n := strings.Count(body, "<h1"); n != 1 {
			t.Fatalf("%s home has %d h1, want 1", locale, n)
		}
		if n := strings.Count(body, "<details"); n != 3 || !strings.Contains(body, "<details open>") {
			t.Fatalf("%s home FAQ has %d entries or none open", locale, n)
		}
		cta := body[strings.Index(body, `id="landingCta"`):]
		cta = cta[:strings.Index(cta, "</section>")]
		if !strings.Contains(cta, `href="/portal/`+locale+`/plans"`) || !strings.Contains(cta, `href="/portal/`+locale+`/help"`) {
			t.Fatalf("%s closing call to action needs plans and guide links", locale)
		}
	}

	// the plans page keeps the app's own plan list; help stays a guide
	for _, page := range []string{"plans", "help"} {
		body := marketingRequest(mux, "GET", "/portal/en/"+page, "", false).Body.String()
		if strings.Contains(body, `id="landingPricing"`) || !strings.Contains(body, `id="publicHeading"`) {
			t.Fatalf("%s must not carry the landing sections", page)
		}
	}
}

// The map is art, not telemetry: no "connected" claim, no live indicator, and
// every icon is hidden from assistive technology. The portal CSP forbids inline styles.
func TestPortalLandingArtMakesNoLiveClaim(t *testing.T) {
	_, mux := portalTestServer(t)
	for _, locale := range portalLocales {
		body := marketingRequest(mux, "GET", "/portal/"+locale+"/", "", false).Body.String()
		start := strings.Index(body, `id="publicContent"`)
		landing := body[start : start+strings.Index(body[start:], `<nav class="page-links"`)]
		if strings.Contains(landing, `class="live"`) || strings.Contains(landing, "landing.card.online") {
			t.Fatalf("%s hero art still shows a live connection indicator", locale)
		}
		if strings.Contains(landing, ` style="`) {
			t.Fatalf("%s landing uses an inline style attribute", locale)
		}
		for rest := landing; strings.Contains(rest, "<svg"); {
			rest = rest[strings.Index(rest, "<svg"):]
			tag := rest[:strings.Index(rest, ">")]
			if !strings.Contains(tag, `aria-hidden="true"`) {
				t.Fatalf("%s landing has an svg exposed to assistive technology: %s", locale, tag)
			}
			rest = rest[len(tag):]
		}
	}
}

// Readiness is server state: while orders are closed the page says so in its
// HTML (banner, step 2, closing copy) instead of waiting for JavaScript.
func TestPortalLandingFollowsOrderReadiness(t *testing.T) {
	s, mux := portalTestServer(t)
	commerceInsertPlan(t, s, Plan{ID: "shared-30", Name: "Shared 30", Days: 30, RateMbps: 10, Kind: "shared", PriceSatang: 29900, Active: true})
	for _, locale := range portalLocales {
		d := portalDictionary(locale)
		body := marketingRequest(mux, "GET", "/portal/"+locale+"/", "", false).Body.String()
		for _, key := range []string{"ui.orders.are.not.open.yet", "ui.readiness.closed.body", "landing.step.2.closed", "landing.cta.body.closed", "landing.plan.view"} {
			if d[key] == "" || !strings.Contains(body, html.EscapeString(d[key])) {
				t.Fatalf("%s closed home lacks %s", locale, key)
			}
		}
		if strings.Contains(body, html.EscapeString(d["landing.cta.body"])) || strings.Contains(body, html.EscapeString(d["ui.select.this.plan"])) {
			t.Fatalf("%s closed home still invites an order", locale)
		}
	}

	configurePortalTest(t)
	t.Setenv("PAYMENT_BANK_NAME", "Fixture Bank")
	t.Setenv("PAYMENT_ACCOUNT_NUMBER", "000-0-00000-0")
	t.Setenv("PAYMENT_ACCOUNT_NAME", "Fixture Seller")
	d := portalDictionary("en")
	body := marketingRequest(mux, "GET", "/portal/en/", "", false).Body.String()
	for _, key := range []string{"ui.orders.are.not.open.yet", "landing.step.2.closed", "landing.cta.body.closed"} {
		if strings.Contains(body, html.EscapeString(d[key])) {
			t.Fatalf("open home still shows %s", key)
		}
	}
	if !strings.Contains(body, html.EscapeString(d["landing.cta.body"])) || !strings.Contains(body, html.EscapeString(d["ui.select.this.plan"])) {
		t.Fatal("open home lacks the order invitation")
	}
}

func TestPortalLandingWithoutPlans(t *testing.T) {
	_, mux := portalTestServer(t)
	for _, locale := range portalLocales {
		d := portalDictionary(locale)
		body := marketingRequest(mux, "GET", "/portal/"+locale+"/", "", false).Body.String()
		if strings.Contains(body, `data-plan="`) || !strings.Contains(body, `class="landing-empty"`) {
			t.Fatalf("%s: no active plan must say so instead of showing cards", locale)
		}
		empty := body[strings.Index(body, `class="landing-empty"`):]
		empty = empty[:strings.Index(empty, "</div></section>")]
		for _, key := range []string{"landing.pricing.empty.title", "landing.pricing.empty.body"} {
			if d[key] == "" || !strings.Contains(empty, html.EscapeString(d[key])) {
				t.Fatalf("%s empty pricing lacks %s", locale, key)
			}
		}
		if !strings.Contains(empty, `href="/portal/`+locale+`/plans"`) || !strings.Contains(empty, `href="/portal/`+locale+`/help"`) {
			t.Fatalf("%s empty pricing needs links to plan details and the guide", locale)
		}
		if strings.Contains(empty, "฿") {
			t.Fatalf("%s empty pricing shows a price", locale)
		}
	}
}

func TestPortalPriceFormat(t *testing.T) {
	for satang, want := range map[int64]string{0: "฿0", 7900: "฿79", 29900: "฿299", 129000: "฿1,290", 1234550: "฿12,345.50", 100000000: "฿1,000,000"} {
		if got := portalPrice(satang); got != want {
			t.Fatalf("portalPrice(%d) = %q, want %q", satang, got, want)
		}
	}
}
