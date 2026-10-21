/* The OTA ownership/deadline policy is independent of IDF for host testing. */
#ifndef RPROXY_OTA_TRANSFER_H
#define RPROXY_OTA_TRANSFER_H

#include <stdbool.h>
#include <stdint.h>

typedef struct {
    void *ctx;
    int (*begin)(void *);
    int (*perform)(void *);
    bool (*complete)(void *);
    int (*finish)(void *); /* consumes the handle, including on verification failure */
    void (*abort)(void *);
    int64_t (*now_us)(void *);
    void (*yield)(void *);
    int in_progress, timeout, incomplete;
} rproxy_ota_ops_t;

static inline int rproxy_ota_budget_seconds(int configured)
{
    return configured < 30 ? 30 : configured > 600 ? 600 : configured;
}

static inline int rproxy_ota_transfer(const rproxy_ota_ops_t *ops, int64_t deadline_us)
{
    if (ops->now_us(ops->ctx) >= deadline_us) return ops->timeout;
    int err = ops->begin(ops->ctx);
    if (err != 0) return err; /* begin owns its own failure cleanup */
    for (;;) {
        if (ops->now_us(ops->ctx) >= deadline_us) { err = ops->timeout; break; }
        err = ops->perform(ops->ctx);
        if (ops->now_us(ops->ctx) >= deadline_us) { err = ops->timeout; break; }
        if (err != ops->in_progress) break;
        ops->yield(ops->ctx);
    }
    if (err == 0 && !ops->complete(ops->ctx)) err = ops->incomplete;
    if (err == 0 && ops->now_us(ops->ctx) >= deadline_us) err = ops->timeout;
    if (err == 0) return ops->finish(ops->ctx);
    ops->abort(ops->ctx);
    return err;
}

#endif
