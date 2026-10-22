/* Link write and relay watchdog policy, independent of IDF for host testing. */
#ifndef RPROXY_LINK_GUARD_H
#define RPROXY_LINK_GUARD_H

#include <stdbool.h>
#include <stdint.h>

typedef struct {
    void *ctx;
    int (*write)(void *, const void *, int); /* bytes accepted, `retry`, or <= 0 on error */
    int64_t (*now_us)(void *);
    void (*yield)(void *);
    int retry;
} rproxy_write_ops_t;

/* Write all of data, retrying while the transport asks to. A link whose send
 * buffer stops draining fails once no byte has moved for stall_us, so the
 * caller reconnects instead of waiting on a dead peer forever. */
static inline bool rproxy_write_all(const rproxy_write_ops_t *ops, const void *data, int len, int64_t stall_us)
{
    const char *p = data;
    int off = 0;
    int64_t progress = ops->now_us(ops->ctx);
    while (off < len) {
        int w = ops->write(ops->ctx, p + off, len - off);
        if (w == ops->retry) {
            if (ops->now_us(ops->ctx) - progress >= stall_us) return false;
            ops->yield(ops->ctx);
            continue;
        }
        if (w <= 0) return false;
        off += w;
        progress = ops->now_us(ops->ctx);
    }
    return true;
}

/* The relay loop beats once per pass. It is stuck when it has started, is not
 * paused for an OTA, and has not beaten for longer than limit_us. */
static inline bool rproxy_relay_stuck(int64_t now_us, int64_t last_beat_us, int64_t limit_us, bool paused)
{
    return !paused && last_beat_us > 0 && now_us - last_beat_us > limit_us;
}

#endif
