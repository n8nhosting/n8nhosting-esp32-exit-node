/* Execute the same OTA transfer/ownership policy the firmware uses. */
#include <assert.h>
#include <stdio.h>
#include "../firmware/components/rproxy/include/ota_transfer.h"

enum { MORE = 1, TIMEOUT = 101, INCOMPLETE = 102 };
typedef struct {
    int64_t now, begin_cost, step_cost, complete_cost;
    int begin_error, perform_error, finish_error, steps;
    int begins, performs, finishes, aborts, yields;
    bool complete;
} mock_t;
static int begin(void *ctx) { mock_t *m = ctx; m->begins++; m->now += m->begin_cost; return m->begin_error; }
static int perform(void *ctx) { mock_t *m = ctx; m->performs++; m->now += m->step_cost; return m->perform_error ? m->perform_error : m->performs < m->steps ? MORE : 0; }
static bool complete(void *ctx) { mock_t *m = ctx; m->now += m->complete_cost; return m->complete; }
static int finish(void *ctx) { mock_t *m = ctx; m->finishes++; return m->finish_error; }
static void abort_transfer(void *ctx) { ((mock_t *)ctx)->aborts++; }
static int64_t now(void *ctx) { return ((mock_t *)ctx)->now; }
static void yield(void *ctx) { ((mock_t *)ctx)->yields++; }
static int run(mock_t *m) {
    const rproxy_ota_ops_t ops = { .ctx=m, .begin=begin, .perform=perform, .complete=complete,
        .finish=finish, .abort=abort_transfer, .now_us=now, .yield=yield,
        .in_progress=MORE, .timeout=TIMEOUT, .incomplete=INCOMPLETE };
    return rproxy_ota_transfer(&ops, 180);
}
int main(void) {
    mock_t m = {.complete=true, .steps=3, .begin_cost=1, .step_cost=40};
    assert(run(&m)==0 && m.finishes==1 && m.aborts==0 && m.performs==3 && m.yields==2);
    m=(mock_t){.begin_error=201};
    assert(run(&m)==201 && m.begins==1 && m.aborts==0 && m.finishes==0);
    m=(mock_t){.complete=true, .begin_cost=181};
    assert(run(&m)==TIMEOUT && m.performs==0 && m.aborts==1 && m.finishes==0);
    m=(mock_t){.complete=true, .step_cost=180};
    assert(run(&m)==TIMEOUT && m.performs==1 && m.aborts==1 && m.finishes==0);
    m=(mock_t){.complete=true, .steps=100000, .step_cost=70};
    assert(run(&m)==TIMEOUT && m.performs==3 && m.aborts==1 && m.finishes==0);
    m=(mock_t){.perform_error=202};
    assert(run(&m)==202 && m.aborts==1 && m.finishes==0);
    m=(mock_t){.complete=false};
    assert(run(&m)==INCOMPLETE && m.aborts==1 && m.finishes==0);
    m=(mock_t){.complete=true, .finish_error=203};
    assert(run(&m)==203 && m.aborts==0 && m.finishes==1);
    m=(mock_t){.complete=true, .complete_cost=180};
    assert(run(&m)==TIMEOUT && m.aborts==1 && m.finishes==0);
    m=(mock_t){.now=180};
    assert(run(&m)==TIMEOUT && m.begins==0 && m.aborts==0 && m.finishes==0);
    assert(rproxy_ota_budget_seconds(-1)==30 && rproxy_ota_budget_seconds(180)==180 && rproxy_ota_budget_seconds(99999)==600);
    puts("PASS OTA deadline, complete-image gate, and exactly-once finish/abort ownership (10 cases)");
    return 0;
}
