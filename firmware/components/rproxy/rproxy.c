/*
 * rproxy - Reverse exit-node dialer for ESP32 NAT Router.
 * See rproxy.h for the protocol and security notes.
 * SPDX-License-Identifier: MIT
 */
#include "rproxy.h"

#include <string.h>
#include <stdlib.h>
#include <stdio.h>
#include <errno.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/semphr.h"

#include "esp_log.h"
#include "esp_netif.h"
#include "esp_heap_caps.h"
#include "nvs_flash.h"
#include "nvs.h"

#include "lwip/sockets.h"
#include "lwip/netdb.h"
#include "lwip/inet.h"

#include "esp_console.h"
#include "argtable3/argtable3.h"

static const char *TAG = "rproxy";

#define NVS_NS            "rproxy"
#define CONNECT_TIMEOUT_S 10
#define DIAL_TIMEOUT_S    10
#define BACKOFF_MIN_MS    1000
#define BACKOFF_MAX_MS    20000

/* ---- state ------------------------------------------------------------- */

typedef enum { W_IDLE_OFF = 0, W_CONNECTING, W_READY, W_DIALING } worker_state_t;

static rproxy_config_t s_cfg;
static SemaphoreHandle_t s_lock;        /* guards s_cfg and the counters     */
static volatile bool s_run;             /* worker tasks should keep running  */
static volatile worker_state_t s_wstate[RPROXY_MAX_POOL];
static TaskHandle_t s_wtask[RPROXY_MAX_POOL];

static uint32_t s_total_dials, s_failed_dials, s_blocked_dials;
static uint64_t s_bytes_up, s_bytes_down;

/* ---- config persistence ------------------------------------------------ */

static void cfg_defaults(rproxy_config_t *c)
{
    memset(c, 0, sizeof(*c));
    c->enabled = false;
    c->use_tls = false;
    c->port    = RPROXY_DEFAULT_PORT;
    c->pool    = RPROXY_DEFAULT_POOL;
    c->host[0] = 0;
    c->token[0] = 0;
}

static void cfg_load(rproxy_config_t *c)
{
    cfg_defaults(c);
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) return;
    size_t n;
    uint8_t b;
    n = sizeof(c->host);  nvs_get_str(h, "host", c->host, &n);
    n = sizeof(c->token); nvs_get_str(h, "token", c->token, &n);
    uint16_t port; if (nvs_get_u16(h, "port", &port) == ESP_OK) c->port = port;
    if (nvs_get_u8(h, "pool", &b) == ESP_OK && b >= 1 && b <= RPROXY_MAX_POOL) c->pool = b;
    if (nvs_get_u8(h, "enabled", &b) == ESP_OK) c->enabled = b;
    if (nvs_get_u8(h, "tls", &b) == ESP_OK) c->use_tls = b;
    nvs_close(h);
}

static void cfg_save(const rproxy_config_t *c)
{
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) != ESP_OK) return;
    nvs_set_str(h, "host", c->host);
    nvs_set_str(h, "token", c->token);
    nvs_set_u16(h, "port", c->port);
    nvs_set_u8(h, "pool", c->pool);
    nvs_set_u8(h, "enabled", c->enabled ? 1 : 0);
    nvs_set_u8(h, "tls", c->use_tls ? 1 : 0);
    nvs_commit(h);
    nvs_close(h);
}

/* ---- safety filter ----------------------------------------------------- */

/* Refuse addresses that could pivot into the home LAN or the loopback/link
 * local space. Only global-unicast destinations are allowed out. */
