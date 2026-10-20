idf_component_register(
    SRCS "rproxy.c"
    INCLUDE_DIRS "include"
    REQUIRES nvs_flash esp_netif lwip console esp_timer log esp_hw_support
)
target_compile_options(${COMPONENT_LIB} PRIVATE -Os)
