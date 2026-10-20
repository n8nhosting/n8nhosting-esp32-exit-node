/*
 * rproxy - Reverse exit-node dialer for ESP32 NAT Router.
 *
 * The pool link to the broker can run over TLS (esp-tls), with the broker
 * certificate verified against a CA embedded in the firmware
 * (components/rproxy/broker_ca.pem). The device identifies itself in the
 * handshake by its STA MAC so individual devices can be blocked server-side:
 *
 *     HELLO <token> <mac> <fw>\n   ->   READY\n   (accepted)
 *                                  ->   DENY ...\n or close (rejected)
 *
 * Provisioning defaults (host/port/token/pool/tls/enabled) can be baked in at
 * build time via Kconfig (CONFIG_RPROXY_DEFAULT_*) so end users never type the
 * token; they are applied only when NVS holds no configuration yet.
 *
 * See rproxy.h for the DIAL protocol and security notes.
 * SPDX-License-Identifier: MIT
 */
#include "rproxy.h"
#include "sdkconfig.h"

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
#include "esp_mac.h"
#include "esp_app_desc.h"
#include "esp_tls.h"
#include "esp_https_ota.h"
#include "esp_http_client.h"
#include "esp_crt_bundle.h"
#include "esp_system.h"
#include "wifi_config.h"
#include "nvs_flash.h"
#include "nvs.h"

#include "lwip/sockets.h"
#include "lwip/netdb.h"
#include "lwip/inet.h"

#include "esp_console.h"
#include "argtable3/argtable3.h"

static const char *TAG = "rproxy";

/* Embedded broker CA (PEM). */
extern const uint8_t broker_ca_pem_start[] asm("_binary_broker_ca_pem_start");
extern const uint8_t broker_ca_pem_end[]   asm("_binary_broker_ca_pem_end");

#define NVS_NS            "rproxy"
#define CONNECT_TIMEOUT_S 10
#define DIAL_TIMEOUT_S    10
#define BACKOFF_MIN_MS    1000
#define BACKOFF_MAX_MS    20000

/* ---- link abstraction (plain TCP or TLS) ------------------------------- */

typedef struct {
    bool          tls;
    int           fd;         /* raw socket (for select); -1 if none          */
    esp_tls_t    *t;          /* esp-tls handle when tls                      */
} link_t;

static int link_fd(link_t *l) { return l->fd; }

static int link_read(link_t *l, void *buf, size_t len)
{
    if (l->tls) {
        int r = esp_tls_conn_read(l->t, buf, len);
        if (r == ESP_TLS_ERR_SSL_WANT_READ || r == ESP_TLS_ERR_SSL_WANT_WRITE) return -2; /* retry */
        return r;
    }
    return recv(l->fd, buf, len, 0);
}

static bool link_write_all(link_t *l, const void *data, int len)
{
    int off = 0;
    const char *p = data;
    while (off < len) {
        int w;
        if (l->tls) {
            w = esp_tls_conn_write(l->t, p + off, len - off);
            if (w == ESP_TLS_ERR_SSL_WANT_READ || w == ESP_TLS_ERR_SSL_WANT_WRITE) { vTaskDelay(1); continue; }
        } else {
            w = send(l->fd, p + off, len - off, 0);
        }
        if (w <= 0) return false;
        off += w;
    }
    return true;
}

static bool link_has_buffered(link_t *l)
{
    return l->tls && esp_tls_get_bytes_avail(l->t) > 0;
}

static void link_close(link_t *l)
{
    if (l->tls && l->t) { esp_tls_conn_destroy(l->t); l->t = NULL; }
    else if (l->fd >= 0) { close(l->fd); }
    l->fd = -1;
}

