/* Room sensor and chip temperature for the STATS line.
 *
 * A low-priority task reads a BMP280/BME280 on I2C (forced mode, address 0x76
 * or 0x77) and the chip's own temperature sensor every 30 s; health_send()
 * appends whatever is fresh. Nothing here touches the relay path: a missing
 * or failing sensor only drops its fields from STATS. */
#include "sdkconfig.h"
#if CONFIG_RPROXY_ENV_SENSOR
#include "env_sensor.h"
#include "bmp280_calc.h"

#include <math.h>

#include "driver/i2c_master.h"
#include "driver/temperature_sensor.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "soc/soc_caps.h"

static const char *TAG = "env";

#define ENV_PERIOD_MS   30000
#define ENV_REPROBE     10          /* periods between probes for a missing sensor (5 min) */
#define ENV_MAX_FAILS   3           /* failed reads before the sensor counts as gone */

#define BMP_REG_CALIB   0x88
#define BMP_REG_ID      0xD0
#define BMP_REG_STATUS  0xF3
#define BMP_REG_CTRL    0xF4
#define BMP_REG_CONFIG  0xF5
#define BMP_REG_DATA    0xF7
#define BMP_CTRL_FORCED ((1 << 5) | (3 << 2) | 1)   /* temperature x1, pressure x4, one conversion */

static portMUX_TYPE s_mux = portMUX_INITIALIZER_UNLOCKED;
static rproxy_env_t s_env;

static i2c_master_bus_handle_t s_bus;
static i2c_master_dev_handle_t s_dev;
static bmp280_calib_t s_cal;

static esp_err_t reg_read(uint8_t reg, uint8_t *buf, size_t n)
{
    return i2c_master_transmit_receive(s_dev, &reg, 1, buf, n, 100);
}

static esp_err_t reg_write(uint8_t reg, uint8_t v)
{
    const uint8_t b[2] = { reg, v };
    return i2c_master_transmit(s_dev, b, sizeof(b), 100);
}

static void bmp_detach(void)
{
    if (s_dev) i2c_master_bus_rm_device(s_dev);
    s_dev = NULL;
}

static bool bmp_attach(void)
{
    if (!s_bus) {
        i2c_master_bus_config_t cfg = {
            .i2c_port = -1,
            .sda_io_num = CONFIG_RPROXY_ENV_SDA,
            .scl_io_num = CONFIG_RPROXY_ENV_SCL,
            .clk_source = I2C_CLK_SRC_DEFAULT,
            .glitch_ignore_cnt = 7,
            .flags.enable_internal_pullup = true,   /* breakout boards add their own 10k */
        };
        if (i2c_new_master_bus(&cfg, &s_bus) != ESP_OK) {
            s_bus = NULL;
            return false;
        }
    }
    static const uint8_t addrs[] = { 0x76, 0x77 };
    for (size_t i = 0; i < sizeof(addrs); i++) {
        if (i2c_master_probe(s_bus, addrs[i], 50) != ESP_OK) continue;
        i2c_device_config_t dc = {
            .dev_addr_length = I2C_ADDR_BIT_LEN_7,
            .device_address = addrs[i],
            .scl_speed_hz = 100000,
        };
        if (i2c_master_bus_add_device(s_bus, &dc, &s_dev) != ESP_OK) {
            s_dev = NULL;
            continue;
        }
        uint8_t id = 0, raw[24];
        /* 0x58 BMP280 (0x56/0x57 engineering samples), 0x60 BME280: same T/P registers */
        if (reg_read(BMP_REG_ID, &id, 1) == ESP_OK && (id == 0x58 || id == 0x56 || id == 0x57 || id == 0x60) &&
            reg_read(BMP_REG_CALIB, raw, sizeof(raw)) == ESP_OK && reg_write(BMP_REG_CONFIG, 0x00) == ESP_OK) {
            bmp280_parse_calib(raw, &s_cal);
            ESP_LOGI(TAG, "%s at 0x%02x (SDA %d, SCL %d)", id == 0x60 ? "BME280" : "BMP280", addrs[i],
                     CONFIG_RPROXY_ENV_SDA, CONFIG_RPROXY_ENV_SCL);
            return true;
        }
        bmp_detach();
    }
    return false;
}

