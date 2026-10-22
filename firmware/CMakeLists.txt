# The following lines of boilerplate have to be in your project's CMakeLists
# in this exact order for cmake to work correctly
cmake_minimum_required(VERSION 3.16)

# The version a board reports (HELLO, dashboard FW column, flasher) carries its
# chip so an image can never be mistaken for another board's: 2.4.22-s3,
# 2.4.22-c3, 2.4.22-c6. tools/fw_release.py files all of them under release
# 2.4.22 and refuses an image whose suffix disagrees with the build target.
set(EXITNODE_RELEASE "2.4.26")
string(REPLACE "esp32" "" _chip "${IDF_TARGET}")
if(_chip STREQUAL "")
    set(_chip "esp32")
endif()
set(PROJECT_VER "${EXITNODE_RELEASE}-${_chip}")
include($ENV{IDF_PATH}/tools/cmake/project.cmake)
project(esp32_nat_router)

# Force esp_app_desc.c to recompile on every build so that
# __DATE__/__TIME__ in the app descriptor stay current.
set(APP_DESC_OBJ "${CMAKE_BINARY_DIR}/esp-idf/esp_app_format/CMakeFiles/__idf_esp_app_format.dir/esp_app_desc.c.obj")
add_custom_target(force_app_desc_rebuild ALL
    COMMAND ${CMAKE_COMMAND} -E remove -f "${APP_DESC_OBJ}"
    COMMENT "Ensuring app descriptor timestamp is current"
)
add_dependencies(${PROJECT_NAME}.elf force_app_desc_rebuild)
