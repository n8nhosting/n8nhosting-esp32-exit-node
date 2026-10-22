package main

import (
	"fmt"
	"html"
	"strconv"
	"strings"
)

// The home page is the sales page: hero with the Thailand exit-node map,
// three steps, cards for the active plans, shared vs dedicated, use cases,
// FAQ and a closing call to action. Everything is server-rendered so it
// reads the same without JavaScript and to search engines; portal.js only
// adds the account header and the readiness banner around it.

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

func portalPlanCard(locale string, p Plan, d map[string]string) string {
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
	fmt.Fprintf(&b, `<p class="plan-price">%s</p><p class="meta">%s</p><ul class="features"><li>%s: %s</li><li>%s</li></ul>`,
		e(portalPrice(p.PriceSatang)), e(strings.ReplaceAll(d["ui.count.days"], "{count}", strconv.Itoa(p.Days))),
		e(d["ui.rate.cap"]), e(rate), e(quota))
	fmt.Fprintf(&b, `<a class="button" href="/portal/%s/plans">%s</a></article>`, locale, e(d["ui.select.this.plan"]))
	return b.String()
}

func portalLandingContent(locale string, d map[string]string, plans []Plan) string {
	e := html.EscapeString
	plansURL, helpURL := "/portal/"+locale+"/plans", "/portal/"+locale+"/help"
	var b strings.Builder
	fmt.Fprintf(&b, `<section id="publicContent" class="public-content landing" aria-labelledby="publicHeading">`)

	// hero; the figure is a marked slot for a real photo later (data-photo-slot)
	fmt.Fprintf(&b, `<div class="hero"><div class="hero-copy"><p class="eyebrow">%s</p><h1 id="publicHeading">%s</h1><p class="lead">%s</p>`,
		e(d["landing.eyebrow"]), e(d["seo.home.h1"]), e(d["seo.home.intro"]))
	fmt.Fprintf(&b, `<div class="hero-actions"><a class="button" href="%s">%s</a><a class="button secondary" href="%s">%s</a></div><ul class="proof">`,
		plansURL, e(d["ui.browse.plans"]), helpURL, e(d["ui.connection.guide"]))
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&b, `<li>%s</li>`, e(d[fmt.Sprintf("landing.proof.%d", i)]))
	}
	fmt.Fprintf(&b, `</ul></div><figure class="hero-art" data-photo-slot="hero">%s<div class="hero-card" aria-hidden="true"><span class="live"></span><strong>%s</strong><span>%s</span><code>HTTP · SOCKS5</code></div></figure></div>`,
		portalLandingMap(), e(d["landing.card.online"]), e(d["landing.card.exit"]))

	fmt.Fprintf(&b, `<section id="landingSteps" class="landing-steps" aria-labelledby="stepsHeading"><h2 id="stepsHeading">%s</h2><ol>`, e(d["landing.steps.title"]))
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&b, `<li><span class="step-no" aria-hidden="true">%d</span><h3>%s</h3><p>%s</p></li>`,
			i, e(d[fmt.Sprintf("landing.step.%d.title", i)]), e(d[fmt.Sprintf("landing.step.%d.body", i)]))
	}
	b.WriteString(`</ol></section>`)

	fmt.Fprintf(&b, `<section id="landingPricing" class="landing-pricing" aria-labelledby="pricingHeading"><div class="section-head"><h2 id="pricingHeading">%s</h2><p>%s</p></div>`,
		e(d["landing.pricing.title"]), e(d["landing.pricing.intro"]))
	if len(plans) == 0 {
		fmt.Fprintf(&b, `<p class="landing-empty">%s</p>`, e(d["landing.pricing.empty"]))
	} else {
		b.WriteString(`<div class="landing-plans">`)
		for _, p := range plans {
			b.WriteString(portalPlanCard(locale, p, d))
		}
		b.WriteString(`</div>`)
	}
	fmt.Fprintf(&b, `<p class="section-link"><a href="%s">%s →</a></p></section>`, plansURL, e(d["landing.pricing.all"]))

	fmt.Fprintf(&b, `<section id="landingCompare" class="landing-compare" aria-labelledby="compareHeading"><h2 id="compareHeading">%s</h2><div class="compare">`, e(d["landing.compare.title"]))
	for _, k := range []string{"shared", "dedicated"} {
		fmt.Fprintf(&b, `<article class="compare-%s"><h3>%s</h3><p>%s</p></article>`, k, e(d["landing."+k+".title"]), e(d["landing."+k+".body"]))
	}
	b.WriteString(`</div></section>`)

	fmt.Fprintf(&b, `<section class="landing-usecases" aria-labelledby="usecasesHeading"><h2 id="usecasesHeading">%s</h2><ul>`, e(d["landing.usecases.title"]))
	for i := 1; i <= 3; i++ {
		fmt.Fprintf(&b, `<li>%s</li>`, e(d[fmt.Sprintf("landing.usecase.%d", i)]))
	}
	fmt.Fprintf(&b, `</ul><p class="public-body">%s</p></section>`, e(d["seo.home.body"]))

	fmt.Fprintf(&b, `<section class="public-faq landing-faq" aria-labelledby="faqHeading"><h2 id="faqHeading">%s</h2>`, e(d["landing.faq.title"]))
	fmt.Fprintf(&b, `<details open><summary>%s</summary><p>%s</p></details>`, e(d["seo.home.faq.question"]), e(d["seo.home.faq.answer"]))
	for i := 2; i <= 3; i++ {
		fmt.Fprintf(&b, `<details><summary>%s</summary><p>%s</p></details>`,
			e(d[fmt.Sprintf("landing.faq.%d.question", i)]), e(d[fmt.Sprintf("landing.faq.%d.answer", i)]))
	}
	b.WriteString(`</section>`)

	fmt.Fprintf(&b, `<section id="landingCta" class="landing-cta" aria-labelledby="ctaHeading"><h2 id="ctaHeading">%s</h2><p>%s</p><a class="button" href="%s">%s</a></section>`,
		e(d["landing.cta.title"]), e(d["landing.cta.body"]), plansURL, e(d["ui.browse.plans"]))

	b.WriteString(portalPageLinks(locale, "home", d))
	b.WriteString(`</section>`)
	return b.String()
}
