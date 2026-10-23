package main

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// The home page is the sales page: hero with the Thailand map, a benefit
// strip, three steps, cards for the active plans, shared vs dedicated, use
// cases, FAQ and a closing call to action. Everything is server-rendered so
// it reads the same without JavaScript and to search engines; portal.js only
// adds the account header and refreshes the readiness banner around it.

// portalPrice formats satang as Thai baht: ฿1,290 or ฿12,345.50.
func portalPrice(satang int64) string {
	baht, rest := satang/100, satang%100
	digits := strconv.FormatInt(baht, 10)
	var b strings.Builder
	for i, c := range digits {
		if i > 0 && (len(digits)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if rest != 0 {
		fmt.Fprintf(&b, ".%02d", rest)
	}
	return "฿" + b.String()
}

// portalNumber drops a trailing .00 the way the portal's number() does.
func portalNumber(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

func portalLandingMap() string {
	raw, err := portalFS.ReadFile("portal/landing-map.svg")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

// Readiness as the portal config reports it (handlePortalConfig): sign-in
// needs the public URL, email and the challenge; an order also needs a
// payment method.
func portalLoginReady() bool {
	_, baseOK := portalBase()
	return baseOK && emailConfigured() && turnstileConfigured()
}
func portalPaymentReady() bool {
	_, baseOK := portalBase()
	return (baseOK && stripeConfigured()) || manualConfigured()
}
func portalOrdersOpen() bool { return portalLoginReady() && portalPaymentReady() }

// portalReadinessBanner is the banner portal.js's showReadiness() shows for
// the same state, so the first paint already has it and nothing shifts.
func portalReadinessBanner(d map[string]string) string {
	e := html.EscapeString
	switch {
	case !portalLoginReady():
		return `<div class="banner"><strong>` + e(d["ui.orders.are.not.open.yet"]) + `</strong><br>` + e(d["ui.readiness.closed.body"]) + `</div>`
	case !portalPaymentReady():
		return `<div class="banner">` + e(d["ui.payment.is.not.available.yet.no.payment.will.be.collected.meanwhi"]) + `</div>`
	case stripeConfigured() && !stripeLiveMode():
		return `<div class="banner"><strong>` + e(d["ui.payment.test.mode"]) + `</strong> ` + e(d["ui.real.payments.are.not.enabled"]) + `</div>`
	}
	return ""
}

// Decorative line icons (24×24, stroked by portal.css); the text beside each
// one carries the meaning, so every icon is hidden from assistive technology.
var landingIcons = map[string]string{
	"arrow":   `<path d="M5 12h14M13 6l6 6-6 6"/>`,
	"book":    `<path d="M4 5.5A2.5 2.5 0 0 1 6.5 3H20v15H6.5A2.5 2.5 0 0 0 4 20.5zM4 20.5A2.5 2.5 0 0 0 6.5 23H20v-5"/>`,
	"globe":   `<circle cx="12" cy="12" r="9"/><path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18"/>`,
	"key":     `<circle cx="8" cy="15" r="4"/><path d="M11 12l9-9M17 6l3 3M14 9l2 2"/>`,
	"gauge":   `<path d="M4 18a8 8 0 1 1 16 0"/><path d="M12 18l4-6"/><path d="M4 18h16"/>`,
	"shield":  `<path d="M12 3l8 3v6c0 4.5-3.4 8.3-8 9-4.6-.7-8-4.5-8-9V6z"/><path d="M8.5 12l2.5 2.5 4.5-5"/>`,
	"plan":    `<path d="M12 3l8 4.5v9L12 21l-8-4.5v-9z"/><path d="M4 7.5l8 4.5 8-4.5M12 12v9"/>`,
	"verify":  `<rect x="3" y="5" width="18" height="14" rx="2"/><path d="M3 9h18M7 15h4"/><path d="M15 14.5l1.5 1.5 3-3"/>`,
	"connect": `<path d="M9 15l6-6"/><path d="M11 6.5l1.5-1.5a4 4 0 0 1 5.7 5.7L16.5 12.5M7.5 11.5L5.8 13.2a4 4 0 0 0 5.7 5.7l1.5-1.5"/>`,
	"shared":  `<circle cx="9" cy="8" r="3.5"/><path d="M2.5 20a6.5 6.5 0 0 1 13 0"/><path d="M16 4.6a3.5 3.5 0 0 1 0 6.8M18 14a6.5 6.5 0 0 1 3.5 6"/>`,
	"board":   `<rect x="6" y="6" width="12" height="12" rx="2"/><path d="M10 10h4v4h-4zM9 2v4M15 2v4M9 18v4M15 18v4M2 9h4M2 15h4M18 9h4M18 15h4"/>`,
	"view":    `<path d="M2 12s3.6-7 10-7 10 7 10 7-3.6 7-10 7S2 12 2 12z"/><circle cx="12" cy="12" r="3"/>`,
	"tag":     `<path d="M3 12V4h8l10 10-8 8z"/><circle cx="7.5" cy="8.5" r="1.5"/>`,
	"login":   `<rect x="4" y="10" width="16" height="11" rx="2"/><path d="M8 10V7a4 4 0 0 1 8 0v3M12 14.5v2.5"/>`,
	"map":     `<path d="M9 4L3 6.5v13L9 17l6 2.5 6-2.5V4l-6 2.5z"/><path d="M9 4v13M15 6.5v13"/>`,
}

func landingIcon(name string) string {
	return `<svg class="ic" viewBox="0 0 24 24" aria-hidden="true" focusable="false">` + landingIcons[name] + `</svg>`
}

func portalPlanCard(locale string, p Plan, d map[string]string, open bool) string {
	e := html.EscapeString
	kind := d["ui.kind.shared"]
	if p.Kind == "dedicated" {
		kind = d["ui.kind.dedicated"]
	}
	rate := d["ui.measured.board.capacity"]
	if p.RateMbps > 0 {
		rate = portalNumber(p.RateMbps) + " Mbps"
	}
	quota := d["ui.unmetered.gb.within.the.plan.speed.cap"]
	if p.QuotaGB > 0 {
		quota = strings.ReplaceAll(d["ui.quota.period"], "{count}", portalNumber(p.QuotaGB))
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<article class="landing-plan%s" data-plan="%s"><span class="pill">%s</span><h3>%s</h3>`,
		map[bool]string{true: " dedicated", false: ""}[p.Kind == "dedicated"], e(p.ID), e(kind), e(p.Name))
	if p.Description != "" {
		fmt.Fprintf(&b, `<p class="meta">%s</p>`, e(p.Description))
	}
	fmt.Fprintf(&b, `<p class="plan-price">%s</p><p class="plan-days">%s</p>`,
		e(portalPrice(p.PriceSatang)), e(strings.ReplaceAll(d["ui.count.days"], "{count}", strconv.Itoa(p.Days))))
	if p.BonusDays > 0 {
		fmt.Fprintf(&b, `<p class="bonus">%s</p>`, e(strings.ReplaceAll(d["ui.count.bonus"], "{count}", strconv.Itoa(p.BonusDays))))
	}
	fmt.Fprintf(&b, `<ul class="features"><li>%s: %s</li><li>%s</li></ul>`, e(d["ui.rate.cap"]), e(rate), e(quota))
	if p.Kind == "dedicated" {
		fmt.Fprintf(&b, `<p class="plan-note">%s</p>`, e(d["landing.dedicated.note"]))
	}
	action := d["landing.plan.view"]
	if open {
		action = d["ui.select.this.plan"]
	}
	fmt.Fprintf(&b, `<a class="button" href="/portal/%s/plans">%s</a></article>`, locale, e(action))
	return b.String()
}

func portalLandingContent(locale string, d map[string]string, plans []Plan) string {
	e := html.EscapeString
	open := portalOrdersOpen()
	plansURL, helpURL := "/portal/"+locale+"/plans", "/portal/"+locale+"/help"
	var b strings.Builder
	fmt.Fprintf(&b, `<section id="publicContent" class="public-content landing" aria-labelledby="publicHeading">`)

	// hero; the figure is a marked slot for a real photo later (data-photo-slot)
	fmt.Fprintf(&b, `<div class="hero"><div class="hero-copy"><p class="eyebrow">%s</p><h1 id="publicHeading">%s</h1><p class="lead">%s</p>`,
		e(d["landing.eyebrow"]), e(d["seo.home.h1"]), e(d["seo.home.intro"]))
	fmt.Fprintf(&b, `<div class="hero-actions"><a class="button" href="%s">%s%s</a><a class="button secondary" href="%s">%s%s</a></div></div>`,
		plansURL, e(d["ui.browse.plans"]), landingIcon("arrow"), helpURL, landingIcon("book"), e(d["ui.connection.guide"]))
	fmt.Fprintf(&b, `<figure class="hero-art" data-photo-slot="hero">%s<figcaption class="hero-card"><span class="hero-card-tag">%s%s</span><span>%s</span><code>HTTP · SOCKS5</code></figcaption></figure></div>`,
		portalLandingMap(), landingIcon("map"), e(d["landing.card.illustration"]), e(d["landing.card.exit"]))

	b.WriteString(`<ul class="landing-benefits">`)
	for i, icon := range []string{"globe", "key", "gauge", "shield"} {
		fmt.Fprintf(&b, `<li>%s<span>%s</span></li>`, landingIcon(icon), e(d[fmt.Sprintf("landing.proof.%d", i+1)]))
	}
	b.WriteString(`</ul>`)

	fmt.Fprintf(&b, `<section id="landingSteps" class="landing-steps" aria-labelledby="stepsHeading"><div class="section-head"><h2 id="stepsHeading">%s</h2></div><ol>`, e(d["landing.steps.title"]))
	for i, icon := range []string{"plan", "verify", "connect"} {
		n := i + 1
		gated := n == 2 && !open
		fmt.Fprintf(&b, `<li%s><div class="step-top"><span class="step-no" aria-hidden="true">%d</span>%s</div><h3>%s</h3><p>%s</p>`,
			map[bool]string{true: ` class="step-gated"`, false: ""}[gated], n, landingIcon(icon),
			e(d[fmt.Sprintf("landing.step.%d.title", n)]), e(d[fmt.Sprintf("landing.step.%d.body", n)]))
		if gated {
			fmt.Fprintf(&b, `<p class="step-badge">%s</p>`, e(d["landing.step.2.closed"]))
		}
		b.WriteString(`</li>`)
	}
	b.WriteString(`</ol></section>`)

	fmt.Fprintf(&b, `<section id="landingPricing" class="landing-pricing" aria-labelledby="pricingHeading"><div class="section-head"><h2 id="pricingHeading">%s</h2><p>%s</p><p class="section-link"><a href="%s">%s%s</a></p></div>`,
		e(d["landing.pricing.title"]), e(d["landing.pricing.intro"]), plansURL, e(d["landing.pricing.all"]), landingIcon("arrow"))
	if len(plans) == 0 {
		fmt.Fprintf(&b, `<div class="landing-empty"><span class="empty-art">%s</span><div><h3>%s</h3><p>%s</p><p class="empty-links"><a href="%s">%s</a><a href="%s">%s</a></p></div></div>`,
			landingIcon("board"), e(d["landing.pricing.empty.title"]), e(d["landing.pricing.empty.body"]),
			plansURL, e(d["landing.pricing.all"]), helpURL, e(d["ui.connection.guide"]))
	} else {
		b.WriteString(`<div class="landing-plans">`)
		for _, p := range plans {
			b.WriteString(portalPlanCard(locale, p, d, open))
		}
		b.WriteString(`</div>`)
	}
	b.WriteString(`</section>`)

	fmt.Fprintf(&b, `<section id="landingCompare" class="landing-compare" aria-labelledby="compareHeading"><div class="section-head"><h2 id="compareHeading">%s</h2><p>%s</p></div><div class="compare">`,
		e(d["landing.compare.title"]), e(d["landing.compare.intro"]))
	fmt.Fprintf(&b, `<article class="compare-shared">%s<h3>%s</h3><p>%s</p></article>`, landingIcon("shared"), e(d["landing.shared.title"]), e(d["landing.shared.body"]))
	fmt.Fprintf(&b, `<article class="compare-dedicated">%s<h3>%s</h3><p>%s</p><p class="compare-note">%s</p></article>`,
		landingIcon("board"), e(d["landing.dedicated.title"]), e(d["landing.dedicated.body"]), e(d["landing.dedicated.note"]))
	b.WriteString(`</div></section>`)

	fmt.Fprintf(&b, `<section id="landingUsecases" class="landing-usecases" aria-labelledby="usecasesHeading"><div class="section-head"><h2 id="usecasesHeading">%s</h2><p class="public-body">%s</p></div><ul>`,
		e(d["landing.usecases.title"]), e(d["seo.home.body"]))
	for i, icon := range []string{"view", "tag", "login"} {
		fmt.Fprintf(&b, `<li>%s<span>%s</span></li>`, landingIcon(icon), e(d[fmt.Sprintf("landing.usecase.%d", i+1)]))
	}
	b.WriteString(`</ul></section>`)

	fmt.Fprintf(&b, `<section id="landingFaq" class="public-faq landing-faq" aria-labelledby="faqHeading"><div class="section-head"><h2 id="faqHeading">%s</h2><p>%s</p></div><div class="faq-list">`,
		e(d["landing.faq.title"]), e(d["landing.faq.intro"]))
	fmt.Fprintf(&b, `<details open><summary>%s</summary><p>%s</p></details>`, e(d["seo.home.faq.question"]), e(d["seo.home.faq.answer"]))
	for i := 2; i <= 3; i++ {
		fmt.Fprintf(&b, `<details><summary>%s</summary><p>%s</p></details>`,
			e(d[fmt.Sprintf("landing.faq.%d.question", i)]), e(d[fmt.Sprintf("landing.faq.%d.answer", i)]))
	}
	b.WriteString(`</div></section>`)

	cta := d["landing.cta.body.closed"]
	if open {
		cta = d["landing.cta.body"]
	}
	fmt.Fprintf(&b, `<section id="landingCta" class="landing-cta" aria-labelledby="ctaHeading"><div><h2 id="ctaHeading">%s</h2><p>%s</p></div><div class="cta-actions"><a class="button" href="%s">%s%s</a><a class="button secondary" href="%s">%s</a></div></section>`,
		e(d["landing.cta.title"]), e(cta), plansURL, e(d["ui.browse.plans"]), landingIcon("arrow"), helpURL, e(d["ui.connection.guide"]))

	b.WriteString(portalPageLinks(locale, "home", d))
	b.WriteString(`</section>`)
	return b.String()
}
