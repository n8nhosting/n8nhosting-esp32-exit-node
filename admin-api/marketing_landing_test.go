package main

import (
	"html"
	"strings"
	"testing"
)

// The home page is a server-rendered sales page: hero, three steps, live
// cards for the active plans, shared vs dedicated, FAQ and a closing call
// to action, in every locale and without JavaScript.
func TestPortalLandingRendersActivePlans(t *testing.T) {
	s, mux := portalTestServer(t)
	commerceInsertPlan(t, s, Plan{ID: "shared-30", Name: "Shared 30", Days: 30, QuotaGB: 50, RateMbps: 10, Kind: "shared", PriceSatang: 29900, Active: true})
	commerceInsertPlan(t, s, Plan{ID: "dedicated-30", Name: `Dedicated <script>alert(1)</script>`, Days: 30, RateMbps: 20, Kind: "dedicated", PriceSatang: 129000, Active: true})
	commerceInsertPlan(t, s, Plan{ID: "draft", Name: "Hidden draft", Days: 7, Kind: "shared", PriceSatang: 100, Active: false})

	for _, locale := range portalLocales {
		d := portalDictionary(locale)
		body := marketingRequest(mux, "GET", "/portal/"+locale+"/", "", false).Body.String()
		for _, want := range []string{`class="public-content landing"`, `class="hero-art"`, `<svg`, `id="landingSteps"`, `id="landingPricing"`, `id="landingCompare"`, `id="landingCta"`, `href="/portal/` + locale + `/plans"`} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s home lacks %s", locale, want)
			}
		}
		for _, key := range []string{"landing.eyebrow", "landing.proof.1", "landing.step.1.title", "landing.step.3.body", "landing.pricing.title", "landing.shared.body", "landing.dedicated.body", "landing.faq.2.answer", "landing.cta.title"} {
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
	}

	// the plans page keeps the app's own plan list; help stays a guide
	for _, page := range []string{"plans", "help"} {
		body := marketingRequest(mux, "GET", "/portal/en/"+page, "", false).Body.String()
		if strings.Contains(body, `id="landingPricing"`) || !strings.Contains(body, `id="publicHeading"`) {
			t.Fatalf("%s must not carry the landing sections", page)
		}
	}
}

func TestPortalLandingWithoutPlans(t *testing.T) {
	_, mux := portalTestServer(t)
	d := portalDictionary("th")
	body := marketingRequest(mux, "GET", "/portal/th/", "", false).Body.String()
	if strings.Contains(body, `data-plan="`) || !strings.Contains(body, html.EscapeString(d["landing.pricing.empty"])) {
		t.Fatal("no active plan must say so instead of showing cards")
	}
}

func TestPortalPriceFormat(t *testing.T) {
	for satang, want := range map[int64]string{0: "฿0", 7900: "฿79", 29900: "฿299", 129000: "฿1,290", 1234550: "฿12,345.50", 100000000: "฿1,000,000"} {
		if got := portalPrice(satang); got != want {
			t.Fatalf("portalPrice(%d) = %q, want %q", satang, got, want)
		}
	}
}
