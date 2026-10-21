package main

// Telemetry is sampled by one goroutine. All counter history belongs to the
// admin API; the broker remains the source of live connection state.
import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

type brokerSnapshot struct {
	IdleNodes         int   `json:"idle_nodes"`
	SlotsAssigned     int   `json:"slots_assigned"`
	StreamsEvicted    int64 `json:"streams_evicted"`
	StreamsIdleClosed int64 `json:"streams_idle_closed"`
	Uptime            int64 `json:"uptime_sec"`
	BytesUp           int64 `json:"bytes_up"`
	BytesDown         int64 `json:"bytes_down"`
	DialsOK           int64 `json:"dials_ok"`
	DialsErr          int64 `json:"dials_err"`
	NoNode            int64 `json:"dials_no_node"`
	PinnedOffline     int64 `json:"dials_pinned_offline"`
	NodesOnline       int   `json:"nodes_online"`
	NodesTotal        int   `json:"nodes_total"`
	MuxLinks          int   `json:"mux_links"`
	TLS               bool  `json:"tls"`
}
type observerLive struct {
	at    time.Time
	conns uint64
	rate  float64
}
type nodeBucket struct {
	samples, online            int
	up, down, ok, errs, active int64
}
type metricsBucket struct {
	samples                                          int
	online, nodes, mux, users, seated, over, expired float64
	connected                                        map[string]bool
	up, down, ok, errs                               int64
	nodesByMAC                                       map[string]*nodeBucket
	usersBytes                                       map[string]int64
}
type telemetryState struct {
	mu               sync.Mutex
	sampleMu         sync.Mutex
	previous         *brokerSnapshot
	previousNodes    map[string]brokerNode
	previousAt       time.Time
	nodes            []brokerNode
	asOf             time.Time
	rateUp, rateDown float64 // bytes per second, preserving the backend API contract
	observers        map[string]observerLive
	pending          map[int64]*metricsBucket
	maintainedHour   int64
}