static bool link_connect(link_t *l, const char *host, uint16_t port, bool use_tls)
{
    memset(l, 0, sizeof(*l));
    l->fd = -1;
    if (use_tls) {
        esp_tls_cfg_t cfg = {
            .cacert_buf   = broker_ca_pem_start,
            .cacert_bytes = (unsigned)(broker_ca_pem_end - broker_ca_pem_start),
            .skip_common_name = true,   /* private CA issues only the broker cert */
            .timeout_ms   = CONNECT_TIMEOUT_S * 1000,
        };
        l->t = esp_tls_init();
        if (!l->t) return false;
        if (esp_tls_conn_new_sync(host, strlen(host), port, &cfg, l->t) != 1) {
            esp_tls_conn_destroy(l->t); l->t = NULL;
            return false;
        }
        if (esp_tls_get_conn_sockfd(l->t, &l->fd) != ESP_OK) l->fd = -1;
        l->tls = true;
        return true;
    }
    /* plain TCP */
    struct addrinfo hints = { .ai_family = AF_INET, .ai_socktype = SOCK_STREAM };
    struct addrinfo *res = NULL;
    char ps[6]; snprintf(ps, sizeof(ps), "%u", (unsigned)port);
    if (getaddrinfo(host, ps, &hints, &res) != 0 || !res) return false;
    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd >= 0) {
        struct timeval tv = { .tv_sec = CONNECT_TIMEOUT_S, .tv_usec = 0 };
        setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));
        setsockopt(fd, SOL_SOCKET, SO_SNDTIMEO, &tv, sizeof(tv));
        if (connect(fd, res->ai_addr, res->ai_addrlen) != 0) { close(fd); fd = -1; }
    }
    freeaddrinfo(res);
    if (fd < 0) return false;
    l->fd = fd;
    return true;
}

/* Read one '\n'-terminated line from a link (CR stripped). -1 on error. */
static int link_read_line(link_t *l, char *buf, int cap)
{
    int n = 0;
    while (n < cap - 1) {
        char c;
        int r = link_read(l, &c, 1);
        if (r == -2) { vTaskDelay(1); continue; }
        if (r <= 0) return -1;
        if (c == '\n') break;
        if (c != '\r') buf[n++] = c;
    }
    buf[n] = 0;
    return n;
}

/* ---- state ------------------------------------------------------------- */

typedef enum { W_IDLE_OFF = 0, W_CONNECTING, W_READY, W_DIALING } worker_state_t;

static rproxy_config_t s_cfg;
static SemaphoreHandle_t s_lock;
static volatile bool s_run;
static volatile worker_state_t s_wstate[RPROXY_MAX_POOL];
static TaskHandle_t s_wtask[RPROXY_MAX_POOL];
static volatile bool s_rejected;        /* broker refused the token / blocked us */

static uint32_t s_total_dials, s_failed_dials, s_blocked_dials;
static uint64_t s_bytes_up, s_bytes_down;

static char s_mac[18];                  /* "aa:bb:cc:dd:ee:ff"                   */
static char s_fw[24];

/* ---- config persistence ------------------------------------------------ */

static void cfg_defaults(rproxy_config_t *c)
{
    memset(c, 0, sizeof(*c));
    c->enabled = false;
    c->use_tls = true;
    c->port    = RPROXY_DEFAULT_PORT;
    c->pool    = RPROXY_DEFAULT_POOL;
}

/* Apply baked-in provisioning defaults from Kconfig. */
static void cfg_from_build(rproxy_config_t *c)
{
    cfg_defaults(c);
#ifdef CONFIG_RPROXY_DEFAULT_HOST
    strlcpy(c->host, CONFIG_RPROXY_DEFAULT_HOST, sizeof(c->host));
#endif
#ifdef CONFIG_RPROXY_DEFAULT_TOKEN
    strlcpy(c->token, CONFIG_RPROXY_DEFAULT_TOKEN, sizeof(c->token));
#endif
#ifdef CONFIG_RPROXY_DEFAULT_PORT
    c->port = CONFIG_RPROXY_DEFAULT_PORT;
#endif
#ifdef CONFIG_RPROXY_DEFAULT_POOL
    c->pool = CONFIG_RPROXY_DEFAULT_POOL;
#endif
#ifdef CONFIG_RPROXY_DEFAULT_TLS
    c->use_tls = true;
#else
    c->use_tls = false;
#endif
#ifdef CONFIG_RPROXY_DEFAULT_ENABLED
    c->enabled = (c->host[0] && c->token[0]);
#endif
}

