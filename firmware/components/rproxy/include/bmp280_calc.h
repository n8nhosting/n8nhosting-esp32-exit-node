/* BMP280/BME280 calibration and compensation, independent of IDF for host testing. */
#ifndef RPROXY_BMP280_CALC_H
#define RPROXY_BMP280_CALC_H

#include <stdbool.h>
#include <stdint.h>

typedef struct {
    uint16_t T1;
    int16_t T2, T3;
    uint16_t P1;
    int16_t P2, P3, P4, P5, P6, P7, P8, P9;
} bmp280_calib_t;

/* raw holds registers 0x88..0x9F: twelve little-endian words, T1 and P1 unsigned. */
static inline void bmp280_parse_calib(const uint8_t raw[24], bmp280_calib_t *c)
{
    int16_t w[12];
    for (int i = 0; i < 12; i++) w[i] = (int16_t)(uint16_t)(raw[2 * i] | (raw[2 * i + 1] << 8));
    c->T1 = (uint16_t)w[0]; c->T2 = w[1]; c->T3 = w[2];
    c->P1 = (uint16_t)w[3]; c->P2 = w[4]; c->P3 = w[5]; c->P4 = w[6]; c->P5 = w[7];
    c->P6 = w[8]; c->P7 = w[9]; c->P8 = w[10]; c->P9 = w[11];
}

/* data holds registers 0xF7..0xFC: pressure then temperature, 20 bits each, MSB first. */
static inline void bmp280_unpack(const uint8_t data[6], int32_t *adc_p, int32_t *adc_t)
{
    *adc_p = (int32_t)(((uint32_t)data[0] << 12) | ((uint32_t)data[1] << 4) | (data[2] >> 4));
    *adc_t = (int32_t)(((uint32_t)data[3] << 12) | ((uint32_t)data[4] << 4) | (data[5] >> 4));
}

/* Bosch's integer compensation (datasheet 3.11.3, 64-bit pressure). Gives
 * hundredths of a degree C and whole pascals. False for a skipped measurement
 * (0x80000) or calibration that would divide by zero. */
static inline bool bmp280_compensate(const bmp280_calib_t *c, int32_t adc_t, int32_t adc_p,
                                     int32_t *centi_c, uint32_t *pa)
{
    if (adc_t == 0x80000 || adc_p == 0x80000) return false;
    int32_t v1 = (((adc_t >> 3) - ((int32_t)c->T1 * 2)) * (int32_t)c->T2) >> 11;
    int32_t d = (adc_t >> 4) - (int32_t)c->T1;
    int32_t v2 = (((d * d) >> 12) * (int32_t)c->T3) >> 14;
    int32_t t_fine = v1 + v2;
    *centi_c = (t_fine * 5 + 128) >> 8;

    int64_t a = (int64_t)t_fine - 128000;
    int64_t b = a * a * c->P6;
    b += a * c->P5 * 131072;                    /* << 17 */
    b += (int64_t)c->P4 * 34359738368LL;        /* << 35 */
    a = ((a * a * c->P3) >> 8) + a * c->P2 * 4096;
    a = ((140737488355328LL + a) * c->P1) >> 33; /* (1 << 47) + a */
    if (a == 0) return false;
    int64_t p = 1048576 - adc_p;
    p = ((p * 2147483648LL - b) * 3125) / a;    /* p << 31 */
    a = ((int64_t)c->P9 * (p >> 13) * (p >> 13)) >> 25;
    b = ((int64_t)c->P8 * p) >> 19;
    p = ((p + a + b) >> 8) + (int64_t)c->P7 * 16;
    if (p <= 0) return false;
    *pa = (uint32_t)(p >> 8);                   /* Q24.8 to whole pascals */
    return true;
}

#endif