static bool ipv4_is_blocked(uint32_t host_order)
{
    uint8_t a = (host_order >> 24) & 0xff;
    uint8_t b = (host_order >> 16) & 0xff;
    if (a == 0)                      return true;  /* 0.0.0.0/8                */
    if (a == 10)                     return true;  /* 10.0.0.0/8               */
    if (a == 127)                    return true;  /* loopback                 */
    if (a == 169 && b == 254)        return true;  /* link-local               */
    if (a == 172 && (b & 0xf0) == 16) return true; /* 172.16.0.0/12            */
    if (a == 192 && b == 168)        return true;  /* 192.168.0.0/16           */
    if (a == 100 && (b & 0xc0) == 64) return true; /* 100.64.0.0/10 (CGNAT)    */
    if (a == 192 && b == 0  && ((host_order >> 8) & 0xff) == 0) return true; /* 192.0.0.0/24 */
    if (a >= 224)                    return true;  /* multicast + reserved     */
    return false;
}

static bool port_is_blocked(uint16_t port)
{
    return (port == 0 || port == 25 || port == 465 || port == 587);
}

/* ---- socket helpers ---------------------------------------------------- */

static bool sta_has_ip(void)
{
    esp_netif_t *sta = esp_netif_get_handle_from_ifkey("WIFI_STA_DEF");
    if (!sta) return false;
    esp_netif_ip_info_t ip;
    if (esp_netif_get_ip_info(sta, &ip) != ESP_OK) return false;
    return ip.ip.addr != 0;
}

static void set_timeout(int fd, int secs)
{
    struct timeval tv = { .tv_sec = secs, .tv_usec = 0 };
    setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));
}

/* Resolve+connect to host:port, validating the resolved IP. Returns fd or
 * -1 (dial failure) or -2 (blocked by the safety filter). */
static int dial_target(const char *host, uint16_t port)
{
    if (port_is_blocked(port)) return -2;

    struct addrinfo hints = { .ai_family = AF_INET, .ai_socktype = SOCK_STREAM };
    struct addrinfo *res = NULL;
    char portstr[6];
    snprintf(portstr, sizeof(portstr), "%u", (unsigned)port);
    if (getaddrinfo(host, portstr, &hints, &res) != 0 || !res) return -1;

    int fd = -1;
    for (struct addrinfo *ai = res; ai; ai = ai->ai_next) {
        if (ai->ai_family != AF_INET) continue;
        struct sockaddr_in *sa = (struct sockaddr_in *)ai->ai_addr;
        uint32_t hostorder = ntohl(sa->sin_addr.s_addr);
        if (ipv4_is_blocked(hostorder)) { fd = -2; break; }

        fd = socket(AF_INET, SOCK_STREAM, 0);
        if (fd < 0) { fd = -1; continue; }
        set_timeout(fd, CONNECT_TIMEOUT_S);
        if (connect(fd, ai->ai_addr, ai->ai_addrlen) == 0) break;
        close(fd); fd = -1;
    }
    freeaddrinfo(res);
    return fd;
}

static int connect_broker(const char *host, uint16_t port)
{
    struct addrinfo hints = { .ai_family = AF_INET, .ai_socktype = SOCK_STREAM };
    struct addrinfo *res = NULL;
    char portstr[6];
    snprintf(portstr, sizeof(portstr), "%u", (unsigned)port);
    if (getaddrinfo(host, portstr, &hints, &res) != 0 || !res) return -1;

    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd >= 0) {
        set_timeout(fd, CONNECT_TIMEOUT_S);
        if (connect(fd, res->ai_addr, res->ai_addrlen) != 0) { close(fd); fd = -1; }
    }
    freeaddrinfo(res);
    return fd;
}

/* Read one '\n'-terminated line (CR stripped) into buf. Returns length, or
 * -1 on error/EOF/timeout. */
static int read_line(int fd, char *buf, int cap)
{
    int n = 0;
    while (n < cap - 1) {
        char c;
        int r = recv(fd, &c, 1, 0);
        if (r <= 0) return -1;
        if (c == '\n') break;
        if (c != '\r') buf[n++] = c;
    }
    buf[n] = 0;
    return n;
}

static bool write_all(int fd, const char *data, int len)
{
    int off = 0;
    while (off < len) {
        int w = send(fd, data + off, len - off, 0);
        if (w <= 0) return false;
        off += w;
    }
    return true;
}

