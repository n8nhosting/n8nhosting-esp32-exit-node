idf_component_register(
    SRCS "rproxy.c"
    INCLUDE_DIRS "include"
    REQUIRES nvs_flash esp_netif lwip console esp_timer log esp_hw_support esp-tls esp_wifi esp_app_format esp_https_ota esp_http_client app_update esp_partition bootloader_support spi_flash mbedtls cmd_router http_server esp_http_server
    EMBED_TXTFILES "broker_ca.pem"
)
