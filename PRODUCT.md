# Product

<!-- impeccable:product-schema 1 -->

## Platform

web

## Users

The Thai operator manages ESP32 exit boards, proxy users and paid packages.
Customers use a separate portal to buy access, retrieve their own connection
details, inspect usage and renew. These audiences are established in the
approved seller-platform design and operator instructions.

## Product Purpose

Sell and operate Thai residential-network proxy access with measured per-board
capacity, durable routing and clear customer account state.

## Operating Context

Go, SQLite, gost and broker run on Coolify. The admin dashboard is a single
embedded HTML file. Mobile operation and Thai board names/notes are required.

## Capabilities and Constraints

Seats are independent per board/MAC even behind one public IP. Dedicated means
one customer on one board, not exclusive ownership of a shared public IP.
Capacity is measured; a configured speed limit is not guaranteed throughput.
Preserve existing users and firmware archives. Provider keys, bank details and
sender/domain verification are deployment configuration, never invented content.

## Brand Commitments

The operator selected the supplied dark dashboard references and asked the
flasher and new screens to match the established admin mood and tone.
Admin labels are English; customer-facing flows are Thai-first with selectable
English, Simplified Chinese, Japanese, Russian and Vietnamese. Language-specific
public pages support search and sharing. Private account/payment documents never
load optional analytics SDKs; public measurement requires visitor consent.

## Evidence on Hand

Actual firmware/production tests and screenshots are recorded in
docs/dashboard-review/04-phase2-acceptance.md. No 24-hour acceptance, public SLA,
customer testimonials or guaranteed static exit IP has been established.

## Product Principles

- Capacity and payment state must be truthful and recoverable.
- One customer only sees their own account information.
- Unknown and stale telemetry remain distinguishable from healthy readings.
- Operator edits and browser refresh must not destroy work in progress.

## Accessibility & Inclusion

Support Thai system fonts, keyboard operation, reduced motion, clear focus,
44-pixel touch targets and narrow mobile screens without horizontal page scroll.