/* Bidirectional relay between the customer link (a) and the target (b).
 * Buffers are drawn from PSRAM when available. */
static void relay(int a, int b, char *buf_a, char *buf_b)
{
    struct timeval tv = { .tv_sec = 0, .tv_usec = 0 };
    setsockopt(a, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
    setsockopt(b, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));

    int maxfd = (a > b ? a : b) + 1;
    uint64_t up = 0, down = 0;

    while (s_run) {
        fd_set rs;
        FD_ZERO(&rs);
        FD_SET(a, &rs);
        FD_SET(b, &rs);
        struct timeval sel = { .tv_sec = 30, .tv_usec = 0 };
        int r = select(maxfd, &rs, NULL, NULL, &sel);
        if (r < 0) break;
        if (r == 0) continue;

        if (FD_ISSET(a, &rs)) {
            int n = recv(a, buf_a, RPROXY_RELAY_BUF, 0);
            if (n <= 0) break;
            if (!write_all(b, buf_a, n)) break;
            up += n;
        }
        if (FD_ISSET(b, &rs)) {
            int n = recv(b, buf_b, RPROXY_RELAY_BUF, 0);
            if (n <= 0) break;
            if (!write_all(a, buf_b, n)) break;
            down += n;
        }
    }

    xSemaphoreTake(s_lock, portMAX_DELAY);
    s_bytes_up += up;
    s_bytes_down += down;
    xSemaphoreGive(s_lock);
}

/* ---- worker ------------------------------------------------------------ */

static void backoff_delay(int *ms)
{
    vTaskDelay(pdMS_TO_TICKS(*ms));
    *ms *= 2;
    if (*ms > BACKOFF_MAX_MS) *ms = BACKOFF_MAX_MS;
}

