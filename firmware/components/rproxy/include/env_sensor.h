/* Room sensor (BMP280/BME280) and chip temperature for the STATS line. */
#ifndef RPROXY_ENV_SENSOR_H
#define RPROXY_ENV_SENSOR_H

#include "env_stats.h"

/* Start the reading task (CONFIG_RPROXY_ENV_SENSOR); safe to call once at init. */
void env_sensor_start(void);

/* Copy the latest readings; times are 0 until the first successful read. */
void env_sensor_snapshot(rproxy_env_t *out);

#endif
