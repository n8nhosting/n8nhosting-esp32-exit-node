# Firmware versions and rollback

Every firmware release is kept, so any board can be moved to any version,
older ones included. Since 2.4.22 one version carries one image per board chip
(**target**: `esp32s3` for the S3 N16R8, `esp32c3` for the C3 Super Mini).

```
flasher/firmware/releases/<version>/<target>/   4 bins per target
flasher/firmware/releases/<version>/manifest.json   ESP Web Tools: one build per target
flasher/firmware/releases.json         index: version, protocol, build time, notes,
                                       "targets": {<target>: {url, size, sha256, built}}
                                       (+ url/size/sha256 of the esp32s3 image for old readers)
flasher/firmware/*.bin                 the "latest" esp32s3 release (old OTA URL still works)
flasher/firmware/esp32c3/*.bin         the "latest" esp32c3 release
flasher/manifest.json                  web flasher default = latest, both chips
```

Releases up to 2.4.21-mux1.4 keep their four bins at the version root; the
tool reads them as `esp32s3` and never moves them.

**The version a board reports carries its chip** (`firmware/CMakeLists.txt`):
release `2.4.22` is `2.4.22-s3` on the S3 N16R8, `2.4.22-c3` on the C3 Super
Mini, `2.4.22-c6` on a future C6 — so the dashboard FW column, the broker log
and the flasher never leave a doubt about which image a board runs. The tool
files all of them under `releases/2.4.22/<target>/`, records each string in
`targets.<target>.reports`, and refuses an image whose suffix disagrees with
the build directory's target (a C3 bin in the S3 folder). The dashboard sends
the per-chip string as the OTA target version, so a push is only "ok" when the
board comes back reporting exactly that.

**2.4.23** adds the board health report: a `STATS` frame on the mux link
every 30 s (internal heap free / low-water / total, PSRAM, CPU load, image /
OTA slot / flash sizes, open streams) that the dashboard shows in the Health
cell. Older brokers ignore the frame; older firmware simply has no sample. The
broker from the same release keeps every board's uptime running between
reports, so 2.4.21/2.4.22 boards stop showing `0m` without an update.

The dashboard phase 2 broker changes add `/nodes` fields `max_streams`,
`link_since`, and per-node `dials_pinned_offline` without a firmware update
or a new wire frame. The stream limit is the one accepted by the live mux
session (24 when older firmware omits it), and the link timestamp starts
only after `READY mux1` succeeds. Older firmware still connects, and nodes
without `STATS` continue to have unknown RAM/CPU rather than zero readings.
These API additions are development changes until the broker/admin stack
is deployed; they do not change archived firmware or the latest/stable release.

The flasher page (`flash.<domain>`) has a version picker for USB flashing and
ESP Web Tools picks the build matching the chip it detects; the dashboard's
**Update FW** lists the same releases and sends each board the image for its
own chip (the broker refuses a mismatch).

## Cutting a release

1. Bump `PROJECT_VER` in `firmware/CMakeLists.txt` (each release needs a new
   version; the tool refuses to overwrite a target's image with a different one).
2. Build each board: `firmware/build_s3n16r8.sh` and `firmware/build_c3mini.sh`.
3. Archive each build (the target comes from the build directory's
   `project_description.json`, the version from the image) and, once tested
   on one board of each kind, make the version the default:

```
tools/fw_release.py add firmware/build_s3n16r8 --status testing \
    --title "one-line headline (Thai)" --notes "what changed" --commit $(git rev-parse --short HEAD)
tools/fw_release.py add firmware/build_c3mini --status testing   # same version, second target
tools/fw_release.py latest <version>                             # needs the esp32s3 image
tools/fw_release.py list
```

   `--title` / `--notes` are what the flasher page's version list and the
   dashboard picker show (given once per version; a second `add` keeps them);
   `--status` is `testing`, `stable` (default) or `legacy`, and promoting a
   release with `latest` makes it `stable`. Commit `flasher/` and redeploy
   the flasher on Coolify.
4. Flashing a C3 by hand: `esptool.py --chip esp32c3 --after watchdog_reset
   write_flash ...` — the default `hard_reset` leaves a C3 Super Mini in ROM
   download mode on Windows (the board looks dead until it is replugged).
4. Optional: tag the commit, `git tag fw-<version> && git push origin fw-<version>`.

## Rolling back

| What | How |
|---|---|
| One board / all boards to an older version | Dashboard → Devices → **Update FW** (or **Update all online…**) → pick the older version. Downgrades are allowed (anti-rollback eFuse is off). |
| What new USB flashes / "latest" get | `tools/fw_release.py latest <version>`, commit, redeploy flasher. |
| A bad image on its own | Automatic, see below. |

Mind the protocol column: `pool` firmware (v1.2) needs a broker that still
accepts pool nodes (the current broker does), and boards on it lose the mux link.

## Automatic rollback (firmware 2.4.21-mux1.3 and later)

The bootloader is built with `CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE`. A newly
OTA'd image boots "on probation" and is only confirmed when its mux link to the
broker comes up. Otherwise the previous image comes back:

- it crashes or reboots before the link is up: the bootloader boots the old image;
- it runs but cannot reach the broker for 5 minutes while the board is
  provisioned (home WiFi + broker set, not blocked): it rolls itself back and reboots.

Boards that are not provisioned (setup mode, rproxy off) accept the new image
after 5 minutes, since there is nothing to test against. Note that a power
cut during those 5 minutes also returns the board to the old image.

Older firmware (up to 2.4.21-mux1.2) confirmed itself right at boot, so the
automatic part applies from the first board that runs mux1.3 onward. Rolling
back *to* an older version is still fine, it just won't auto-revert itself.

The broker tracks each push: the dashboard FW column shows `queued`,
`updating`, `updated`, or **rolled back** (the board rebooted but came back on
its old version). The firmware also logs
`OTA: firmware X ... was rejected earlier (rolled back)` at boot.
