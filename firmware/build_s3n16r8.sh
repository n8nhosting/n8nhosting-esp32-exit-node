#!/bin/bash
# Build the exit-node firmware for ESP32-S3 N16R8 (16MB flash / 8MB Octal PSRAM).
# Requires ESP-IDF v5.5.x sourced. If sdkconfig.prov exists (see
# sdkconfig.prov.example) its values are baked in: broker host/port/token, TLS,
# minimal UI, AP auto-off — so flashed devices self-provision.
set -e
DEFS="sdkconfig.defaults;sdkconfig.defaults.esp32s3;sdkconfig.defaults.s3n16r8"
[ -f sdkconfig.prov ] && DEFS="$DEFS;sdkconfig.prov" && echo "Using provisioning overlay sdkconfig.prov"
rm -rf build_s3n16r8
idf.py -B build_s3n16r8 -DSDKCONFIG=build_s3n16r8/sdkconfig -DSDKCONFIG_DEFAULTS="$DEFS" set-target esp32s3
idf.py -B build_s3n16r8 build
echo; echo "Binaries: build_s3n16r8/{bootloader/bootloader.bin,partition_table/partition-table.bin,ota_data_initial.bin,esp32_nat_router.bin}"
echo "Offsets:  0x0  0x8000  0xf000  0x20000   (flash_size 16MB, dio, 80m)"
