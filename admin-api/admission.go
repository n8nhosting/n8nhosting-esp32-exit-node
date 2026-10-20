// admission.go - binding users to boards: who takes a seat on a board's IP
// group, the capacity rule a binding must pass, and the bound board the gost
// plugins and /credentials look up.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// measurePrefix marks the throwaway users that measure a board's capacity:
// they are never rate-capped and never take a seat.
const measurePrefix = "_measure-"

// isReservedUsername reports whether name is a measurement user's. Such a
// user is uncapped and seatless, so only the store may create one: the HTTP
// endpoints that create users (admin API, payment webhook) refuse the prefix.
func isReservedUsername(name string) bool { return strings.HasPrefix(name, measurePrefix) }

// takesSeat reports whether u counts against its board's capacity: enabled,
// unexpired and not a measurement user.
func takesSeat(u *User, now time.Time) bool {
	if !u.Enabled || isReservedUsername(u.Username) {
		return false
	}
	if u.ExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, u.ExpiresAt); err == nil && now.After(exp) {
			return false
		}
	}
	return true
}

// boundDevice is the record of the board u is bound to; nil when u is unbound
// or the board has no record.
func (s *server) boundDevice(u *User) (*Device, error) {
	if u.DeviceMAC == "" {
		return nil, nil
	}
	d, err := s.devices.Get(u.DeviceMAC)
	if errors.Is(err, ErrNotFound) {
		return nil, nil
	}
	return d, err
}

// admit applies the capacity rule to binding username to the board mac. Seats
// are counted over mac's IP group exactly as the device list shows it (broker
// /nodes), not counting username itself. The sellable unit is the exit IP: a
// user on a dedicated board owns the whole group, so a dedicated board takes a
// user only while the rest of its group is empty, and no board in the group
// takes anyone while a dedicated board there is held. It returns 0 when the
// binding may proceed, else the status and body to answer with. Callers hold
// admitMu until the binding is written.
func (s *server) admit(username, mac string) (int, map[string]string) {
	u, err := s.store.Get(username)
	if errors.Is(err, ErrNotFound) {
		return http.StatusNotFound, map[string]string{"error": "not found"}
	}
	if err != nil {
		return http.StatusInternalServerError, map[string]string{"error": "store: " + err.Error()}
	}
	var nodes []brokerNode
	if err := s.brokerGET("/nodes", &nodes); err != nil {
		return http.StatusBadGateway, map[string]string{"error": "broker unreachable"}
	}
	known := false
	for _, n := range nodes {
		known = known || n.MAC == mac
	}
	if !known {
		if _, err := s.devices.Get(mac); errors.Is(err, ErrNotFound) {
			return http.StatusNotFound, map[string]string{"error": "unknown device"}
		} else if err != nil {
			return http.StatusInternalServerError, map[string]string{"error": "store: " + err.Error()}
		}
		nodes = append(nodes, brokerNode{MAC: mac}) // a group of its own
	}
	views := s.buildDeviceViews(nodes)
	var v deviceView
	for _, dv := range views {
		if dv.MAC == mac {
			v = dv
		}
	}
	// seats in mac's IP group held by others: on mac itself, on the group's
	// other boards, and on those of them that are dedicated
	self := takesSeat(u, time.Now())
	onTarget, elsewhere, onDedicated := 0, 0, 0
	for _, dv := range views {
		if dv.MAC != mac && (v.IPGroup == "" || dv.IPGroup != v.IPGroup) {
			continue
		}
		n := dv.Seats
		if self && dv.MAC == u.DeviceMAC {
			n--
		}
		if dv.MAC == mac {
			onTarget = n
			continue
		}
		elsewhere += n
		if dv.Kind == "dedicated" {
			onDedicated += n
		}
	}
	taken := onTarget + elsewhere // the group's active_users, less this user
	reason := ""
	switch {
	case v.Kind == "dedicated" && onTarget >= v.MaxUsers:
		reason = "dedicated device already has a user"
	case onDedicated > 0:
		reason = "IP is dedicated to another user"
	case v.Kind == "dedicated" && elsewhere > 0:
		reason = "IP already shared"
	case v.Kind != "dedicated" && v.CapacityMbps <= 0:
		reason = "capacity not measured"
	case v.Kind != "dedicated" && taken >= v.MaxUsers:
		reason = fmt.Sprintf("shared IP full (%d/%d)", taken, v.MaxUsers)
	}
	if reason != "" {
		return http.StatusConflict, map[string]string{"error": "capacity", "reason": reason}
	}
	return 0, nil
}
