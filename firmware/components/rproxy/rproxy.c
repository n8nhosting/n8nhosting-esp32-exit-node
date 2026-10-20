/*
 * rproxy - Reverse exit-node dialer for ESP32 NAT Router.
 *
 * The device keeps ONE link to the broker and multiplexes every customer
 * stream over it (see "multiplexed link" below). The link can run over TLS
 * (esp-tls), with the broker certificate verified against a CA embedded in
 * the firmware (components/rproxy/broker_ca.pem). The device identifies
 * itself in the handshake by its STA MAC so it can be blocked server-side:
 *
 *     HELLO <token> <mac> <fw> <uptime> <ssid_b64> mux1 <max>\n
 *         ->   READY mux1\n          (accepted, binary frames follow)
 *         ->   DENY ...\n or close   (rejected)
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
#include <fcntl.h>

#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/semphr.h"
#include "freertos/queue.h"

#include "esp_log.h"
#include "esp_netif.h"
#include "esp_heap_caps.h"
#include "esp_mac.h"
#include "esp_wifi.h"
#include "esp_timer.h"
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

/* minimal base64 (no padding issues: standard with padding) */
static void b64encode(const uint8_t *in, int len, char *out, int outcap)
{
    static const char tbl[] = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    int o = 0;
    for (int i = 0; i < len; i += 3) {
        int n = in[i] << 16;
        if (i + 1 < len) n |= in[i+1] << 8;
        if (i + 2 < len) n |= in[i+2];
        if (o + 4 >= outcap) break;
        out[o++] = tbl[(n >> 18) & 63];
        out[o++] = tbl[(n >> 12) & 63];
        out[o++] = (i + 1 < len) ? tbl[(n >> 6) & 63] : '=';
        out[o++] = (i + 2 < len) ? tbl[n & 63] : '=';
    }
    out[o] = 0;
}

/* Embedded broker CA (PEM). */
extern const uint8_t broker_ca_pem_start[] asm("_binary_broker_ca_pem_start");
extern const uint8_t broker_ca_pem_end[]   asm("_binary_broker_ca_pem_end");

#define NVS_NS            "rproxy"
#define RPROXY_PROV_GEN   1   /* bump to force re-provision over stale NVS */
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

static rproxy_config_t s_cfg;
static SemaphoreHandle_t s_lock;
static volatile bool s_run;
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
        size_t n; uint8_t b; uint16_t port; uint32_t gen = 0;
        /* "prov" marks that NVS has been written at least once. Re-provision
         * (ignore stale NVS) when the build's provisioning generation differs
         * from what is stored, so an OTA/flash that changes the baked-in
         * defaults takes effect without a manual erase. */
        if (nvs_get_u8(h, "prov", &b) == ESP_OK) have_nvs = true;
        if (nvs_get_u32(h, "prov_gen", &gen) != ESP_OK) gen = 0;
        if (have_nvs && gen != RPROXY_PROV_GEN) {
            have_nvs = false;   /* stale generation: fall back to build defaults */
            ESP_LOGW(TAG, "provisioning generation changed (%u->%u); re-applying build defaults",
                     (unsigned)gen, (unsigned)RPROXY_PROV_GEN);
        }
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
    nvs_set_u32(h, "prov_gen", RPROXY_PROV_GEN);
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

static void start_ota(const char *url);

/* ---- multiplexed link ("mux1") ------------------------------------------
 *
 * One long-lived link to the broker carries many customer streams as binary
 * frames:  type u8 | flags u8 | len u16 BE | sid u32 BE | payload[len]
 *
 *   OPEN "host port"   broker -> node   dial a target for stream <sid>
 *   OPEN_OK / OPEN_ERR node -> broker   dial result ("blocked"/"dial"/"busy")
 *   DATA               both ways        at most MUX_WINDOW bytes in flight
 *   WINDOW u32         both ways        credit returned after bytes are written
 *   FIN / RST          both ways        half-close / abort or forget
 *   PING / PONG, OTA   sid 0            keepalive, firmware update
 *
 * A single task owns the link and every target socket (select loop, target
 * sockets non-blocking), so the TLS session is only touched from one task.
 * Slow DNS/connect work runs in a few dialer tasks that hand finished
 * sockets back through a queue.
 */