static void cfg_save(const rproxy_config_t *c);

static void cfg_load(rproxy_config_t *c)
{
    nvs_handle_t h;
    bool have_nvs = false;
    cfg_from_build(c);     /* start from provisioning defaults                 */

    if (nvs_open(NVS_NS, NVS_READONLY, &h) == ESP_OK) {
        size_t n; uint8_t b; uint16_t port;
        /* "prov" marks that NVS has been written at least once. */
        if (nvs_get_u8(h, "prov", &b) == ESP_OK) have_nvs = true;
        if (have_nvs) {
            n = sizeof(c->host);  if (nvs_get_str(h, "host", c->host, &n) != ESP_OK) {}
            n = sizeof(c->token); if (nvs_get_str(h, "token", c->token, &n) != ESP_OK) {}
            if (nvs_get_u16(h, "port", &port) == ESP_OK) c->port = port;
            if (nvs_get_u8(h, "pool", &b) == ESP_OK && b >= 1 && b <= RPROXY_MAX_POOL) c->pool = b;
            if (nvs_get_u8(h, "enabled", &b) == ESP_OK) c->enabled = b;
            if (nvs_get_u8(h, "tls", &b) == ESP_OK) c->use_tls = b;
        }
        nvs_close(h);
    }
    if (!have_nvs) {
        /* First boot: persist the provisioning defaults so later edits stick. */
        cfg_save(c);
    }
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
    nvs_set_u8(h, "prov", 1);
    nvs_commit(h);
    nvs_close(h);
}

/* ---- safety filter ----------------------------------------------------- */

static bool ipv4_is_blocked(uint32_t host_order)
{
    uint8_t a = (host_order >> 24) & 0xff;
    uint8_t b = (host_order >> 16) & 0xff;
    if (a == 0)                       return true;
    if (a == 10)                      return true;
    if (a == 127)                     return true;
    if (a == 169 && b == 254)         return true;
    if (a == 172 && (b & 0xf0) == 16) return true;
    if (a == 192 && b == 168)         return true;
    if (a == 100 && (b & 0xc0) == 64) return true;
    if (a == 192 && b == 0 && ((host_order >> 8) & 0xff) == 0) return true;
    if (a >= 224)                     return true;
    return false;
}

static bool port_is_blocked(uint16_t port)
{
    return (port == 0 || port == 25 || port == 465 || port == 587);
}

/* ---- helpers ----------------------------------------------------------- */

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

/* Resolve+connect to host:port (plain TCP out to the Internet, via STA). */
static int dial_target(const char *host, uint16_t port)
{
    if (port_is_blocked(port)) return -2;
    struct addrinfo hints = { .ai_family = AF_INET, .ai_socktype = SOCK_STREAM };
    struct addrinfo *res = NULL;
    char portstr[6]; snprintf(portstr, sizeof(portstr), "%u", (unsigned)port);
    if (getaddrinfo(host, portstr, &hints, &res) != 0 || !res) return -1;

    int fd = -1;
    for (struct addrinfo *ai = res; ai; ai = ai->ai_next) {
        if (ai->ai_family != AF_INET) continue;
        struct sockaddr_in *sa = (struct sockaddr_in *)ai->ai_addr;
        if (ipv4_is_blocked(ntohl(sa->sin_addr.s_addr))) { fd = -2; break; }
        fd = socket(AF_INET, SOCK_STREAM, 0);
        if (fd < 0) { fd = -1; continue; }
        set_timeout(fd, CONNECT_TIMEOUT_S);
        if (connect(fd, ai->ai_addr, ai->ai_addrlen) == 0) break;
        close(fd); fd = -1;
    }
    freeaddrinfo(res);
    return fd;
}

static bool sock_write_all(int fd, const char *data, int len)
{
    int off = 0;
    while (off < len) {
        int w = send(fd, data + off, len - off, 0);
        if (w <= 0) return false;
        off += w;
    }
    return true;
}

