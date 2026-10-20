# Firmware versions and rollback

Every firmware release is kept, so any board can be moved to any version,
older ones included.

```
flasher/firmware/releases/<version>/   4 bins + manifest.json (one folder per release)
flasher/firmware/releases.json         index: version, protocol, build time, sha256, notes
flasher/firmware/*.bin                 the "latest" release (old OTA URL still works)
flasher/manifest.json                  web flasher default = latest
```

The flasher page (`flash.<domain>`) has a version picker for USB flashing, and
the dashboard's **Update FW** lists the same releases.

## Cutting a release

1. Bump `PROJECT_VER` in `firmware/CMakeLists.txt` (each release needs a new
   version; the tool refuses to overwrite a release with a different image).
2. Build: `firmware/build_s3n16r8.sh`.
3. Archive it and, once tested on one board, make it the default:

```
tools/fw_release.py add firmware/build_s3n16r8 --status testing \
    --title "one-line headline (Thai)" --notes "what changed" --commit $(git rev-parse --short HEAD)
tools/fw_release.py add firmware/build_s3n16r8 --latest      # same, and make it the default
tools/fw_release.py list
```

   `--title` / `--notes` are what the flasher page's version list and the
   dashboard picker show; `--status` is `testing`, `stable` (default) or
   `legacy`, and promoting a release with `latest` makes it `stable`.
   The version is read from the image itself, so a bin cannot be filed under
   the wrong name. Commit `flasher/` and redeploy the flasher on Coolify.
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
