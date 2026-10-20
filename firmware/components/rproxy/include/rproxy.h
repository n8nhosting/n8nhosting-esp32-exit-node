/*
 * rproxy - Reverse exit-node dialer for ESP32 NAT Router
 *
 * The ESP32 sits behind home NAT and cannot be reached from the Internet.
 * Instead it opens a pool of OUTBOUND connections to a broker running on the
 * VPS and keeps them idle. When a customer connects to the VPS proxy (GOST),
 * the broker hands a pending request to one idle ESP32 connection as a line:
 *
 *     DIAL <host> <port>\n
 *
 * The ESP32 resolves <host> (via the home DNS, giving a Thai-exit result),
 * connects out through its STA (home WiFi) uplink, answers:
 *
 *     OK\n            (connected, start relaying)   or
 *     ERR <reason>\n  (refused / blocked / failed)
 *
 * and then relays bytes in both directions until either side closes. When a
 * dial finishes, the ESP32 opens a fresh pool connection to keep the pool full.
 *
 * Handshake on every pool connection, before any DIAL:
 *     HELLO <token>\n   ->   READY\n   (broker accepts)  or connection closed
 *
 * SECURITY NOTES
 *  - DNS results are checked AFTER resolution: private / loopback / link-local
 *    / CGNAT ranges are refused so a customer cannot pivot into the home LAN.
 *  - Outbound ports 25 / 465 / 587 are refused to limit spam abuse.
 *  - The pool link is plain TCP unless built/configured with TLS. Customer
 *    HTTPS traffic stays end-to-end encrypted regardless; plain-HTTP customer
 *    traffic is readable on the ESP32<->VPS hop. Use a long random token and
 *    firewall the broker port to trusted source IPs during testing.
 *
 * SPDX-License-Identifier: MIT
 */
#pragma once

#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

#define RPROXY_MAX_POOL        8      /* hard ceiling on concurrent workers   */
#define RPROXY_DEFAULT_POOL    4
#define RPROXY_DEFAULT_PORT    7000
#define RPROXY_RELAY_BUF       4096   /* per-direction relay buffer (bytes)    */

typedef struct {
    bool     enabled;                 /* start the pool on boot                */
    bool     use_tls;                 /* wrap pool link in TLS (esp-tls)       */
    char     host[64];                /* broker host / VPS public address      */
    uint16_t port;                    /* broker pool port                      */
    uint8_t  pool;                    /* number of idle workers to maintain    */
    char     token[64];               /* shared secret sent in HELLO           */
} rproxy_config_t;

typedef struct {
    bool      running;                /* task group is up                      */
    uint8_t   configured_pool;
    uint8_t   connected;              /* workers with a live pool link         */
    uint8_t   dialing;                /* workers currently relaying a customer */
    uint32_t  total_dials;            /* successful DIALs since boot           */
    uint32_t  failed_dials;           /* DIALs that ended in ERR               */
    uint32_t  blocked_dials;          /* DIALs refused by the safety filter    */
    uint64_t  bytes_up;               /* customer -> target                    */
    uint64_t  bytes_down;             /* target -> customer                    */
} rproxy_status_t;

/* Coarse link state for the web UI status row. */
typedef enum {
    RPROXY_LINK_DISABLED = 0,   /* feature off                                */
    RPROXY_LINK_NO_UPLINK,      /* enabled but STA has no IP yet              */
    RPROXY_LINK_CONNECTING,     /* trying to reach the broker                 */
    RPROXY_LINK_ONLINE,         /* at least one worker connected to broker    */
    RPROXY_LINK_REJECTED        /* broker refused us (bad token / blocked)    */
} rproxy_link_state_t;

/* One-line state for the web UI: "gost online" / "connecting" / "error". */
rproxy_link_state_t rproxy_link_state(void);
const char *rproxy_link_state_str(rproxy_link_state_t s);

/* Load config from NVS and, if enabled, start the worker pool. Call once from
 * app_main after the network stack and STA are initialised. */
esp_err_t rproxy_init(void);

/* Register the `rproxy` console command (serial + remote console). */
void rproxy_register_console(void);

/* Runtime control (all persist to NVS). */
esp_err_t rproxy_enable(void);
esp_err_t rproxy_disable(void);
esp_err_t rproxy_set_server(const char *host, uint16_t port);
esp_err_t rproxy_set_token(const char *token);
esp_err_t rproxy_set_pool(uint8_t n);
esp_err_t rproxy_set_tls(bool on);

void rproxy_get_config(rproxy_config_t *out);
void rproxy_get_status(rproxy_status_t *out);

#ifdef __cplusplus
}
#endif
