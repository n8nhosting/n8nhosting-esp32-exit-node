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
- The 640px device drawer retains all ten information sections inside its frame,
  using keyboard-accessible tabs. The visible scrollbar is removed; touch,
  wheel and keyboard scrolling remain available for unusually long content,
  short windows and zoom. Activity pagination exposes all fetched events.
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