/* Bidirectional relay: pool link (TLS/TCP) <-> target socket. */
static void relay(link_t *pool, int tgt, char *buf_a, char *buf_b)
{
    int pfd = link_fd(pool);
    struct timeval z = { 0, 0 };
    setsockopt(tgt, SOL_SOCKET, SO_RCVTIMEO, &z, sizeof(z));
    int maxfd = (pfd > tgt ? pfd : tgt) + 1;
    uint64_t up = 0, down = 0;

    while (s_run) {
        bool pool_ready = link_has_buffered(pool);
        if (!pool_ready) {
            fd_set rs; FD_ZERO(&rs);
            FD_SET(pfd, &rs); FD_SET(tgt, &rs);
            struct timeval sel = { .tv_sec = 30, .tv_usec = 0 };
            int r = select(maxfd, &rs, NULL, NULL, &sel);
            if (r < 0) break;
            if (r == 0) continue;
            pool_ready = FD_ISSET(pfd, &rs);
            if (FD_ISSET(tgt, &rs)) {
                int n = recv(tgt, buf_b, RPROXY_RELAY_BUF, 0);
                if (n <= 0) break;
                if (!link_write_all(pool, buf_b, n)) break;
                down += n;
            }
        }
        if (pool_ready) {
            int n = link_read(pool, buf_a, RPROXY_RELAY_BUF);
            if (n == -2) continue;
            if (n <= 0) break;
            if (!sock_write_all(tgt, buf_a, n)) break;
            up += n;
        }
    }
    xSemaphoreTake(s_lock, portMAX_DELAY);
    s_bytes_up += up; s_bytes_down += down;
    xSemaphoreGive(s_lock);
}

static void start_ota(const char *url);

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
        ESP_LOGE(TAG, "w%d: no memory", id);
        free(buf_a); free(buf_b);
        s_wstate[id] = W_IDLE_OFF; s_wtask[id] = NULL; vTaskDelete(NULL); return;
    }

    while (s_run) {
        if (!sta_has_ip()) { s_wstate[id] = W_CONNECTING; vTaskDelay(pdMS_TO_TICKS(1000)); continue; }

        xSemaphoreTake(s_lock, portMAX_DELAY);
        char host[64]; strlcpy(host, s_cfg.host, sizeof(host));
        char token[64]; strlcpy(token, s_cfg.token, sizeof(token));
        uint16_t port = s_cfg.port;
        bool tls = s_cfg.use_tls;
        xSemaphoreGive(s_lock);

        if (host[0] == 0 || token[0] == 0) { vTaskDelay(pdMS_TO_TICKS(2000)); continue; }

        s_wstate[id] = W_CONNECTING;
        link_t link;
        if (!link_connect(&link, host, port, tls)) { backoff_delay(&backoff); continue; }

        /* HELLO <token> <mac> <fw> */
        char line[320];
        int hlen = snprintf(line, sizeof(line), "HELLO %s %s %s\n", token, s_mac, s_fw);
        if (!link_write_all(&link, line, hlen)) { link_close(&link); backoff_delay(&backoff); continue; }
        if (link_read_line(&link, line, sizeof(line)) < 0 || strncmp(line, "READY", 5) != 0) {
            if (strncmp(line, "DENY", 4) == 0) {
                s_rejected = true;
                ESP_LOGW(TAG, "w%d: broker rejected this device (%s)", id, line);
            }
            link_close(&link); backoff_delay(&backoff); continue;
        }
        s_rejected = false;
        backoff = BACKOFF_MIN_MS;
        s_wstate[id] = W_READY;

        /* Wait (idle) for one DIAL line. */
        int ll = link_read_line(&link, line, sizeof(line));
        if (ll < 0) { link_close(&link); continue; }

        if (strncmp(line, "OTA ", 4) == 0) {
            link_write_all(&link, "OK\n", 3);
            link_close(&link);
            start_ota(line + 4);
            vTaskDelay(pdMS_TO_TICKS(2000));
            continue;
        }

        char dhost[200]; unsigned dport = 0;
        if (sscanf(line, "DIAL %199s %u", dhost, &dport) != 2 || dport == 0 || dport > 65535) {
            link_write_all(&link, "ERR badreq\n", 11);
            link_close(&link); continue;
        }

        s_wstate[id] = W_DIALING;
        int tfd = dial_target(dhost, (uint16_t)dport);
        if (tfd == -2) {
            link_write_all(&link, "ERR blocked\n", 12);
            xSemaphoreTake(s_lock, portMAX_DELAY); s_blocked_dials++; xSemaphoreGive(s_lock);
            ESP_LOGW(TAG, "w%d: blocked dial to %s:%u", id, dhost, dport);
            link_close(&link); continue;
        }
        if (tfd < 0) {
            link_write_all(&link, "ERR dial\n", 9);
            xSemaphoreTake(s_lock, portMAX_DELAY); s_failed_dials++; xSemaphoreGive(s_lock);
            link_close(&link); continue;
        }
        if (!link_write_all(&link, "OK\n", 3)) { close(tfd); link_close(&link); continue; }

        xSemaphoreTake(s_lock, portMAX_DELAY); s_total_dials++; xSemaphoreGive(s_lock);
        ESP_LOGI(TAG, "w%d: dial %s:%u", id, dhost, dport);

        relay(&link, tfd, buf_a, buf_b);
        close(tfd);
        link_close(&link);
    }

    free(buf_a); free(buf_b);
    s_wstate[id] = W_IDLE_OFF; s_wtask[id] = NULL;
    vTaskDelete(NULL);
}

