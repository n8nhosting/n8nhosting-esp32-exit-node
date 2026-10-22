/* Execute the BMP280 compensation and the STATS sensor fields the firmware uses. */
#include <assert.h>
#include <stdio.h>
#include <string.h>
#include "../firmware/components/rproxy/include/bmp280_calc.h"
#include "../firmware/components/rproxy/include/env_stats.h"

static void le16(uint8_t *p, int v) { p[0] = (uint8_t)(v & 0xff); p[1] = (uint8_t)((v >> 8) & 0xff); }

static void compensation(void)
{
    /* Bosch BST-BMP280-DS001 section 8.2 worked example. */
    uint8_t raw[24];
    const int dig[12] = { 27504, 26435, -1000, 36477, -10685, 3024, 2855, 140, -7, 15500, -14600, 6000 };
    for (int i = 0; i < 12; i++) le16(raw + 2 * i, dig[i]);
    bmp280_calib_t c;
    bmp280_parse_calib(raw, &c);
    assert(c.T1 == 27504 && c.T2 == 26435 && c.T3 == -1000 && c.P1 == 36477 && c.P2 == -10685 && c.P9 == 6000);

    int32_t centi_c = 0;
    uint32_t pa = 0;
    assert(bmp280_compensate(&c, 519888, 415148, &centi_c, &pa));
    assert(centi_c == 2508);                 /* 25.08 degC */
    assert(pa == 100653);                    /* 100653 Pa = 1006.53 hPa */

    /* the raw registers pack 20-bit readings MSB first */
    const uint8_t data[6] = { 0x65, 0x5A, 0xC0, 0x7E, 0xED, 0x00 };
    int32_t adc_p, adc_t;
    bmp280_unpack(data, &adc_p, &adc_t);
    assert(adc_p == 415148 && adc_t == 519888);

    /* an unmeasured reading (0x80000) and an all-zero P1 are rejected, not divided by */
    assert(!bmp280_compensate(&c, 0x80000, 415148, &centi_c, &pa));
    bmp280_calib_t broken = c;
    broken.P1 = 0;
    assert(!bmp280_compensate(&broken, 519888, 415148, &centi_c, &pa));
}

static void stats_fields(void)
{
    const int64_t s = 1000000;
    rproxy_env_t e = { .room_at_us = 100 * s, .room_centi_c = 2508, .pressure_pa = 100653,
                       .chip_at_us = 100 * s, .chip_centi_c = 4510 };
    char buf[96] = "";
    int n = rproxy_env_append(buf, sizeof(buf), &e, 130 * s);
    assert(strcmp(buf, " tempc=2508 press=100653 chiptempc=4510") == 0 && n == (int)strlen(buf));

    /* a reading older than three periods is dropped, so a sensor that stops
     * answering disappears from the dashboard instead of freezing on old data */
    buf[0] = 0;
    e.room_at_us = 100 * s - RPROXY_ENV_FRESH_US;
    rproxy_env_append(buf, sizeof(buf), &e, 100 * s + 1);
    assert(strcmp(buf, " chiptempc=4510") == 0);

    /* never read (no sensor wired) sends nothing; a cold room keeps its sign */
    buf[0] = 0;
    rproxy_env_t none = { 0 };
    assert(rproxy_env_append(buf, sizeof(buf), &none, 130 * s) == 0 && buf[0] == 0);
    rproxy_env_t cold = { .room_at_us = 100 * s, .room_centi_c = -512, .pressure_pa = 99000 };
    rproxy_env_append(buf, sizeof(buf), &cold, 100 * s);
    assert(strcmp(buf, " tempc=-512 press=99000") == 0);

    /* fields that do not fit are left out whole, never cut mid-number */
    char small[12] = "";
    assert(rproxy_env_append(small, sizeof(small), &e, 130 * s) == 0 && small[0] == 0);
}

int main(void)
{
    compensation();
    stats_fields();
    puts("{\"ok\":true,\"groups\":2}");
    return 0;
}
