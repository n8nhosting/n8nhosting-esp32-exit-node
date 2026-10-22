/* Room and chip temperature fields of the STATS line, independent of IDF for host testing. */
#ifndef RPROXY_ENV_STATS_H
#define RPROXY_ENV_STATS_H

#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

typedef struct {
    int64_t room_at_us;                          /* 0 = never read */
    int32_t room_centi_c;
    uint32_t pressure_pa;
    int64_t chip_at_us;
    int32_t chip_centi_c;
} rproxy_env_t;

/* Readings come every 30 s; one older than three periods is not reported. */
#define RPROXY_ENV_FRESH_US (90LL * 1000000)

/* Append " tempc=<centi degC> press=<Pa> chiptempc=<centi degC>" for the fresh
 * readings to the NUL-terminated buf. Returns the characters appended; 0 when
 * nothing is fresh or the fields do not fit. */
static inline int rproxy_env_append(char *buf, size_t cap, const rproxy_env_t *e, int64_t now_us)
{
    char f[64];
    int n = 0;
    if (e->room_at_us > 0 && now_us - e->room_at_us < RPROXY_ENV_FRESH_US)
        n += snprintf(f + n, sizeof(f) - n, " tempc=%ld press=%lu", (long)e->room_centi_c, (unsigned long)e->pressure_pa);
    if (e->chip_at_us > 0 && now_us - e->chip_at_us < RPROXY_ENV_FRESH_US)
        n += snprintf(f + n, sizeof(f) - n, " chiptempc=%ld", (long)e->chip_centi_c);
    size_t used = strlen(buf);
    if (n <= 0 || used + (size_t)n >= cap) return 0;
    memcpy(buf + used, f, (size_t)n + 1);
    return n;
}

#endif