/* ---- OTA pulled on command from the broker ------------------------------ */

static volatile bool s_ota_busy;

static void ota_task(void *arg)
{
    char *url = (char *)arg;
    ESP_LOGW(TAG, "OTA: downloading %s", url);
    esp_http_client_config_t http = {
        .url = url,
        .timeout_ms = 30000,
        .crt_bundle_attach = esp_crt_bundle_attach,   /* public CAs for https URLs */
        .keep_alive_enable = true,
    };
    esp_https_ota_config_t ota = { .http_config = &http };
    esp_err_t err = esp_https_ota(&ota);
    if (err == ESP_OK) {
        ESP_LOGW(TAG, "OTA: success, rebooting");
        vTaskDelay(pdMS_TO_TICKS(500));
        esp_restart();
    }
    ESP_LOGE(TAG, "OTA: failed: %s", esp_err_to_name(err));
    free(url);
    s_ota_busy = false;
    vTaskDelete(NULL);
}

static void start_ota(const char *url)
{
    if (s_ota_busy) return;
    s_ota_busy = true;
    char *copy = strdup(url);
    if (!copy || xTaskCreate(ota_task, "rproxy_ota", 8192, copy, tskIDLE_PRIORITY + 4, NULL) != pdPASS) {
        free(copy); s_ota_busy = false;
    }
}

/* ---- AP policy: hide the setup AP while the uplink works ---------------- */
#if CONFIG_RPROXY_AP_AUTO_OFF
static void ap_policy_task(void *arg)
{
    int down_secs = 0;
    while (1) {
        vTaskDelay(pdMS_TO_TICKS(5000));
        if (sta_has_ip()) {
            down_secs = 0;
            if (!ap_disabled) { ESP_LOGI(TAG, "uplink up: turning setup AP off"); ap_set_enabled(false); }
        } else {
            down_secs += 5;
            if (ap_disabled && down_secs >= CONFIG_RPROXY_AP_AUTO_OFF_GRACE) {
                ESP_LOGW(TAG, "uplink down %ds: turning setup AP back on", down_secs);
                ap_set_enabled(true);
            }
        }
    }
}
#endif

/* ---- lifecycle --------------------------------------------------------- */

