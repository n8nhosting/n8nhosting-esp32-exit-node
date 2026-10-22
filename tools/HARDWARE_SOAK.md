# Read-only hardware soak recorder

Live run (22 October 2026): testing firmware 2.4.25 is being recorded
from **13:26:02 +07** for 24 hours in container
`exitnode-soak-20261022-v2425-i18n`, bound to the broker ending
`20261022T062427` (runtime `7a6f884`). Expected duration finish:
**23 October 2026, 13:26:02 +07**.
Private server output: `/root/exitnode-soak-20261022-0550/evidence/run-5/`.
The first two samples show all three boards online and zero polling errors.
This is **running evidence collection**, not a completed acceptance result.

**Update 22 October 2026:** the owner approved deploying `d2cc62b` before run 5
finished. Coolify deployment `14f7fnic34j3j7gm2ahfkkly` replaced the broker between
**16:41:40 and 16:43:03 +07**, so run 5 is **interrupted** after about 3h15m. Count
only samples before 16:41:40 +07. The old recorder stays bound to the replaced
broker's namespace and records failed polls until someone stops it. Stop it with
SIGTERM (giving `completed:false`), keep it as interrupted evidence, and start
run 6 in a new output directory bound to the new broker.

Runs 1-4 are retained as interrupted evidence with `completed:false`:
run 1 warm-up (9 samples), run 2 (416 samples, 6h55m40s, zero poll failures),
and runs 3/4 (5 samples each, zero poll failures), interrupted by runtime
deployments. See the [current portal handoff](../docs/dashboard-review/07-portal-language-seo-sem-analytics.md#hardware-evidence-continuity).

The recorder shares its broker container's network namespace; after a
deployment replaces that broker, start a fresh recorder and new output directory
and preserve the previous run as interrupted evidence. Do not promote firmware
from elapsed time alone.

Prepared for a reviewed, unattended 24-hour canary. Creating this tool does not
start a soak, certify hardware, change firmware, or promote a release.

Use Node.js 20+ from a machine that will remain awake and connected. The admin
mode reads `/api/devices` and `/api/overview`; the broker mode reads `/nodes`
and `/status`. Only GET requests are made. This tool does not generate load,
assign customers, change settings, initiate OTA, or open a serial port.

## Start after the canary and collection scope are approved

For admin collection, privately configure `ADMIN_TOKEN` in the launching
process environment. Do not put its value on the command line, in screenshots,
in shell history, or in a report. `--token-env` selects a different environment
variable by name when needed.

```powershell
node tools/hardware_soak.cjs --source admin --url https://admin.jbacteam.com --hours 24 --interval 60 --out output/soak-2026-10-22-canary
```

Alternatively run next to a private broker listener, without an admin token:

```powershell
node tools/hardware_soak.cjs --source broker --url http://127.0.0.1:7001 --hours 24 --interval 60 --out output/soak-2026-10-22-broker
```

The output directory **must not exist**. Choose a new directory for each run.
Optional `--mac ac:a7:04:ba:ae:c0` limits recorded devices; comma-separated MACs
are supported. HTTP is accepted only on localhost; remote collection uses
HTTPS. URLs containing credentials or query parameters are rejected.

The foreground command runs unattended until the duration expires. For a
background Windows process use `Start-Process -WindowStyle Hidden` with a
properly quoted script/output path, and preserve the launching environment.
`run.json` records the PID. Review `samples.jsonl` during collection. Ctrl+C
or SIGTERM finishes with `completed:false`; an interrupted run is not a
24-hour result. OS termination or power loss can leave `result.json` absent;
existing samples remain evidence, not a completed run.

## Evidence and acceptance

Files are created exclusively and never overwrite existing state:

- `run.json`: collection scope, timing, PID, and service URL without secrets.
- `samples.jsonl`: allowlisted device health, RSSI, uptime, stream/counter
  values, polling errors, and transitions. Passwords, SSIDs, names, notes,
  customer identities, routing credentials, and arbitrary response fields
  are excluded. Each sample is flushed to disk.
- `result.json`: actual elapsed time, completion, sample/error totals,
  per-board online/offline samples, uptime/counter resets and resource bounds.

A failed poll is recorded as unknown, never as all boards being offline.
Read actual timestamps and gaps; do not synthesize missing samples. This
collector reports sample-based availability, not an exact outage duration.
Device disappearance/return and counter/uptime resets remain explicit events.

Before acceptance compare expected firmware, actual uptime resets, recurring
disconnects, heap low-water/free memory, CPU under known load, RSSI, mux
availability, and broker errors. `completed:true` only means the recorder ran
for the configured duration; it does not assert a hardware pass or change
testing/stable/latest status. Review results and run any approved load tests
separately. A restarted recorder begins a new evidence directory.

Firmware 2.4.25 is a testing release. Its normal overall OTA budget is
180 seconds, configurable at build time from 30 to 600 seconds. Normal
timeouts abort the incomplete image and reconnect without resetting. If an
IDF blocking operation remains stuck, a separate fail-safe restarts the board
after an additional 10 seconds: **190 seconds with the default setting**.
The fail-safe does not touch a live TLS handle from another task. Complete
image/chip verification and the existing rollback mechanism remain in IDF;
the current boot partition stays selected until verified finish. During a
canary, record the actual reconnect and reported firmware before acceptance.

Local recorder tests use a simulated short duration and fake responses:

```powershell
node --test tools/test_hardware_soak.cjs
gcc -std=c11 -Wall -Wextra -Werror tools/test_firmware_ota.c -o output/test_firmware_ota.exe
./output/test_firmware_ota.exe
```
