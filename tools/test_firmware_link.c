/* Execute the same link write and relay watchdog policy the firmware uses. */
#include <assert.h>
#include <stdio.h>
#include <string.h>
#include "../firmware/components/rproxy/include/link_guard.h"

enum { RETRY = -2 };
typedef struct {
    int64_t now, retry_cost;
    int script[16], steps, calls, yields;   /* per call: bytes accepted, RETRY, or an error */
    char sent[64]; int sent_len;
} mock_t;
static int write_fn(void *ctx, const void *p, int len) {
    mock_t *m = ctx;
    int r = m->calls < m->steps ? m->script[m->calls] : RETRY;
    m->calls++;
    if (r == RETRY) { m->now += m->retry_cost; return RETRY; }
    if (r > len) r = len;
    if (r > 0) { memcpy(m->sent + m->sent_len, p, r); m->sent_len += r; }
    return r;
}
static int64_t now(void *ctx) { return ((mock_t *)ctx)->now; }
static void yield(void *ctx) { ((mock_t *)ctx)->yields++; }
static bool run(mock_t *m, const char *data, int len) {
    const rproxy_write_ops_t ops = { .ctx = m, .write = write_fn, .now_us = now, .yield = yield, .retry = RETRY };
    return rproxy_write_all(&ops, data, len, 30 * 1000000LL);
}
int main(void) {
    /* partial writes and retries still deliver every byte, in order */
    mock_t m = { .retry_cost = 1000, .script = { 3, RETRY, RETRY, 4, 3 }, .steps = 5 };
    assert(run(&m, "0123456789", 10) && m.sent_len == 10 && memcmp(m.sent, "0123456789", 10) == 0 && m.yields == 2);
    /* a transport error fails at once, without waiting for the stall limit */
    m = (mock_t){ .retry_cost = 1000, .script = { 2, -1 }, .steps = 2 };
    assert(!run(&m, "0123456789", 10) && m.calls == 2 && m.yields == 0);
    m = (mock_t){ .script = { 0 }, .steps = 1 };
    assert(!run(&m, "abc", 3) && m.calls == 1);
    /* a send buffer that never drains fails after 30 s instead of retrying forever */
    m = (mock_t){ .retry_cost = 10 * 1000000LL, .script = { 2 }, .steps = 1 };
    assert(!run(&m, "0123456789", 10) && m.sent_len == 2 && m.calls == 4 && m.yields == 2);
    /* progress restarts the stall clock: two 20 s waits split by one byte succeed */
    m = (mock_t){ .retry_cost = 10 * 1000000LL, .script = { RETRY, RETRY, 1, RETRY, RETRY, 2 }, .steps = 6 };
    assert(run(&m, "abc", 3) && m.sent_len == 3 && m.yields == 4 && m.now == 40 * 1000000LL);
    /* nothing to write succeeds without touching the transport */
    m = (mock_t){ 0 };
    assert(run(&m, "", 0) && m.calls == 0);

    /* relay watchdog: only a running loop that stopped beating for longer than the limit is stuck */
    const int64_t s = 1000000LL, limit = 120 * s;
    assert(!rproxy_relay_stuck(500 * s, 0, limit, false));            /* not started yet */
    assert(!rproxy_relay_stuck(500 * s, 400 * s, limit, false));      /* 100 s: within the limit */
    assert(!rproxy_relay_stuck(520 * s, 400 * s, limit, false));      /* exactly the limit */
    assert(rproxy_relay_stuck(521 * s, 400 * s, limit, false));       /* past it */
    assert(!rproxy_relay_stuck(900 * s, 400 * s, limit, true));       /* paused (OTA owns the board) */
    puts("{\"ok\":true,\"groups\":2}");
    return 0;
}
