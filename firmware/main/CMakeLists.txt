set(main_priv_requires
    esp_wifi esp_netif console nvs_flash driver esp_http_server
    fatfs cmd_router dhcpserver wpa_supplicant cmd_system acl remote_console
    syslog json esp_wireguard esp_eth http_server mqtt_ha app_update
    rproxy)

# Feature gates (menu "Exit node build"): the component is only linked when
# its gate is on, so an exit-node image carries none of its code.
if(CONFIG_FEATURE_PCAP)
    list(APPEND main_priv_requires pcap_capture)
endif()
if(CONFIG_FEATURE_OLED)
    list(APPEND main_priv_requires oled_display)
endif()
if(CONFIG_FEATURE_MDNS)
    list(APPEND main_priv_requires espressif__mdns)
endif()

# ESP32-C5 uses a bit-bang WS2812 driver; all other targets use the RMT-backed
# espressif/led_strip managed component.
if(NOT IDF_TARGET STREQUAL "esp32c5")
    list(APPEND main_priv_requires led_strip)
endif()

idf_component_register(SRCS "esp32_nat_router.c"
                            "portmap.c"
                            "dhcp_manager.c"
                            "vpn_manager.c"
                            "netif_hooks.c"
                            "led_strip_status.c"
                    INCLUDE_DIRS "."
                    PRIV_REQUIRES ${main_priv_requires})
target_compile_options(__idf_main PRIVATE -O2)
# Setup and event-handling files build for size; netif_hooks.c stays on -O2
# because it sits on the packet path. Source-level COMPILE_OPTIONS are emitted
# after the target options, so the later -Os wins for the listed files.
set_source_files_properties(esp32_nat_router.c portmap.c dhcp_manager.c vpn_manager.c led_strip_status.c
    PROPERTIES COMPILE_OPTIONS "-Os")
