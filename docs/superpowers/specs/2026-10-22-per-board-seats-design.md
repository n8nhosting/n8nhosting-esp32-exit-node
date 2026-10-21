# Seats counted per board

The operator explicitly selected **independent seats per board, even when
boards share the same public IP**, on 2026-10-22. This supersedes the older
exit-IP capacity and dedicated-IP ownership rules in the Phase 2 reports.

## Contract

- Slot identifies routing to a board; port is 3130 + slot. Automatic slot
  reservation and manual editing remain as implemented.
- Shared board capacity remains zero while unmeasured. A positive measured or
  manually entered capacity gives `min(3,max(1,floor(Mbps/5)))` seats.
- Dedicated reserves one board for one account. Another board on the same
  public IP can independently accept accounts; dedicated pool exclusion remains.
- Seat occupancy is based only on enabled, unexpired, non-measurement accounts
  bound to the target MAC. It is separate from live connection counts.
- Admission checks target-board occupancy, excluding the account itself only
  when it already occupies that target. Moving from a board with the same IP
  cannot bypass a full target. POST assign and PUT user follow the same rule.
- `active_users` and `seats` report board-local occupancy. `max_users` remains
  board capacity. `ip_group` / `ip_group_size` remain informational metadata.
- Overview occupied counts actual bound accounts once; available sums free
  seats independently over eligible online boards. Offline, blocked, updating,
  unassigned and routing-mismatch boards are unavailable for the live KPI.
- Device rows, detail Capacity, both assignment pickers and Overview use the
  same unit. The default user-first picker includes partially occupied boards
  with room. Remove the former same-IP exception from board-first assignment.

## Scope and validation

No schema migration, user rebinding, capacity override or firmware update is
required. Existing records and historical metrics remain intact. Update current
UI help and annotate older documents with this superseding contract.

Validate independent same-IP shared/dedicated boards, target-full rejection,
unmeasured C3 rejection, self-reassignment and transfers, concurrent admission,
per-board device counts and Overview totals. Run both Go modules' tests/vet,
browser regressions and a real isolated assignment through both pickers.
Deploy through the existing manual Coolify process and verify production
counts and picker eligibility without changing customer assignments.
