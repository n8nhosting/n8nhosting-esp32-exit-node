# Read-only hardware soak recorder

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

Local recorder tests use a simulated short duration and fake responses:

```powershell
node --test tools/test_hardware_soak.cjs
gcc -std=c11 -Wall -Wextra -Werror tools/test_firmware_ota.c -o output/test_firmware_ota.exe
./output/test_firmware_ota.exe
```