#define MUX_HDR          8
#define MUX_MAX_PAYLOAD  16384
#define MUX_WINDOW       32768
#define MUX_GRANT_AT     8192
#define MUX_DEAD_MS      45000
#define MUX_DIALERS      4
#define MUX_RXCAP        (MUX_HDR + MUX_MAX_PAYLOAD + 2048)

enum {
    F_OPEN = 0x01, F_OPEN_OK = 0x02, F_OPEN_ERR = 0x03, F_DATA = 0x04,
    F_FIN = 0x05, F_RST = 0x06, F_WINDOW = 0x07, F_PING = 0x08,
    F_PONG = 0x09, F_OTA = 0x0A,
};

typedef enum { ST_FREE = 0, ST_DIALING, ST_OPEN } sstate_t;

typedef struct {
    sstate_t state;
    uint32_t sid;
    int      fd;
    uint8_t *tx;            /* broker -> target bytes not yet sent (MUX_WINDOW) */
    int      tx_off, tx_len;
    uint32_t credit;        /* bytes we may still send to the broker            */
    uint32_t consumed;      /* written to target, not yet granted back          */
    bool     rx_fin;        /* broker sent FIN                                  */
    bool     shut_wr;       /* shutdown(SHUT_WR) done on target                 */
    bool     eof_sent;      /* target EOF forwarded as FIN                      */
} mstream_t;

typedef struct { uint32_t epoch, sid; uint16_t port; char host[200]; } dial_job_t;
typedef struct { uint32_t epoch, sid; int fd; } dial_res_t;   /* fd -1 dial, -2 blocked */

static mstream_t      s_st[RPROXY_MAX_STREAMS];
static QueueHandle_t  s_dial_q, s_res_q;
static volatile uint32_t s_epoch;
static volatile bool  s_link_up;
static volatile int   s_active;            /* streams dialing or open         */
static TaskHandle_t   s_mux_task;
static uint8_t       *s_fb;                /* frame scratch (mux task only)   */

static bool frame_send(link_t *l, uint8_t typ, uint32_t sid, const void *p, uint16_t len)
{
    uint8_t *f = s_fb;
    f[0] = typ; f[1] = 0;
    f[2] = len >> 8; f[3] = len & 0xff;
    f[4] = sid >> 24; f[5] = sid >> 16; f[6] = sid >> 8; f[7] = sid;
    if (len && p != f + MUX_HDR) memcpy(f + MUX_HDR, p, len);
    return link_write_all(l, f, MUX_HDR + len);
}

static mstream_t *st_find(uint32_t sid)
{
    for (int i = 0; i < RPROXY_MAX_STREAMS; i++)
        if (s_st[i].state != ST_FREE && s_st[i].sid == sid) return &s_st[i];
    return NULL;
}

static void st_free(mstream_t *st)
{
    if (st->fd >= 0) close(st->fd);
    if (st->tx) free(st->tx);
    memset(st, 0, sizeof(*st));
    st->fd = -1;
    s_active--;
}

static void dialer_task(void *arg)
{
    dial_job_t job;
    while (1) {
        if (xQueueReceive(s_dial_q, &job, portMAX_DELAY) != pdTRUE) continue;
        if (job.epoch != s_epoch) continue;            /* link already gone */
        dial_res_t r = { .epoch = job.epoch, .sid = job.sid };
        r.fd = dial_target(job.host, job.port);
        if (r.fd >= 0) ESP_LOGI(TAG, "dial %s:%u", job.host, job.port);
        else if (r.fd == -2) ESP_LOGW(TAG, "blocked dial to %s:%u", job.host, job.port);
        if (job.epoch != s_epoch) { if (r.fd >= 0) close(r.fd); continue; }
        xQueueSend(s_res_q, &r, portMAX_DELAY);
    }
}

/* Flush pending broker->target bytes; grant credit back as they drain. */
static bool st_flush(link_t *l, mstream_t *st)
{
    while (st->tx_len > 0) {
        int w = send(st->fd, st->tx + st->tx_off, st->tx_len, MSG_DONTWAIT);
        if (w > 0) {
            st->tx_off += w; st->tx_len -= w; st->consumed += w;
            xSemaphoreTake(s_lock, portMAX_DELAY); s_bytes_up += w; xSemaphoreGive(s_lock);
            continue;
        }
        if (w < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) break;
        frame_send(l, F_RST, st->sid, NULL, 0);
        st_free(st);
        return true;
    }
    if (st->tx_len == 0) st->tx_off = 0;
    if (st->consumed >= MUX_GRANT_AT) {
        uint8_t g[4] = { st->consumed >> 24, st->consumed >> 16, st->consumed >> 8, st->consumed };
        st->consumed = 0;
        if (!frame_send(l, F_WINDOW, st->sid, g, 4)) return false;
    }
    if (st->rx_fin && st->tx_len == 0 && !st->shut_wr) {
        shutdown(st->fd, SHUT_WR);
        st->shut_wr = true;
    }
    return true;
}

