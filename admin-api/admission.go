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

// takesSeat reports whether u counts against its board's capacity: enabled,
// unexpired and not a measurement user.
func takesSeat(u *User, now time.Time) bool {
	if !u.Enabled || strings.HasPrefix(u.Username, measurePrefix) {
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
// /nodes), not counting username itself. It returns 0 when the binding may
// proceed, else the status and body to answer with. Callers hold admitMu until
// the binding is written.
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
	ip, known := "", false
	for _, n := range nodes {
		if n.MAC == mac {
			ip, known = n.RemoteIP, true
		}
	}
	if !known {
		if _, err := s.devices.Get(mac); errors.Is(err, ErrNotFound) {
			return http.StatusNotFound, map[string]string{"error": "unknown device"}
		} else if err != nil {
			return http.StatusInternalServerError, map[string]string{"error": "store: " + err.Error()}
		}
		nodes = append(nodes, brokerNode{MAC: mac}) // a group of its own
	}
	var v deviceView
	for _, dv := range s.buildDeviceViews(nodes) {
		if dv.MAC == mac {
			v = dv
		}
	}
	taken := v.ActiveUsers
	if takesSeat(u, time.Now()) {
		inGroup := u.DeviceMAC == mac
		for _, n := range nodes {
			inGroup = inGroup || (ip != "" && n.MAC == u.DeviceMAC && n.RemoteIP == ip)
		}
		if inGroup {
			taken--
		}
	}
	reason := ""
	switch {
	case v.Kind == "dedicated":
		if taken >= v.MaxUsers {
			reason = "dedicated device already has a user"
		}
	case v.CapacityMbps <= 0:
		reason = "capacity not measured"
	case taken >= v.MaxUsers:
		reason = fmt.Sprintf("shared IP full (%d/%d)", taken, v.MaxUsers)
	}
	if reason != "" {
		return http.StatusConflict, map[string]string{"error": "capacity", "reason": reason}
	}
	return 0, nil
}
