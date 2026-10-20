#!/bin/bash
# Build the exit-node firmware for ESP32-S3 N16R8 (16MB flash / 8MB Octal PSRAM).
# Requires ESP-IDF v5.5.x sourced (. $IDF_PATH/export.sh).
set -e
idf.py -B build_s3n16r8 \
  -DSDKCONFIG_DEFAULTS="sdkconfig.defaults;sdkconfig.defaults.esp32s3;sdkconfig.defaults.s3n16r8" \
  set-target esp32s3
idf.py -B build_s3n16r8 build
echo
echo "Flash with:"
echo "  idf.py -B build_s3n16r8 -p <PORT> flash monitor"
echo "or esptool (offsets): bootloader@0x0  partition-table@0x8000  ota_data@0xf000  app@0x20000"