static bool bmp_read(int32_t *centi_c, uint32_t *pa)
{
    if (reg_write(BMP_REG_CTRL, BMP_CTRL_FORCED) != ESP_OK) return false;
    uint8_t st = 0x08;
    for (int i = 0; i < 10 && (st & 0x08); i++) {   /* a forced conversion takes ~13 ms */
        vTaskDelay(pdMS_TO_TICKS(10));
        if (reg_read(BMP_REG_STATUS, &st, 1) != ESP_OK) return false;
    }
    uint8_t d[6];
    if ((st & 0x08) || reg_read(BMP_REG_DATA, d, sizeof(d)) != ESP_OK) return false;
    int32_t adc_p, adc_t;
    bmp280_unpack(d, &adc_p, &adc_t);
    return bmp280_compensate(&s_cal, adc_t, adc_p, centi_c, pa);
}

static bool chip_read(int32_t *centi_c)
{
#if SOC_TEMP_SENSOR_SUPPORTED
    /* Installed per reading, as the console's status command does, so either
     * can use it; a reading is skipped while the other holds the sensor. */
    temperature_sensor_handle_t h;
    temperature_sensor_config_t cfg = TEMPERATURE_SENSOR_CONFIG_DEFAULT(20, 100);
    if (temperature_sensor_install(&cfg, &h) != ESP_OK) return false;
    float c = 0;
    bool ok = temperature_sensor_enable(h) == ESP_OK;
    ok = ok && temperature_sensor_get_celsius(h, &c) == ESP_OK;
    temperature_sensor_disable(h);
    temperature_sensor_uninstall(h);
    if (ok) *centi_c = (int32_t)lroundf(c * 100.0f);
    return ok;
#else
    (void)centi_c;
    return false;
#endif
}

static void env_task(void *arg)
{
    (void)arg;
    bool have_bmp = false;
    int probe_in = 0, fails = 0;
    for (;;) {
        if (!have_bmp && --probe_in <= 0) {
            have_bmp = bmp_attach();
            probe_in = ENV_REPROBE;
            fails = 0;
        }
        int32_t room = 0, chip = 0;
        uint32_t pa = 0;
        bool room_ok = have_bmp && bmp_read(&room, &pa);
        if (have_bmp && !room_ok && ++fails >= ENV_MAX_FAILS) {
            ESP_LOGW(TAG, "room sensor stopped answering");
            bmp_detach();
            have_bmp = false;
            probe_in = 1;                            /* look for it again next period */
        }
        if (room_ok) fails = 0;
        bool chip_ok = chip_read(&chip);
        int64_t now = esp_timer_get_time();
        taskENTER_CRITICAL(&s_mux);
        if (room_ok) {
            s_env.room_at_us = now;
            s_env.room_centi_c = room;
            s_env.pressure_pa = pa;
        }
        if (chip_ok) {
            s_env.chip_at_us = now;
            s_env.chip_centi_c = chip;
        }
        taskEXIT_CRITICAL(&s_mux);
        vTaskDelay(pdMS_TO_TICKS(ENV_PERIOD_MS));
    }
}

void env_sensor_start(void)
{
    static bool started;
    if (started) return;
    started = true;
    esp_log_level_set("temperature_sensor", ESP_LOG_WARN);   /* it logs its range on every install */
    if (xTaskCreate(env_task, "env_sensor", 3072, NULL, tskIDLE_PRIORITY + 1, NULL) != pdPASS)
        ESP_LOGE(TAG, "no memory for the sensor task");
}

void env_sensor_snapshot(rproxy_env_t *out)
{
    taskENTER_CRITICAL(&s_mux);
    *out = s_env;
    taskEXIT_CRITICAL(&s_mux);
}
#endif
