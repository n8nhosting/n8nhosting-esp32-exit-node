# Device settings and flasher refinement

User-requested follow-up to the deployed Phase 2 dashboard (2026-10-22).
The existing admin visual design and five requested changes are the authority.

## Behavior

- Device Settings exposes measured speed in Mbps. `PUT /api/devices/<mac>`
  accepts optional `capacity_mbps` from 0 through 100000; omission preserves
  the value, zero resets unmeasured. Manual overrides clear speed-test time and
  stream metadata, expose `capacity_source: manual`, and emit `device.updated`
  with a manual source. A real measurement continues to emit `device.measured`.
  Automatic measurement and manual override cannot race on the same MAC.
- Empty device names display as `Board-XXXX`, taking the last four MAC hex
  digits in uppercase. Existing names remain intact; no bulk database rename.
- The revised device details sheet shows all ten information sections together,
  without detail tabs. At desktop width, three aligned columns contain Health,
  Capacity and Bound users; Traffic history, Activity and Notes; Routing,
  Location, WiFi and Danger zone. The frame expands up to 1440px and becomes
  one column on phones. No visible scrollbar; touch, wheel and keyboard scrolling
  retain access to long content, short windows and zoom. Activity pagination and
  the chart's table view retain access to every fetched entry.
- Devices and the detail header explicitly label the actual Slot and Port.
  Registered boards without a slot receive the lowest available slot and its
  derived port (3130 + slot), including existing unassigned boards, as explicitly
  approved by the user. Reservations account for durable offline boards and
  broker routing. Settings can change the slot; explicitly saving slot 0 opts
  that board out of automatic assignment. Customer accounts are not assigned
  automatically. Failed broker pushes retain reservations for reconciliation.
- Device editing uses General / Location / WiFi / Notes tabs, retaining all
  original fields. Device rows keep all seven desktop columns and gain a
  primary Assign action; other commands remain in the more menu. Narrow screens
  retain the card layout and bottom navigation.
- Board-first Assign fixes the clicked MAC, lets the operator configure a free
  proxy slot, and optionally assigns a user. Slot setup can precede measurement.
  The API remains authoritative for capacity and exit-IP admission. A slot saved
  before a rejected user assignment is explicitly reported as partial success.
- Flasher uses the admin palette, typography, brand mark, panels and controls.
  Existing setup instructions, firmware archives, manifest selection, query
  parameters and ESP Web Tools installation behavior are preserved.

## Verification and delivery

Backend tests cover numeric validation, persistence, reset/omission behavior,
measurement conflicts, admission, safe events and fallback naming. Browser
checks exercise real local Go handlers with isolated SQLite at 1440x900 and
375x812; flasher checks retain real custom-element loading. Keep existing
dashboard marker tests, keyboard/focus behavior, mobile overflow checks and
Impeccable detector validation. Local checks and production deployment are
recorded separately in the acceptance handoff.
