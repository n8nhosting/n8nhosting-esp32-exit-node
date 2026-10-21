#!/bin/bash
# Build the exit-node firmware for the ESP32-C3 Super Mini (4 MB flash, no PSRAM).
# Requires ESP-IDF v5.5.x sourced. If sdkconfig.prov exists (see
# sdkconfig.prov.example) its values are baked in: broker host/port/token, TLS,
# AP auto-off — so flashed devices self-provision.
set -e
DEFS="sdkconfig.defaults;sdkconfig.defaults.esp32c3;sdkconfig.defaults.exitnode;sdkconfig.defaults.c3mini"
[ -f sdkconfig.prov ] && DEFS="$DEFS;sdkconfig.prov" && echo "Using provisioning overlay sdkconfig.prov"
rm -rf build_c3mini
idf.py -B build_c3mini -DSDKCONFIG=build_c3mini/sdkconfig -DSDKCONFIG_DEFAULTS="$DEFS" set-target esp32c3
idf.py -B build_c3mini build
echo; echo "Binaries: build_c3mini/{bootloader/bootloader.bin,partition_table/partition-table.bin,ota_data_initial.bin,esp32_nat_router.bin}"
echo "Offsets:  0x0  0x8000  0xf000  0x20000   (flash_size 4MB, dio, 80m)"
echo "Flash with esptool --chip esp32c3 --after watchdog_reset ... : the default hard_reset"
echo "leaves a C3 Super Mini in ROM download mode on Windows (console silent until replugged)."