static void start_pool(void)
{
    if (s_run) return;
    s_run = true;
    s_rejected = false;
    uint8_t n = s_cfg.pool;
    if (n < 1) n = 1;
    if (n > RPROXY_MAX_POOL) n = RPROXY_MAX_POOL;
    for (int i = 0; i < n; i++) {
        if (s_wtask[i]) continue;
        s_wstate[i] = W_CONNECTING;
        xTaskCreate(worker_task, "rproxy_w", 7168, (void *)(intptr_t)i,
                    tskIDLE_PRIORITY + 3, &s_wtask[i]);
    }
    ESP_LOGI(TAG, "pool started: %u workers -> %s:%u (tls=%d)", n, s_cfg.host, s_cfg.port, s_cfg.use_tls);
}

static void stop_pool(void)
{
    if (!s_run) return;
    s_run = false;
    ESP_LOGI(TAG, "pool stopping");
}

esp_err_t rproxy_init(void)
{
    s_lock = xSemaphoreCreateMutex();
    if (!s_lock) return ESP_ERR_NO_MEM;

    uint8_t mac[6] = {0};
    esp_read_mac(mac, ESP_MAC_WIFI_STA);
    snprintf(s_mac, sizeof(s_mac), "%02x:%02x:%02x:%02x:%02x:%02x",
             mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]);
    const esp_app_desc_t *d = esp_app_get_description();
    strlcpy(s_fw, d ? d->version : "unknown", sizeof(s_fw));

    cfg_load(&s_cfg);
    ESP_LOGI(TAG, "init: enabled=%d host=%s:%u pool=%u tls=%d mac=%s fw=%s",
             s_cfg.enabled, s_cfg.host, s_cfg.port, s_cfg.pool, s_cfg.use_tls, s_mac, s_fw);
    if (s_cfg.enabled && s_cfg.host[0] && s_cfg.token[0]) start_pool();
#if CONFIG_RPROXY_AP_AUTO_OFF
    xTaskCreate(ap_policy_task, "ap_policy", 3072, NULL, tskIDLE_PRIORITY + 1, NULL);
#endif
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
    strlcpy(s_cfg.host, host, sizeof(s_cfg.host)); s_cfg.port = port; cfg_save(&s_cfg);
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
    return ESP_OK;
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

rproxy_link_state_t rproxy_link_state(void)
{
    if (!s_run) return RPROXY_LINK_DISABLED;
    if (!sta_has_ip()) return RPROXY_LINK_NO_UPLINK;
    if (s_rejected) return RPROXY_LINK_REJECTED;
    for (int i = 0; i < RPROXY_MAX_POOL; i++)
        if (s_wstate[i] == W_READY || s_wstate[i] == W_DIALING) return RPROXY_LINK_ONLINE;
    return RPROXY_LINK_CONNECTING;
}

const char *rproxy_link_state_str(rproxy_link_state_t s)
{
    switch (s) {
        case RPROXY_LINK_DISABLED:  return "disabled";
        case RPROXY_LINK_NO_UPLINK: return "waiting for WiFi";
        case RPROXY_LINK_CONNECTING:return "connecting";
        case RPROXY_LINK_ONLINE:    return "online";
        case RPROXY_LINK_REJECTED:  return "error (rejected)";
    }
    return "unknown";
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
        printf("rproxy: mac=%s fw=%s\n", s_mac, s_fw);
        printf("  enabled:  %s\n", c.enabled ? "yes" : "no");
        printf("  broker:   %s:%u\n", c.host[0] ? c.host : "(unset)", c.port);
        printf("  token:    %s\n", c.token[0] ? "(set)" : "(unset)");
        printf("  pool:     %u\n", c.pool);
        printf("  tls:      %s\n", c.use_tls ? "on (CA pinned)" : "off");
        printf("  state:    %s\n", rproxy_link_state_str(rproxy_link_state()));
        printf("  workers:  %u connected, %u dialing\n", s.connected, s.dialing);
        printf("  dials:    %lu ok, %lu failed, %lu blocked\n",
               (unsigned long)s.total_dials, (unsigned long)s.failed_dials, (unsigned long)s.blocked_dials);
        printf("  traffic:  up %llu B, down %llu B\n",
               (unsigned long long)s.bytes_up, (unsigned long long)s.bytes_down);
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
        printf("TLS %s. Re-enable to apply.\n", on ? "on (CA pinned)" : "off");
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