/* Forward target->broker bytes while we hold credit. */
static bool st_pump(link_t *l, mstream_t *st)
{
    uint32_t n = st->credit < RPROXY_RELAY_BUF ? st->credit : RPROXY_RELAY_BUF;
    if (n == 0 || st->eof_sent) return true;
    int r = recv(st->fd, s_fb + MUX_HDR, n, MSG_DONTWAIT);
    if (r > 0) {
        st->credit -= r;
        xSemaphoreTake(s_lock, portMAX_DELAY); s_bytes_down += r; xSemaphoreGive(s_lock);
        return frame_send(l, F_DATA, st->sid, s_fb + MUX_HDR, r);
    }
    if (r == 0) {
        st->eof_sent = true;
        return frame_send(l, F_FIN, st->sid, NULL, 0);
    }
    if (errno == EAGAIN || errno == EWOULDBLOCK) return true;
    bool ok = frame_send(l, F_RST, st->sid, NULL, 0);
    st_free(st);
    return ok;
}

static void handle_dial_result(link_t *l, const dial_res_t *r)
{
    mstream_t *st = (r->epoch == s_epoch) ? st_find(r->sid) : NULL;
    if (!st || st->state != ST_DIALING) {            /* reset while dialing */
        if (r->fd >= 0) close(r->fd);
        return;
    }
    if (r->fd < 0) {
        const char *why = r->fd == -2 ? "blocked" : "dial";
        frame_send(l, F_OPEN_ERR, st->sid, why, strlen(why));
        xSemaphoreTake(s_lock, portMAX_DELAY);
        if (r->fd == -2) s_blocked_dials++; else s_failed_dials++;
        xSemaphoreGive(s_lock);
        st_free(st);
        return;
    }
    int fl = fcntl(r->fd, F_GETFL, 0);
    fcntl(r->fd, F_SETFL, fl | O_NONBLOCK);
    st->fd = r->fd;
    st->state = ST_OPEN;
    xSemaphoreTake(s_lock, portMAX_DELAY); s_total_dials++; xSemaphoreGive(s_lock);
    frame_send(l, F_OPEN_OK, st->sid, NULL, 0);
}

static void handle_open(link_t *l, uint32_t sid, const uint8_t *p, int len)
{
    char req[220], host[200];
    unsigned port = 0;
    const char *err = NULL;
    if (st_find(sid)) return;
    if (len >= (int)sizeof(req)) len = sizeof(req) - 1;
    memcpy(req, p, len); req[len] = 0;
    if (sscanf(req, "%199s %u", host, &port) != 2 || port == 0 || port > 65535) err = "badreq";
    else if (port_is_blocked((uint16_t)port)) err = "blocked";

    mstream_t *st = NULL;
    if (!err) {
        for (int i = 0; i < RPROXY_MAX_STREAMS; i++)
            if (s_st[i].state == ST_FREE) { st = &s_st[i]; break; }
        if (!st) err = "busy";
    }
    if (!err) {
        dial_job_t job = { .epoch = s_epoch, .sid = sid, .port = (uint16_t)port };
        strlcpy(job.host, host, sizeof(job.host));
        if (xQueueSend(s_dial_q, &job, 0) != pdTRUE) err = "busy";
    }
    if (err) {
        if (strcmp(err, "blocked") == 0) {
            xSemaphoreTake(s_lock, portMAX_DELAY); s_blocked_dials++; xSemaphoreGive(s_lock);
        }
        frame_send(l, F_OPEN_ERR, sid, err, strlen(err));
        return;
    }
    memset(st, 0, sizeof(*st));
    st->state = ST_DIALING;
    st->sid = sid;
    st->fd = -1;
    st->credit = MUX_WINDOW;
    s_active++;
}