static void worker_task(void *arg)
{
    int id = (int)(intptr_t)arg;
    int backoff = BACKOFF_MIN_MS;

    char *buf_a = heap_caps_malloc(RPROXY_RELAY_BUF, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    char *buf_b = heap_caps_malloc(RPROXY_RELAY_BUF, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    if (!buf_a) buf_a = malloc(RPROXY_RELAY_BUF);
    if (!buf_b) buf_b = malloc(RPROXY_RELAY_BUF);
    if (!buf_a || !buf_b) {
        ESP_LOGE(TAG, "w%d: no memory for relay buffers", id);
        free(buf_a); free(buf_b);
        s_wstate[id] = W_IDLE_OFF;
        s_wtask[id] = NULL;
        vTaskDelete(NULL);
        return;
    }

    while (s_run) {
        if (!sta_has_ip()) { s_wstate[id] = W_CONNECTING; vTaskDelay(pdMS_TO_TICKS(1000)); continue; }

        xSemaphoreTake(s_lock, portMAX_DELAY);
        char host[64]; strlcpy(host, s_cfg.host, sizeof(host));
        char token[64]; strlcpy(token, s_cfg.token, sizeof(token));
        uint16_t port = s_cfg.port;
        bool tls = s_cfg.use_tls;
        xSemaphoreGive(s_lock);

        if (host[0] == 0) { vTaskDelay(pdMS_TO_TICKS(2000)); continue; }
        if (tls) {
            /* Phase 1 ships plain TCP only; TLS is a planned upgrade. Warn once
             * per reconnect so the operator notices the link is not encrypted. */
            ESP_LOGW(TAG, "w%d: TLS requested but not built in; using plain TCP", id);
        }

        s_wstate[id] = W_CONNECTING;
        int fd = connect_broker(host, port);
        if (fd < 0) { backoff_delay(&backoff); continue; }

        /* HELLO handshake */
        char line[300];
        int hlen = snprintf(line, sizeof(line), "HELLO %s\n", token);
        if (!write_all(fd, line, hlen)) { close(fd); backoff_delay(&backoff); continue; }
        set_timeout(fd, CONNECT_TIMEOUT_S);
        if (read_line(fd, line, sizeof(line)) < 0 || strncmp(line, "READY", 5) != 0) {
            ESP_LOGW(TAG, "w%d: broker did not accept HELLO (%s)", id, line);
            close(fd); backoff_delay(&backoff); continue;
        }
        backoff = BACKOFF_MIN_MS;     /* good connection, reset backoff        */
        s_wstate[id] = W_READY;

        /* Wait (idle) for one DIAL line. No recv timeout while idle. */
        struct timeval none = { 0, 0 };
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &none, sizeof(none));
        int ll = read_line(fd, line, sizeof(line));
        if (ll < 0) { close(fd); continue; }   /* broker closed idle link      */

        /* Parse "DIAL <host> <port>" */
        char dhost[200]; unsigned dport = 0;
        if (sscanf(line, "DIAL %199s %u", dhost, &dport) != 2 || dport == 0 || dport > 65535) {
            write_all(fd, "ERR badreq\n", 11);
            close(fd); continue;
        }

        s_wstate[id] = W_DIALING;
        set_timeout(fd, DIAL_TIMEOUT_S);
        int tfd = dial_target(dhost, (uint16_t)dport);
        if (tfd == -2) {
            write_all(fd, "ERR blocked\n", 12);
            xSemaphoreTake(s_lock, portMAX_DELAY); s_blocked_dials++; xSemaphoreGive(s_lock);
            ESP_LOGW(TAG, "w%d: blocked dial to %s:%u", id, dhost, dport);
            close(fd); continue;
        }
        if (tfd < 0) {
            write_all(fd, "ERR dial\n", 9);
            xSemaphoreTake(s_lock, portMAX_DELAY); s_failed_dials++; xSemaphoreGive(s_lock);
            close(fd); continue;
        }
        if (!write_all(fd, "OK\n", 3)) { close(fd); close(tfd); continue; }

        xSemaphoreTake(s_lock, portMAX_DELAY); s_total_dials++; xSemaphoreGive(s_lock);
        ESP_LOGI(TAG, "w%d: dial %s:%u", id, dhost, dport);

        relay(fd, tfd, buf_a, buf_b);
        close(tfd);
        close(fd);
        /* loop immediately to refill the pool */
    }

    free(buf_a); free(buf_b);
    s_wstate[id] = W_IDLE_OFF;
    s_wtask[id] = NULL;
    vTaskDelete(NULL);
}

/* ---- lifecycle --------------------------------------------------------- */

static void start_pool(void)
{
    if (s_run) return;
    s_run = true;
    uint8_t n = s_cfg.pool;
    if (n < 1) n = 1;
    if (n > RPROXY_MAX_POOL) n = RPROXY_MAX_POOL;
    for (int i = 0; i < n; i++) {
        if (s_wtask[i]) continue;
        s_wstate[i] = W_CONNECTING;
        xTaskCreate(worker_task, "rproxy_w", 6144, (void *)(intptr_t)i,
                    tskIDLE_PRIORITY + 3, &s_wtask[i]);
    }
    ESP_LOGI(TAG, "pool started: %u workers -> %s:%u", n, s_cfg.host, s_cfg.port);
}

static void stop_pool(void)
{
    if (!s_run) return;
    s_run = false;
    /* Workers exit on their next select/recv wakeup (<=30s). They self-delete. */
    ESP_LOGI(TAG, "pool stopping");
}

esp_err_t rproxy_init(void)
{
    s_lock = xSemaphoreCreateMutex();
    if (!s_lock) return ESP_ERR_NO_MEM;
    cfg_load(&s_cfg);
    ESP_LOGI(TAG, "init: enabled=%d host=%s:%u pool=%u tls=%d",
             s_cfg.enabled, s_cfg.host, s_cfg.port, s_cfg.pool, s_cfg.use_tls);
    if (s_cfg.enabled && s_cfg.host[0]) start_pool();
    return ESP_OK;
}

/* ---- control API ------------------------------------------------------- */

esp_err_t rproxy_enable(void)
{
    xSemaphoreTake(s_lock, portMAX_DELAY);
    s_cfg.enabled = true; cfg_save(&s_cfg);
    xSemaphoreGive(s_lock);
    start_pool();
    return ESP_OK;
}

esp_err_t rproxy_disable(void)
{
    xSemaphoreTake(s_lock, portMAX_DELAY);
    s_cfg.enabled = false; cfg_save(&s_cfg);
    xSemaphoreGive(s_lock);
    stop_pool();
    return ESP_OK;
}

esp_err_t rproxy_set_server(const char *host, uint16_t port)
{
    if (!host || strlen(host) >= sizeof(s_cfg.host) || port == 0) return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(s_lock, portMAX_DELAY);
    strlcpy(s_cfg.host, host, sizeof(s_cfg.host));
    s_cfg.port = port; cfg_save(&s_cfg);
    xSemaphoreGive(s_lock);
    return ESP_OK;
}

esp_err_t rproxy_set_token(const char *token)
{
    if (!token || strlen(token) >= sizeof(s_cfg.token)) return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(s_lock, portMAX_DELAY);
    strlcpy(s_cfg.token, token, sizeof(s_cfg.token)); cfg_save(&s_cfg);
    xSemaphoreGive(s_lock);
    return ESP_OK;
}

esp_err_t rproxy_set_pool(uint8_t n)
{
    if (n < 1 || n > RPROXY_MAX_POOL) return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(s_lock, portMAX_DELAY);
    s_cfg.pool = n; cfg_save(&s_cfg);
    xSemaphoreGive(s_lock);
    return ESP_OK;  /* takes effect on next enable/restart */
}

esp_err_t rproxy_set_tls(bool on)
{
    xSemaphoreTake(s_lock, portMAX_DELAY);
    s_cfg.use_tls = on; cfg_save(&s_cfg);
    xSemaphoreGive(s_lock);
    return ESP_OK;
}

void rproxy_get_config(rproxy_config_t *out)
{
    xSemaphoreTake(s_lock, portMAX_DELAY);
    *out = s_cfg;
    xSemaphoreGive(s_lock);
}

void rproxy_get_status(rproxy_status_t *out)
{
    memset(out, 0, sizeof(*out));
    xSemaphoreTake(s_lock, portMAX_DELAY);
    out->running = s_run;
    out->configured_pool = s_cfg.pool;
    out->total_dials = s_total_dials;
    out->failed_dials = s_failed_dials;
    out->blocked_dials = s_blocked_dials;
    out->bytes_up = s_bytes_up;
    out->bytes_down = s_bytes_down;
    xSemaphoreGive(s_lock);
    for (int i = 0; i < RPROXY_MAX_POOL; i++) {
        if (s_wstate[i] == W_READY) out->connected++;
        else if (s_wstate[i] == W_DIALING) { out->connected++; out->dialing++; }
    }
}

/* ---- console command --------------------------------------------------- */

static struct {
    struct arg_str *action;
    struct arg_str *a1;
    struct arg_str *a2;
    struct arg_end *end;
} rp_args;

static int cmd_rproxy(int argc, char **argv)
{
    int nerr = arg_parse(argc, argv, (void **)&rp_args);
    if (nerr != 0) {
        printf("Usage: rproxy <status|enable|disable|server <host> <port>|token <t>|pool <n>|tls <on|off>>\n");
        return 1;
    }
    const char *act = rp_args.action->count ? rp_args.action->sval[0] : "status";

    if (strcmp(act, "status") == 0) {
        rproxy_config_t c; rproxy_status_t s;
        rproxy_get_config(&c); rproxy_get_status(&s);
        printf("rproxy configuration:\n");
        printf("  enabled:  %s\n", c.enabled ? "yes" : "no");
        printf("  broker:   %s:%u\n", c.host[0] ? c.host : "(unset)", c.port);
        printf("  token:    %s\n", c.token[0] ? "(set)" : "(unset)");
        printf("  pool:     %u\n", c.pool);
        printf("  tls:      %s%s\n", c.use_tls ? "on" : "off",
               c.use_tls ? " (NOTE: not built in; link is plain TCP)" : "");
        printf("rproxy status:\n");
        printf("  running:  %s\n", s.running ? "yes" : "no");
        printf("  uplink:   %s\n", sta_has_ip() ? "STA has IP" : "no STA IP");
        printf("  workers:  %u connected, %u dialing\n", s.connected, s.dialing);
        printf("  dials:    %lu ok, %lu failed, %lu blocked\n",
               (unsigned long)s.total_dials, (unsigned long)s.failed_dials,
               (unsigned long)s.blocked_dials);
        printf("  traffic:  up %llu B, down %llu B\n",
               (unsigned long long)s.bytes_up, (unsigned long long)s.bytes_down);
        if (c.use_tls)
            printf("\nWARNING: ESP32<->VPS hop is NOT encrypted in this build.\n");
        return 0;
    }
    if (strcmp(act, "enable") == 0) {
        rproxy_config_t c; rproxy_get_config(&c);
        if (!c.host[0] || !c.token[0]) { printf("Set 'server' and 'token' first.\n"); return 1; }
        rproxy_enable(); printf("rproxy enabled.\n"); return 0;
    }
    if (strcmp(act, "disable") == 0) { rproxy_disable(); printf("rproxy disabled.\n"); return 0; }
    if (strcmp(act, "server") == 0) {
        if (!rp_args.a1->count || !rp_args.a2->count) { printf("Usage: rproxy server <host> <port>\n"); return 1; }
        int p = atoi(rp_args.a2->sval[0]);
        if (p < 1 || p > 65535) { printf("Invalid port.\n"); return 1; }
        if (rproxy_set_server(rp_args.a1->sval[0], (uint16_t)p) == ESP_OK)
            printf("Broker set to %s:%d. Re-enable to apply.\n", rp_args.a1->sval[0], p);
        else printf("Invalid server.\n");
        return 0;
    }
    if (strcmp(act, "token") == 0) {
        if (!rp_args.a1->count) { printf("Usage: rproxy token <token>\n"); return 1; }
        if (rproxy_set_token(rp_args.a1->sval[0]) == ESP_OK) printf("Token set.\n");
        else printf("Token too long.\n");
        return 0;
    }
    if (strcmp(act, "pool") == 0) {
        if (!rp_args.a1->count) { printf("Usage: rproxy pool <1-%d>\n", RPROXY_MAX_POOL); return 1; }
        int n = atoi(rp_args.a1->sval[0]);
        if (rproxy_set_pool((uint8_t)n) == ESP_OK) printf("Pool set to %d. Re-enable to apply.\n", n);
        else printf("Pool must be 1-%d.\n", RPROXY_MAX_POOL);
        return 0;
    }
    if (strcmp(act, "tls") == 0) {
        bool on = rp_args.a1->count && strcmp(rp_args.a1->sval[0], "on") == 0;
        rproxy_set_tls(on);
        printf("TLS flag %s%s.\n", on ? "on" : "off",
               on ? " (NOTE: not built in yet; link stays plain TCP)" : "");
        return 0;
    }
    printf("Unknown action '%s'.\n", act);
    return 1;
}

void rproxy_register_console(void)
{
    rp_args.action = arg_str0(NULL, NULL, "<action>", "status|enable|disable|server|token|pool|tls");
    rp_args.a1     = arg_str0(NULL, NULL, "<arg>", "host | token | n | on/off");
    rp_args.a2     = arg_str0(NULL, NULL, "<arg2>", "port");
    rp_args.end    = arg_end(4);
    const esp_console_cmd_t cmd = {
        .command = "rproxy",
        .help = "Reverse exit-node dialer: rproxy <status|enable|disable|server <host> <port>|token <t>|pool <n>|tls <on|off>>",
        .hint = NULL,
        .func = &cmd_rproxy,
        .argtable = &rp_args,
    };
    ESP_ERROR_CHECK(esp_console_cmd_register(&cmd));
}
