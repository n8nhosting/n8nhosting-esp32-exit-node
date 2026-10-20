# Firmware notes

This is a fork of [esp32_nat_router](https://github.com/martin-ger/esp32_nat_router)
(MIT) with one added component, **`rproxy`** (`components/rproxy/`), that turns
the board into a reverse exit node for the proxy platform.

- `components/rproxy/` — opens an outbound pool of connections to the VPS broker
  and relays customer dials out through the home-WiFi (STA) uplink. Console
  command: `rproxy`. See `components/rproxy/include/rproxy.h` for the protocol.
- `components/led_strip`, `components/espressif__mdns` — vendored from GitHub
  because the Espressif component registry is not always reachable at build time.
- `sdkconfig.defaults.s3n16r8` + `partitions_16mb.csv` — the N16R8 target
  (16MB flash, Octal PSRAM, dual 3MB OTA slots).

Build: `./build_s3n16r8.sh` (after sourcing ESP-IDF v5.5.x). Prebuilt binaries
for S3 N16R8 are in `prebuilt_s3n16r8/`.
