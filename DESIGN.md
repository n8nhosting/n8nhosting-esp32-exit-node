---
name: Proxy Server
description: The established dark operations console and customer portal
colors:
  primary: "#455DDC"
  accent: "#8595E8"
  background: "#0E1116"
  panel: "#222631"
  input: "#2F374C"
  foreground: "#F2F5FA"
  muted: "#A0A9C0"
  line: "#343C50"
  success: "#43D1A7"
  warning: "#FF862E"
  danger: "#F96589"
typography:
  body:
    fontFamily: '"Segoe UI", "Leelawadee UI", "Noto Sans Thai", system-ui, sans-serif'
    fontSize: "14px"
    lineHeight: 1.5
  data:
    fontFamily: "ui-monospace, Menlo, Consolas, monospace"
    fontSize: "13px"
rounded:
  panel: "12px"
  control: "9px"
spacing:
  small: "8px"
  medium: "16px"
  large: "24px"
components:
  button-primary:
    backgroundColor: "{colors.primary}"
    textColor: "{colors.foreground}"
    rounded: "{rounded.control}"
    height: "44px"
  input:
    backgroundColor: "{colors.input}"
    textColor: "{colors.foreground}"
    rounded: "{rounded.control}"
---

## Overview

Extend the operator-approved visual language already implemented in
admin-api/dashboard/index.html. Tonal panels and measured data carry the
interface; primary actions are easy to locate without decorative effects.

## Colors

Primary marks actions and selection. Success, warning and danger describe
real account, payment or telemetry state. Muted text must remain legible.

## Typography

Use the incumbent Thai-capable system stack. Reserve monospace for actual
connection data, identifiers and measurements. Keep Thai copy at 1.5 line height.

## Layout

Admin uses its existing sidebar and responsive bottom navigation. New sales
sections join one Sales destination. The customer portal uses a compact header,
own-account navigation and a task workspace. Narrow layouts stack fields and
convert data rows to readable records; never hide essential actions off-screen.

## Elevation & Depth

Use tonal surfaces and one fine boundary per container. Avoid glowing shadows.

## Shapes

Panels retain gently curved corners. Small pills carry status only.

## Components

Reuse primary/secondary buttons, labeled inputs, usage bars, empty states and
accessible tables. Editors pause refresh. Every mutation has pending, success
and actionable failure states; payments show their actual lifecycle.

## Do's and Don'ts

- Keep existing device, assignment, OTA and connection-sheet workflows intact.
- Show missing configuration and unavailable capacity explicitly.
- Keep customer credentials masked until deliberately revealed.
- Do not invent prices, SLA, inventory, exclusive IP claims or payment success.
