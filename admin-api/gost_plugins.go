package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"time"
)

// ---- auther plugin ------------------------------------------------------
// GOST POSTs {username,password,client}; we reply {ok, id}. id is passed to the
// limiter and observer as "client", which is how we attribute rate and usage.

type authReq struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Client   string `json:"client"`
}
type authResp struct {
	OK bool   `json:"ok"`
	ID string `json:"id"`
}

func (s *server) handleAuth(w http.ResponseWriter, r *http.Request) {
	var req authReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, authResp{OK: false})
		return
	}
	// gost does not send the service name: each slot service's auther URL
	// carries ?slot=N, and the pool port's carries none
	slot := r.URL.Query().Get("slot")
	ok, reason := s.store.Authorize(req.Username, req.Password, time.Now())
	if ok {
		ok, reason = s.portAllowed(req.Username, slot)
	}
	if !ok {
		log.Printf("auth deny user=%q from=%q slot=%q: %s", req.Username, req.Client, slot, reason)
		writeJSON(w, http.StatusOK, authResp{OK: false})
		return
	}
	writeJSON(w, http.StatusOK, authResp{OK: true, ID: req.Username})
}

// portAllowed reports whether username may use the gost service that asked:
// slot port N (slot "N") only when bound to the board holding slot N; the
// pool port (slot "") when unbound, or bound to a board with no slot yet (so
// bound users keep working until the operator assigns their board a slot).
func (s *server) portAllowed(username, slot string) (bool, string) {
	u, err := s.store.Get(username)
	if err != nil {
		return false, "store: " + err.Error()
	}
	d, err := s.boundDevice(u)
	if err != nil {
		return false, "store: " + err.Error()
	}
	held := 0
	if d != nil {
		held = d.Slot
	}
	if slot == "" {
		if held == 0 {
			return true, ""
		}
		return false, "wrong port"
	}
	if n, err := strconv.Atoi(slot); err != nil || n < 1 || n != held {
		return false, "wrong port"
	}
	return true, ""
}

// ---- limiter plugin -----------------------------------------------------
// GOST POSTs {scope,service,network,addr,client,src}; reply {in,out} bytes/sec.
// client is the user id returned by the auther. 0 => unlimited. A user bound to
// a measured board gets min(own rate, board capacity); own rate 0 => capacity.

type limiterReq struct {
	Scope   string `json:"scope"`
	Service string `json:"service"`
	Network string `json:"network"`
	Addr    string `json:"addr"`
	Client  string `json:"client"`
	Src     string `json:"src"`
}
type limiterResp struct {
	In  int64 `json:"in"`
	Out int64 `json:"out"`
}

func (s *server) handleLimiter(w http.ResponseWriter, r *http.Request) {
	var req limiterReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, limiterResp{})
		return
	}
	// Only enforce at the per-user (client) scope. Service/conn scopes get the
	// user's cap too when a client id is present; otherwise unlimited.
	// A measurement user is never capped, or a board could never measure
	// faster than its last result.
	if req.Client == "" || isReservedUsername(req.Client) {
		writeJSON(w, http.StatusOK, limiterResp{In: 0, Out: 0})
		return
	}
	capMbps := 0.0
	if u, err := s.store.Get(req.Client); err == nil {
		if d, err := s.boundDevice(u); err != nil {
			log.Printf("limiter: board of user=%q: %v (user rate only)", req.Client, err)
		} else if d != nil {
			capMbps = d.CapacityMbps
		}
	}
	bps := s.store.RateBytesPerSecCapped(req.Client, capMbps)
	writeJSON(w, http.StatusOK, limiterResp{In: bps, Out: bps})
}

// ---- observer plugin ----------------------------------------------------
// GOST POSTs {events:[{kind,service,client,type,stats:{inputBytes,outputBytes,...}}]}.
// We accumulate per-client traffic for monthly quota accounting.
//
// GOST handler-level observers report per-client stats every observePeriod.
// Whether inputBytes/outputBytes are per-period deltas or cumulative-per-
// connection varies by version, so USAGE_MODE selects the interpretation:
//   delta       - add each event's bytes as-is (default; matches periodic deltas)
//   cumulative  - add only the increase over the last seen total per client

type observerStats struct {
	TotalConns   uint64 `json:"totalConns"`
	CurrentConns uint64 `json:"currentConns"`
	InputBytes   uint64 `json:"inputBytes"`
	OutputBytes  uint64 `json:"outputBytes"`
	TotalErrs    uint64 `json:"totalErrs"`
}
type observerEvent struct {
	Kind    string         `json:"kind"`
	Service string         `json:"service"`
	Client  string         `json:"client"`
	Type    string         `json:"type"`
	Stats   *observerStats `json:"stats"`
}
type observerReq struct {
	Events []observerEvent `json:"events"`
}

func (s *server) handleObserver(w http.ResponseWriter, r *http.Request) {
	var req observerReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	now := time.Now()
	for _, e := range req.Events {
		if e.Type != "stats" || e.Client == "" || e.Stats == nil {
			continue
		}
		total := int64(e.Stats.InputBytes + e.Stats.OutputBytes)
		var add int64
		if s.usageMode == "cumulative" {
			key := e.Service + "|" + e.Client
			prev := s.lastSeen[key]
			if total >= prev {
				add = total - prev
			} else {
				add = total // counter reset (new connection)
			}
			s.lastSeen[key] = total
		} else {
			add = total
		}
		if add > 0 {
			s.store.AddUsage(e.Client, add, now)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
