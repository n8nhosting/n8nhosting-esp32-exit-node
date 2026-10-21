// admission.go - binding users to boards: who takes a seat on each board,
// the capacity rule a binding must pass, and the bound board the gost
// plugins and /credentials look up.
package main

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
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

// usernameRE is the charset of a username created over HTTP: nothing that
// needs escaping in markup, a URL's userinfo or a proxy connection string.
var usernameRE = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func validUsername(name string) bool { return usernameRE.MatchString(name) }

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
// are counted only on that MAC, not counting username itself when already
// bound to the target. Shared public IPs are informational and do not combine
// limits or confer ownership over other boards. It returns 0 when the binding
// may proceed, else the status and body to answer with. Callers hold admitMu
// until the binding is written.
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
		nodes = append(nodes, brokerNode{MAC: mac})
	}
	views := s.buildDeviceViews(nodes)
	var v deviceView
	for _, dv := range views {
		if dv.MAC == mac {
			v = dv
		}
	}
	taken := v.Seats
	if u.DeviceMAC == mac && takesSeat(u, time.Now()) {
		taken-- // self-reassignment does not consume a second seat
	}
	reason := ""
	switch {
	case v.Kind == "dedicated" && taken >= v.MaxUsers:
		reason = "dedicated device already has a user"
	case v.Kind != "dedicated" && v.CapacityMbps <= 0:
		reason = "capacity not measured"
	case v.Kind != "dedicated" && taken >= v.MaxUsers:
		reason = fmt.Sprintf("shared device full (%d/%d)", taken, v.MaxUsers)
	}
	if reason != "" {
		return http.StatusConflict, map[string]string{"error": "capacity", "reason": reason}
	}
	return 0, nil
}
