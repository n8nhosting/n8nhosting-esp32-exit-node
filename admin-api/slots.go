package main

import (
	"context"
	"database/sql"
	"log"
	"net/http"
	"sort"
)

type slotOption struct {
	Slot int `json:"slot"`
	Port int `json:"port"`
}

type slotReservation struct {
	MAC  string
	Slot int
}

// Include every durable reservation, including boards absent from /nodes.
// Broker-only reservations also remain occupied until explicitly changed.
func occupiedSlots(records []*Device, nodes []brokerNode, excludingMAC string) map[int]bool {
	occupied := map[int]bool{}
	for _, d := range records {
		if d.MAC != excludingMAC && d.Slot > 0 {
			occupied[d.Slot] = true
		}
	}
	for _, n := range nodes {
		if n.MAC != excludingMAC && n.Slot > 0 {
			occupied[n.Slot] = true
		}
	}
	return occupied
}

// GET /api/slots?mac=<target> suggests from the same authoritative sets used by
// the allocator. It never reserves or changes routing; PUT is the final check.
func (s *server) handleSlots(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method"})
		return
	}
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	var nodes []brokerNode
	if err := s.brokerGET("/nodes", &nodes); err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": "broker unreachable"})
		return
	}
	records, err := s.devices.listForRouting()
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": "slots unavailable"})
		return
	}
	mac := r.URL.Query().Get("mac")
	current := 0
	for _, d := range records {
		if d.MAC == mac {
			current = d.Slot
		}
	}
	used := occupiedSlots(records, nodes, mac)
	available := []slotOption{}
	suggested := 0
	for slot := 1; slot <= slotCount(); slot++ {
		if !used[slot] {
			available = append(available, slotOption{Slot: slot, Port: slotPort(slot)})
			if suggested == 0 {
				suggested = slot
			}
		}
	}
	if current > 0 && current <= slotCount() && !used[current] {
		suggested = current
	}
	writeJSON(w, http.StatusOK, map[string]any{"slot_count": slotCount(), "port_base": 3130,
		"current_slot": current, "available": available, "suggested_slot": suggested})
}

// listForRouting propagates DB errors. Allocation must never treat a failed or
// partial store read as an empty set and accidentally re-use a reserved slot.
func (s *DeviceStore) listForRouting() ([]*Device, error) {
	rows, err := s.db.Query(`SELECT ` + deviceCols + ` FROM devices ORDER BY mac`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := []*Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, d)
	}
	return records, rows.Err()
}

// reserveAutomaticSlots runs under routingMu and makes all new reservations in
// one SQLite write transaction. No broker/network call occurs inside it.
func (s *DeviceStore) reserveAutomaticSlots(nodes []brokerNode, count int) ([]slotReservation, error) {
	ctx := context.Background()
	reserved := []slotReservation{}
	err := inTx(ctx, s.db, func(c *sql.Conn) error {
		rows, err := c.QueryContext(ctx, `SELECT `+deviceCols+` FROM devices ORDER BY mac`)
		if err != nil {
			return err
		}
		records := []*Device{}
		byMAC := map[string]*Device{}
		for rows.Next() {
			d, err := scanDevice(rows)
			if err != nil {
				rows.Close()
				return err
			}
			records = append(records, d)
			byMAC[d.MAC] = d
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return err
		}
		// Deterministic first registration order, independent of broker map order.
		ordered := append([]brokerNode(nil), nodes...)
		sort.Slice(ordered, func(i, j int) bool { return ordered[i].MAC < ordered[j].MAC })
		for _, n := range ordered {
			if n.MAC == "" {
				continue
			}
			d := byMAC[n.MAC]
			if d != nil && (d.Slot > 0 || !d.SlotAuto) {
				continue
			}
			used := occupiedSlots(records, nodes, n.MAC)
			slot := 0
			// A broker-only assignment may already have customers. Adopt it when
			// free in the durable store rather than moving a functioning port.
			if n.Slot > 0 && n.Slot <= count && !used[n.Slot] {
				slot = n.Slot
			} else {
				for candidate := 1; candidate <= count; candidate++ {
					if !used[candidate] {
						slot = candidate
						break
					}
				}
			}
			if slot == 0 {
				continue // exhausted: do not evict/recycle an offline reservation
			}
			if d == nil {
				d = &Device{MAC: n.MAC, Kind: "shared", SlotAuto: true}
				if n.Pool != nil && !*n.Pool {
					d.Kind = "dedicated" // preserve broker-only routing policy
				}
				records = append(records, d)
				byMAC[n.MAC] = d
			}
			d.Slot = slot
			if _, err := c.ExecContext(ctx, deviceUpsertSQL, deviceArgs(d)...); err != nil {
				return err
			}
			reserved = append(reserved, slotReservation{MAC: n.MAC, Slot: slot})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return reserved, nil
}

// Called by the existing sampler. Read the broker while holding routingMu, then
// reserve and reconcile before releasing it, so manual updates cannot race a
// stale snapshot. Failed pushes keep their durable reservation for next poll.
func (s *server) brokerNodesWithRouting() ([]brokerNode, error) {
	s.routingMu.Lock()
	defer s.routingMu.Unlock()
	var nodes []brokerNode
	if err := s.brokerGET("/nodes", &nodes); err != nil {
		return nil, err
	}
	reserved, err := s.devices.reserveAutomaticSlots(nodes, slotCount())
	if err != nil {
		log.Printf("routing: automatic reservation failed: %v", err)
		return nodes, nil // this is not a broker outage or an offline sample
	}
	for _, item := range reserved {
		s.emit("device.slot_reserved", "info", "device", item.MAC, "system", "Proxy slot reserved automatically",
			map[string]any{"slot": item.Slot, "port": slotPort(item.Slot), "source": "auto"})
	}
	records, err := s.devices.listForRouting()
	if err != nil {
		log.Printf("routing: could not read reservations: %v", err)
		return nodes, nil
	}
	byMAC := map[string]*Device{}
	for _, d := range records {
		byMAC[d.MAC] = d
	}
	for i := range nodes {
		n := &nodes[i]
		d := byMAC[n.MAC]
		if d == nil || (n.Slot == d.Slot && (n.Pool == nil || *n.Pool == poolFlag(d.Kind))) {
			continue
		}
		if err := s.brokerSetSlot(d.MAC, d.Slot, poolFlag(d.Kind)); err != nil {
			log.Printf("routing: slot %d for %s pending: %v", d.Slot, d.MAC, err)
			continue
		}
		n.Slot = d.Slot
		pool := poolFlag(d.Kind)
		n.Pool = &pool
	}
	return nodes, nil
}