/* Returns false when the link must be dropped. */
static bool handle_frame(link_t *l, uint8_t typ, uint32_t sid, const uint8_t *p, int len)
{
    if (sid == 0) {
        if (typ == F_PING) return frame_send(l, F_PONG, 0, p, len);
        if (typ == F_OTA && len > 0 && len < 256) {
            char url[256];
            memcpy(url, p, len); url[len] = 0;
            ESP_LOGW(TAG, "OTA requested by broker");
            start_ota(url);
        }
        return true;
    }
    if (typ == F_OPEN) { handle_open(l, sid, p, len); return true; }

    mstream_t *st = st_find(sid);
    if (!st) return true;                              /* already forgotten */
    switch (typ) {
    case F_DATA:
        if (st->state != ST_OPEN) return true;
        if (!st->tx) {
            st->tx = heap_caps_malloc(MUX_WINDOW, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
            if (!st->tx) st->tx = malloc(MUX_WINDOW);
            if (!st->tx) { frame_send(l, F_RST, sid, NULL, 0); st_free(st); return true; }
        }
        if (len > MUX_WINDOW - st->tx_len) {           /* broker ignored our window */
            ESP_LOGW(TAG, "stream %lu overran window", (unsigned long)sid);
            frame_send(l, F_RST, sid, NULL, 0);
            st_free(st);
            return true;
        }
        if (st->tx_off + st->tx_len + len > MUX_WINDOW) {
            memmove(st->tx, st->tx + st->tx_off, st->tx_len);
            st->tx_off = 0;
        }
        memcpy(st->tx + st->tx_off + st->tx_len, p, len);
        st->tx_len += len;
        return st_flush(l, st);
    case F_FIN:
        st->rx_fin = true;
        return st->state == ST_OPEN ? st_flush(l, st) : true;
    case F_RST:
        st_free(st);
        return true;
    case F_WINDOW:
        if (len == 4) st->credit += ((uint32_t)p[0] << 24) | (p[1] << 16) | (p[2] << 8) | p[3];
        return true;
    }
    return true;
}

static void mux_reset_streams(void)
{
    s_epoch++;
    xQueueReset(s_dial_q);
    for (int i = 0; i < RPROXY_MAX_STREAMS; i++)
        if (s_st[i].state != ST_FREE) st_free(&s_st[i]);
    s_active = 0;
    dial_res_t r;
    while (xQueueReceive(s_res_q, &r, 0) == pdTRUE)
        if (r.fd >= 0) close(r.fd);
}

/* Serve one established link until it fails or rproxy is disabled. */
static void mux_serve(link_t *l, uint8_t *rx)
{
    int rxlen = 0;
    int64_t last_rx = esp_timer_get_time();
    int64_t last_stats = last_rx;
    int lfd = link_fd(l);
    if (lfd < 0) return;

    while (s_run) {
        dial_res_t r;
        while (xQueueReceive(s_res_q, &r, 0) == pdTRUE) handle_dial_result(l, &r);

        fd_set rs, ws;
        FD_ZERO(&rs); FD_ZERO(&ws);
        FD_SET(lfd, &rs);
        int maxfd = lfd, dialing = 0;
        for (int i = 0; i < RPROXY_MAX_STREAMS; i++) {
            mstream_t *st = &s_st[i];
            if (st->state == ST_DIALING) dialing++;
            if (st->state != ST_OPEN) continue;
            if (!st->eof_sent && st->credit > 0) FD_SET(st->fd, &rs);
            if (st->tx_len > 0) FD_SET(st->fd, &ws);
            if (st->fd > maxfd) maxfd = st->fd;
        }
        bool buffered = link_has_buffered(l);
        struct timeval tv = { 0, buffered ? 0 : (dialing ? 20000 : 0) };
        if (!buffered && !dialing) tv.tv_sec = 1;
        int n = select(maxfd + 1, &rs, &ws, NULL, &tv);
        if (n < 0) { ESP_LOGW(TAG, "select: errno %d", errno); return; }

        if (buffered || FD_ISSET(lfd, &rs)) {
            int got = link_read(l, rx + rxlen, MUX_RXCAP - rxlen);
            if (got == 0 || (got < 0 && got != -2)) return;   /* link closed */
            if (got > 0) {
                rxlen += got;
                last_rx = esp_timer_get_time();
                int off = 0;
                while (rxlen - off >= MUX_HDR) {
                    uint8_t *h = rx + off;
                    int plen = (h[2] << 8) | h[3];
                    if (plen > MUX_MAX_PAYLOAD) { ESP_LOGE(TAG, "bad frame length %d", plen); return; }
                    if (rxlen - off < MUX_HDR + plen) break;
                    uint32_t sid = ((uint32_t)h[4] << 24) | (h[5] << 16) | (h[6] << 8) | h[7];
                    if (!handle_frame(l, h[0], sid, h + MUX_HDR, plen)) return;
                    off += MUX_HDR + plen;
                }
                if (off > 0) { memmove(rx, rx + off, rxlen - off); rxlen -= off; }
            }
        }

        for (int i = 0; i < RPROXY_MAX_STREAMS; i++) {
            mstream_t *st = &s_st[i];
            if (st->state != ST_OPEN) continue;
            int fd = st->fd;
            if (FD_ISSET(fd, &ws) && !st_flush(l, st)) return;
            if (st->state == ST_OPEN && st->fd == fd && FD_ISSET(fd, &rs) && !st_pump(l, st)) return;
            if (st->state == ST_OPEN && st->eof_sent && st->rx_fin && st->tx_len == 0)
                st_free(st);                           /* both directions done */
        }

        if ((esp_timer_get_time() - last_rx) / 1000 > MUX_DEAD_MS) {
            ESP_LOGW(TAG, "broker silent for %ds, reconnecting", MUX_DEAD_MS / 1000);
            return;
        }

        /* One stats line a minute: enough to spot a slow leak or heap
         * starvation (the v1.2 pool died with internal RAM exhausted and
         * nothing in the log said so). */
        if (esp_timer_get_time() - last_stats > 60 * 1000000LL) {
            last_stats = esp_timer_get_time();
            ESP_LOGI(TAG, "stats: streams %d/%d (dialing %d), dials ok %lu err %lu, "
                          "heap free %u min %u, internal free %u largest %u",
                     s_active, RPROXY_MAX_STREAMS, dialing,
                     (unsigned long)s_total_dials, (unsigned long)s_failed_dials,
                     (unsigned)esp_get_free_heap_size(), (unsigned)esp_get_minimum_free_heap_size(),
                     (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL),
                     (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL));
        }
    }
}

static void backoff_delay(int *ms)
{
    vTaskDelay(pdMS_TO_TICKS(*ms));
    *ms *= 2;
    if (*ms > BACKOFF_MAX_MS) *ms = BACKOFF_MAX_MS;
}

static void mux_task(void *arg)
{
    int backoff = BACKOFF_MIN_MS;
    uint8_t *rx = heap_caps_malloc(MUX_RXCAP, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
    if (!rx) rx = malloc(MUX_RXCAP);
    if (!rx || !s_fb) {
        ESP_LOGE(TAG, "mux: no memory");
        free(rx);
        s_mux_task = NULL; vTaskDelete(NULL); return;
    }

    while (s_run) {
        if (!sta_has_ip()) { vTaskDelay(pdMS_TO_TICKS(1000)); continue; }

        xSemaphoreTake(s_lock, portMAX_DELAY);
        char host[64]; strlcpy(host, s_cfg.host, sizeof(host));
        char token[64]; strlcpy(token, s_cfg.token, sizeof(token));
        uint16_t port = s_cfg.port;
        bool tls = s_cfg.use_tls;
        xSemaphoreGive(s_lock);

        if (host[0] == 0 || token[0] == 0) { vTaskDelay(pdMS_TO_TICKS(2000)); continue; }

        link_t link;
        if (!link_connect(&link, host, port, tls)) { backoff_delay(&backoff); continue; }

        char line[320];
        uint32_t uptime = (uint32_t)(esp_timer_get_time() / 1000000ULL);
        char ssid_b64[48] = "-";                       /* "-" keeps fields aligned */
        {
            wifi_config_t sta_cfg;
            if (esp_wifi_get_config(WIFI_IF_STA, &sta_cfg) == ESP_OK) {
                int sl = strnlen((char *)sta_cfg.sta.ssid, sizeof(sta_cfg.sta.ssid));
                if (sl > 0) b64encode(sta_cfg.sta.ssid, sl, ssid_b64, sizeof(ssid_b64));
            }
        }
        /* HELLO <token> <mac> <fw> <uptime_s> <ssid_b64> mux1 <max_streams>
         * (the WiFi password is never sent) */
        int hlen = snprintf(line, sizeof(line), "HELLO %s %s %s %lu %s mux1 %d\n",
                            token, s_mac, s_fw, (unsigned long)uptime, ssid_b64, RPROXY_MAX_STREAMS);
        if (!link_write_all(&link, line, hlen) || link_read_line(&link, line, sizeof(line)) < 0) {
            link_close(&link); backoff_delay(&backoff); continue;
        }
        if (strcmp(line, "READY mux1") != 0) {
            if (strncmp(line, "DENY", 4) == 0) {
                s_rejected = true;
                ESP_LOGW(TAG, "broker rejected this device (%s)", line);
            } else if (strcmp(line, "READY") == 0) {
                ESP_LOGE(TAG, "broker does not support the mux link; update the broker");
            }
            link_close(&link); backoff_delay(&backoff); continue;
        }
        s_rejected = false;
        backoff = BACKOFF_MIN_MS;
        s_link_up = true;
        ESP_LOGI(TAG, "mux link up -> %s:%u (tls=%d, max %d streams)", host, port, tls, RPROXY_MAX_STREAMS);

        mux_serve(&link, rx);

        s_link_up = false;
        mux_reset_streams();
        link_close(&link);
        ESP_LOGW(TAG, "mux link down");
        if (s_run) vTaskDelay(pdMS_TO_TICKS(BACKOFF_MIN_MS));
    }

    free(rx);
    s_mux_task = NULL;
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
    if (!s_dial_q) {
        s_dial_q = xQueueCreate(RPROXY_MAX_STREAMS, sizeof(dial_job_t));
        s_res_q  = xQueueCreate(RPROXY_MAX_STREAMS + MUX_DIALERS, sizeof(dial_res_t));
        s_fb = heap_caps_malloc(MUX_HDR + MUX_MAX_PAYLOAD, MALLOC_CAP_SPIRAM | MALLOC_CAP_8BIT);
        if (!s_fb) s_fb = malloc(MUX_HDR + MUX_MAX_PAYLOAD);
        if (!s_dial_q || !s_res_q || !s_fb) { ESP_LOGE(TAG, "mux: no memory"); return; }
        for (int i = 0; i < RPROXY_MAX_STREAMS; i++) s_st[i].fd = -1;
        for (int i = 0; i < MUX_DIALERS; i++)
            xTaskCreate(dialer_task, "rproxy_dial", 4608, NULL, tskIDLE_PRIORITY + 3, NULL);
    }
    s_run = true;
    s_rejected = false;
    if (!s_mux_task)
        xTaskCreate(mux_task, "rproxy_mux", 8192, NULL, tskIDLE_PRIORITY + 3, &s_mux_task);
    ESP_LOGI(TAG, "mux link started -> %s:%u (tls=%d)", s_cfg.host, s_cfg.port, s_cfg.use_tls);
}

static void stop_pool(void)
{
    if (!s_run) return;
    s_run = false;
    ESP_LOGI(TAG, "mux link stopping");
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
    out->max_streams = RPROXY_MAX_STREAMS;
    out->total_dials = s_total_dials;
    out->failed_dials = s_failed_dials;
    out->blocked_dials = s_blocked_dials;
    out->bytes_up = s_bytes_up;
    out->bytes_down = s_bytes_down;
    xSemaphoreGive(s_lock);
    out->connected = s_link_up ? 1 : 0;
    out->dialing = (uint8_t)(s_active > 0 ? s_active : 0);
}

rproxy_link_state_t rproxy_link_state(void)
{
    if (!s_run) return RPROXY_LINK_DISABLED;
    if (!sta_has_ip()) return RPROXY_LINK_NO_UPLINK;
    if (s_rejected) return RPROXY_LINK_REJECTED;
    return s_link_up ? RPROXY_LINK_ONLINE : RPROXY_LINK_CONNECTING;
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
        printf("  tls:      %s\n", c.use_tls ? "on (CA pinned)" : "off");
        printf("  state:    %s\n", rproxy_link_state_str(rproxy_link_state()));
        printf("  link:     %s (mux), %u/%u streams active\n", s.connected ? "up" : "down", s.dialing, s.max_streams);
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
        if (rproxy_set_pool((uint8_t)n) == ESP_OK) printf("Pool set to %d (unused: the mux link carries all streams).\n", n);
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