func (t *telemetryState) bucket(ts int64) *metricsBucket {
	if t.pending == nil {
		t.pending = map[int64]*metricsBucket{}
	}
	b := t.pending[ts]
	if b == nil {
		b = &metricsBucket{connected: map[string]bool{}, nodesByMAC: map[string]*nodeBucket{}, usersBytes: map[string]int64{}}
		t.pending[ts] = b
	}
	return b
}
func counterDelta(cur, prev int64, reset bool) int64 {
	if cur < 0 {
		return 0
	}
	if reset || cur < prev {
		return cur
	}
	return cur - prev
}
func (s *server) recordObserver(e observerEvent, add int64, now time.Time) {
	if isReservedUsername(e.Client) {
		return
	}
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	t := &s.telemetry
	if t.observers == nil {
		t.observers = map[string]observerLive{}
	}
	key := e.Service + "|" + e.Client
	old := t.observers[key]
	rate := old.rate
	if dt := now.Sub(old.at).Seconds(); !old.at.IsZero() && dt > 0 && dt <= 60 {
		instant := float64(add) / dt
		rate = .35*instant + .65*rate
	} else {
		rate = 0
	}
	if e.Stats.CurrentConns == 0 {
		rate = 0
	}
	t.observers[key] = observerLive{now, e.Stats.CurrentConns, rate}
	b := t.bucket(now.Unix() / 300 * 300)
	b.connected[e.Client] = true
	if add > 0 {
		b.usersBytes[e.Client] += add
	}
}
func (s *server) connectedNow(now time.Time) int {
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	seen := map[string]bool{}
	for key, v := range s.telemetry.observers {
		if now.Sub(v.at) > 60*time.Second {
			delete(s.telemetry.observers, key)
			continue
		}
		if v.conns > 0 {
			parts := strings.SplitN(key, "|", 2)
			if len(parts) == 2 {
				seen[parts[1]] = true
			}
		}
	}
	return len(seen)
}
func (s *server) publicUser(u *User) PublicUser {
	p := u.Public()
	now := time.Now()
	s.telemetry.mu.Lock()
	defer s.telemetry.mu.Unlock()
	for key, v := range s.telemetry.observers {
		if strings.HasSuffix(key, "|"+u.Username) && now.Sub(v.at) <= 60*time.Second && v.conns > 0 {
			p.LiveBPS += v.rate
		}
	}
	return p
}
func (s *server) runTelemetry() {
	interval, err := time.ParseDuration(envOr("POLL_INTERVAL", "60s"))
	if err != nil || interval < time.Second {
		interval = time.Minute
	}
	s.sampleTelemetry(time.Now())
	tick := time.NewTicker(interval)
	defer tick.Stop()
	for range tick.C {
		s.sampleTelemetry(time.Now())
	}
}
func (s *server) sampleTelemetry(now time.Time) {
	t := &s.telemetry
	t.sampleMu.Lock()
	defer t.sampleMu.Unlock()
	var status brokerSnapshot
	statusErr := s.brokerGET("/status", &status)
	nodes, nodesErr := s.brokerNodesWithRouting()
	brokerOK := statusErr == nil && nodesErr == nil
	// Activity owns its own lock and may read stores or emit events.
	s.observeActivity(nodes, brokerOK, now)
	users := s.store.List() // Never query a store from inside the write transaction.
	t.mu.Lock()
	if brokerOK {
		b := t.bucket(now.Unix() / 300 * 300)
		b.samples++
		b.online += float64(status.NodesOnline)
		b.nodes += float64(status.NodesTotal)
		b.mux += float64(status.MuxLinks)
		for _, u := range users {
			if isReservedUsername(u.Username) {
				continue
			}
			b.users++
			if takesSeat(u, now) {
				b.seated++
			}
			switch userStatus(u, now) {
			case "expired":
				b.expired++
			case "over_quota":
				b.over++
			}
		}
		reset := t.previous != nil && status.Uptime < t.previous.Uptime
		t.rateUp, t.rateDown = 0, 0
		if t.previous != nil {
			up := counterDelta(status.BytesUp, t.previous.BytesUp, reset)
			down := counterDelta(status.BytesDown, t.previous.BytesDown, reset)
			b.up += up
			b.down += down
			b.ok += counterDelta(status.DialsOK, t.previous.DialsOK, reset)
			b.errs += counterDelta(status.DialsErr, t.previous.DialsErr, reset) + counterDelta(status.NoNode, t.previous.NoNode, reset) + counterDelta(status.PinnedOffline, t.previous.PinnedOffline, reset)
			if dt := now.Sub(t.previousAt).Seconds(); dt > 0 {
				t.rateUp = float64(up) / dt
				t.rateDown = float64(down) / dt
			}
		}
		for _, n := range nodes {
			nb := b.nodesByMAC[n.MAC]
			if nb == nil {
				nb = &nodeBucket{}
				b.nodesByMAC[n.MAC] = nb
			}
			nb.samples++
			if n.Online {
				nb.online++
			}
			if n.Active > nb.active {
				nb.active = n.Active
			}
			if prev, ok := t.previousNodes[n.MAC]; ok {
				nb.up += counterDelta(n.BytesUp, prev.BytesUp, reset)
				nb.down += counterDelta(n.BytesDown, prev.BytesDown, reset)
				nb.ok += counterDelta(n.DialsOK, prev.DialsOK, reset)
				nb.errs += counterDelta(n.DialsErr, prev.DialsErr, reset)
			}
		}
		t.previous = &status
		t.previousNodes = map[string]brokerNode{}
		for _, n := range nodes {
			t.previousNodes[n.MAC] = n
		}
		t.previousAt = now
		t.nodes = append([]brokerNode(nil), nodes...)
		t.asOf = now
	}
	// Failed polls leave a gap, never manufacture an offline measurement.
	if err := s.flushMetricsLocked(now); err != nil {
		log.Printf("metrics flush: %v", err)
	}
	hour := now.Unix() / 3600
	maintenance := t.maintainedHour != hour
	t.mu.Unlock()
	if maintenance {
		if err := s.maintainMetrics(now); err != nil {
			log.Printf("metrics maintenance: %v", err)
		} else {
			t.mu.Lock()
			t.maintainedHour = hour
			t.mu.Unlock()
		}
	}
}
func (s *server) flushMetricsLocked(now time.Time) error {
	t := &s.telemetry
	cut := now.Unix() / 300 * 300
	timestamps := []int64{}
	for ts := range t.pending {
		if ts < cut {
			timestamps = append(timestamps, ts)
		}
	}
	if len(timestamps) == 0 {
		return nil
	}
	sort.Slice(timestamps, func(i, j int) bool { return timestamps[i] < timestamps[j] })
	err := inTx(context.Background(), s.store.DB(), func(c *sql.Conn) error {
		for _, ts := range timestamps {
			b := t.pending[ts]
			if b.samples > 0 {
				n := float64(b.samples)
				_, err := c.ExecContext(context.Background(), `INSERT INTO metrics_fleet VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ts, "5m", b.online/n, b.nodes/n, b.mux/n, b.users/n, b.seated/n, len(b.connected), b.over/n, b.expired/n, b.up, b.down, b.ok, b.errs)
				if err != nil {
					return err
				}
			}
			for mac, n := range b.nodesByMAC {
				_, err := c.ExecContext(context.Background(), `INSERT INTO metrics_node VALUES (?,?,?,?,?,?,?,?,?)`, ts, "5m", mac, float64(n.online)/float64(n.samples), n.up, n.down, n.ok, n.errs, n.active)
				if err != nil {
					return err
				}
			}
			for user, n := range b.usersBytes {
				if n <= 0 {
					continue
				}
				_, err := c.ExecContext(context.Background(), `INSERT INTO metrics_user VALUES (?,?,?,?)`, ts, "5m", user, n)
				if err != nil {
					return err
				}
			}
		}
		return nil
	})
	// A failed transaction keeps every pending counter for a later retry.
	if err == nil {
		for _, ts := range timestamps {
			delete(t.pending, ts)
		}
	}
	return err
}
func (s *server) maintainMetrics(now time.Time) error {
	retention := atoiDefault(envOr("METRICS_RETENTION_DAYS", "90"), 90)
	if retention < 1 {
		retention = 90
	}
	hour := now.Unix() / 3600 * 3600
	fineCut := hour - 48*3600
	old := hour - int64(retention)*86400
	return inTx(context.Background(), s.store.DB(), func(c *sql.Conn) error {
		queries := []string{
			`INSERT OR REPLACE INTO metrics_node SELECT (ts/3600)*3600,'1h',mac,AVG(online_frac),SUM(bytes_up),SUM(bytes_down),SUM(dials_ok),SUM(dials_err),MAX(active_max) FROM metrics_node WHERE step='5m' AND ts<? GROUP BY ts/3600,mac`,
			`INSERT OR REPLACE INTO metrics_fleet SELECT (ts/3600)*3600,'1h',AVG(nodes_online),AVG(nodes_total),AVG(mux_links),AVG(users_total),AVG(users_seated),AVG(users_connected),AVG(users_over_quota),AVG(users_expired),SUM(bytes_up),SUM(bytes_down),SUM(dials_ok),SUM(dials_err) FROM metrics_fleet WHERE step='5m' AND ts<? GROUP BY ts/3600`,
			`INSERT OR REPLACE INTO metrics_user SELECT (ts/3600)*3600,'1h',username,SUM(bytes) FROM metrics_user WHERE step='5m' AND ts<? GROUP BY ts/3600,username`,
		}
		for _, q := range queries {
			if _, err := c.ExecContext(context.Background(), q, hour); err != nil {
				return err
			}
		}
		for _, table := range []string{"metrics_node", "metrics_fleet", "metrics_user"} {
			if _, err := c.ExecContext(context.Background(), `DELETE FROM `+table+` WHERE (step='5m' AND ts<?) OR (step='1h' AND ts<?)`, fineCut, old); err != nil {
				return err
			}
		}
		return nil
	})
}

type metricField struct{ name, op string }

var metricFields = map[string][]metricField{
	"fleet": {{"nodes_online", "avg"}, {"nodes_total", "avg"}, {"mux_links", "avg"}, {"users_total", "avg"}, {"users_seated", "avg"}, {"users_connected", "avg"}, {"users_over_quota", "avg"}, {"users_expired", "avg"}, {"bytes_up", "sum"}, {"bytes_down", "sum"}, {"dials_ok", "sum"}, {"dials_err", "sum"}},
	"node":  {{"online_frac", "avg"}, {"bytes_up", "sum"}, {"bytes_down", "sum"}, {"dials_ok", "sum"}, {"dials_err", "sum"}, {"active_max", "max"}},
	"user":  {{"bytes", "sum"}},
}

type metricRow struct {
	ts     int64
	step   string
	values []float64
}
type metricAgg struct {
	sum, max float64
	count    int
}

func (a *metricAgg) add(v float64) {
	a.sum += v
	if a.count == 0 || v > a.max {
		a.max = v
	}
	a.count++
}
func (a metricAgg) value(op string) any {
	if a.count == 0 {
		return nil
	}
	if op == "avg" {
		return a.sum / float64(a.count)
	}
	if op == "max" {
		return a.max
	}
	return a.sum
}
func fieldsFor(entity, raw string) ([]metricField, error) {
	available := metricFields[entity]
	if raw == "" {
		if entity == "fleet" {
			return []metricField{{"bytes_up", "sum"}, {"bytes_down", "sum"}, {"nodes_online", "avg"}}, nil
		}
		return available, nil
	}
	names := strings.Split(raw, ",")
	if len(names) > 8 {
		return nil, fmt.Errorf("at most 8 fields")
	}
	out := []metricField{}
	seen := map[string]bool{}
	for _, name := range names {
		found := false
		for _, f := range available {
			if f.name == name {
				if !seen[name] {
					out = append(out, f)
					seen[name] = true
				}
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("unknown field %s", name)
		}
	}
	return out, nil
}
func (s *server) readMetricRows(entity, id string, fields []metricField, from, to int64) ([]metricRow, error) {
	names := []string{}
	for _, f := range fields {
		names = append(names, f.name)
	}
	q := `SELECT ts,step,` + strings.Join(names, ",") + ` FROM metrics_` + entity + ` WHERE ts>=? AND ts<?`
	args := []any{from, to}
	if entity == "node" {
		q += ` AND mac=?`
		args = append(args, strings.ToLower(id))
	}
	if entity == "user" {
		q += ` AND username=?`
		args = append(args, id)
	}
	rows, err := s.store.DB().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []metricRow{}
	for rows.Next() {
		r := metricRow{values: make([]float64, len(fields))}
		dest := []any{&r.ts, &r.step}
		for i := range r.values {
			dest = append(dest, &r.values[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// aggregateMetricRows selects one source per hour, avoiding 1h/5m overlap.
func aggregateMetricRows(rows []metricRow, fields []metricField, step int64) map[int64][]metricAgg {
	if step > 3600 {
		hours := aggregateMetricRows(rows, fields, 3600)
		out := map[int64][]metricAgg{}
		for hour, bucket := range hours {
			ts := hour / step * step
			a := out[ts]
			if a == nil {
				a = make([]metricAgg, len(fields))
			}
			for i, f := range fields {
				if value, ok := bucket[i].value(f.op).(float64); ok {
					a[i].add(value)
				}
			}
			out[ts] = a
		}
		return out
	}
	hourly := map[int64]bool{}
	if step >= 3600 {
		for _, r := range rows {
			if r.step == "1h" {
				hourly[r.ts] = true
			}
		}
	}
	out := map[int64][]metricAgg{}
	for _, r := range rows {
		if step < 3600 && r.step != "5m" {
			continue
		}
		if step >= 3600 && r.step == "5m" && hourly[r.ts/3600*3600] {
			continue
		}
		ts := r.ts / step * step
		a := out[ts]
		if a == nil {
			a = make([]metricAgg, len(fields))
		}
		for i, v := range r.values {
			a[i].add(v)
		}
		out[ts] = a
	}
	return out
}
func (s *server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	s.handleMetricsAt(w, r, time.Now())
}
func (s *server) handleMetricsAt(w http.ResponseWriter, r *http.Request, now time.Time) {
	w.Header().Set("Cache-Control", "no-store")
	bad := func(msg string) { writeJSON(w, 400, map[string]string{"error": msg}) }
	if r.Method != http.MethodGet {
		writeJSON(w, 405, map[string]string{"error": "method"})
		return
	}
	q := r.URL.Query()
	entity := q.Get("entity")
	if entity == "" {
		entity = "fleet"
	}
	totals := entity == "nodes" || entity == "users"
	base := entity
	if totals {
		base = strings.TrimSuffix(entity, "s")
	}
	if _, ok := metricFields[base]; !ok {
		bad("unknown entity")
		return
	}
	id := q.Get("id")
	if !totals && base != "fleet" && id == "" {
		bad("id required")
		return
	}
	ranges := map[string]int64{"24h": 86400, "48h": 172800, "7d": 604800, "30d": 2592000}
	rangeName := q.Get("range")
	if rangeName == "" {
		rangeName = "24h"
	}
	duration, ok := ranges[rangeName]
	if !ok {
		bad("unknown range")
		return
	}
	stepName := q.Get("step")
	if stepName == "" {
		stepName = "5m"
		if duration > 172800 {
			stepName = "1h"
		}
	}
	step, ok := map[string]int64{"5m": 300, "1h": 3600, "1d": 86400}[stepName]
	if !ok || step == 300 && duration > 172800 || duration/step > 1440 {
		bad("invalid step for range")
		return
	}
	fields, err := fieldsFor(base, q.Get("fields"))
	if err != nil {
		bad(err.Error())
		return
	}
	to := (now.Unix()/step + 1) * step
	from := to - duration
	if totals {
		s.handleMetricTotals(w, base, fields, from, to)
		return
	}
	window := step
	if duration >= 604800 {
		window = 86400
	}
	end := now.Unix() / window * window
	readFrom := from
	if end-2*window < readFrom {
		readFrom = end - 2*window
	}
	rows, err := s.readMetricRows(base, id, fields, readFrom, to)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "metrics unavailable"})
		return
	}
	agg := aggregateMetricRows(rows, fields, step)
	var coverageRows []metricRow
	var createdAt time.Time
	// Sparse user counters are zero only where the fleet actually observed
	// telemetry and the account already existed. Unknown IDs and periods
	// before sampling began retain null, so an empty chart is honest.
	if base == "user" {
		if u, err := s.store.Get(id); err == nil {
			coverage, err := s.readMetricRows("fleet", "", []metricField{{"bytes_up", "sum"}}, readFrom, to)
			if err == nil {
				created, _ := time.Parse(time.RFC3339, u.CreatedAt)
				coverageRows, createdAt = coverage, created
				for ts := range aggregateMetricRows(coverage, []metricField{{"bytes_up", "sum"}}, step) {
					if !created.IsZero() && ts+step <= created.Unix() {
						continue
					}
					if agg[ts] == nil {
						a := make([]metricAgg, len(fields))
						for i := range a {
							a[i].add(0)
						}
						agg[ts] = a
					}
				}
			}
		}
	}
	series := map[string][]any{}
	n := int(duration / step)
	for i, f := range fields {
		a := make([]any, n)
		for p := 0; p < n; p++ {
			if b := agg[from+int64(p)*step]; b != nil {
				a[p] = b[i].value(f.op)
			}
		}
		series[f.name] = a
	}
	summary := map[string]any{}
	winAgg := aggregateMetricRows(rows, fields, window)
	for ts := range aggregateMetricRows(coverageRows, []metricField{{"bytes_up", "sum"}}, window) {
		if !createdAt.IsZero() && ts+window <= createdAt.Unix() {
			continue
		}
		if winAgg[ts] == nil {
			a := make([]metricAgg, len(fields))
			for i := range a {
				a[i].add(0)
			}
			winAgg[ts] = a
		}
	}
	for i, f := range fields {
		var last, prev any
		if a := winAgg[end-window]; a != nil {
			last = a[i].value(f.op)
		}
		if a := winAgg[end-2*window]; a != nil {
			prev = a[i].value(f.op)
		}
		var delta any
		if l, ok := last.(float64); ok {
			if p, ok := prev.(float64); ok && p != 0 {
				delta = (l - p) / p * 100
			}
		}
		summary[f.name] = map[string]any{"last": last, "prev": prev, "delta_pct": delta, "window_sec": window}
	}
	writeJSON(w, 200, map[string]any{"entity": base, "id": id, "from": from, "to": to, "step_sec": step, "n": n, "series": series, "summary": summary})
}
func (s *server) handleMetricTotals(w http.ResponseWriter, entity string, fields []metricField, from, to int64) {
	col := "mac"
	if entity == "user" {
		col = "username"
	}
	rows, err := s.store.DB().Query(`SELECT DISTINCT `+col+` FROM metrics_`+entity+` WHERE ts>=? AND ts<?`, from, to)
	if err != nil {
		writeJSON(w, 500, map[string]string{"error": "metrics unavailable"})
		return
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			writeJSON(w, 500, map[string]string{"error": "metrics unavailable"})
			return
		}
		ids = append(ids, id)
	}
	rows.Close()
	totals := map[string]any{}
	for _, id := range ids {
		rs, err := s.readMetricRows(entity, id, fields, from, to)
		if err != nil {
			writeJSON(w, 500, map[string]string{"error": "metrics unavailable"})
			return
		}
		a := aggregateMetricRows(rs, fields, 3600)
		acc := make([]metricAgg, len(fields))
		for _, bucket := range a {
			for i, v := range bucket {
				if val, ok := v.value(fields[i].op).(float64); ok {
					acc[i].add(val)
				}
			}
		}
		m := map[string]any{}
		for i, f := range fields {
			m[f.name] = acc[i].value(f.op)
		}
		totals[id] = m
	}
	writeJSON(w, 200, map[string]any{"totals": totals})
}

// Keep arithmetic finite when presented with invalid external telemetry.
func clamp(v float64) float64 {
	if math.IsNaN(v) || v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
