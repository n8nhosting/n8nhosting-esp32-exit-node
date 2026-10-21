package main

import (
	"sync/atomic"
	"time"
)

const otaConfirmationTimeout = 15 * time.Minute
const otaMaxURLBytes = 255 // firmware's OTA URL buffer is char[256]

type otaClaim struct {
	URL                           string
	Revision, ID, HelloGeneration uint64
	FromFW                        string
	Uptime                        uint64
}

// A claim reserves one request for one writer, while leaving its URL durable
// until a complete write. Only the newest accepted mux session may claim new
// commands; legacy connections may claim only when no mux session is live.
func (r *registry) claimOTA(n *node, muxGeneration uint64) (otaClaim, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.nodes[n.MAC] != n || n.PendingOTA == "" || n.OTAStatus != "queued" || n.otaClaimActive != 0 || n.Blocked {
		return otaClaim{}, false
	}
	if muxGeneration > 0 {
		if muxGeneration != n.muxGeneration {
			return otaClaim{}, false
		}
	} else if atomic.LoadInt64(&n.Mux) > 0 {
		return otaClaim{}, false
	}
	n.otaClaimID++
	n.otaClaimActive = n.otaClaimID
	n.otaDispatching = true
	now := timeNow()
	n.otaFromFW, n.otaUptime, n.OTAAt = n.Firmware, n.runningUptime(now), now.UTC().Format(time.RFC3339)
	r.saveLocked()
	return otaClaim{URL: n.PendingOTA, Revision: n.otaRevision, ID: n.otaClaimID,
		HelloGeneration: n.helloGeneration, FromFW: n.Firmware, Uptime: n.otaUptime}, true
}

// A failed/partial write preserves the queued request for another connection.
// A full write is never automatically resent: TCP success does not prove the
// board accepted/flashed the image, and repeating could reboot it indefinitely.
func (r *registry) completeOTA(n *node, claim otaClaim, written bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if claim.ID == 0 || r.nodes[n.MAC] != n || n.otaRevision != claim.Revision || n.otaClaimActive != claim.ID {
		return // forgotten node, cancellation, replacement, or an obsolete writer
	}
	n.otaClaimActive = 0
	n.otaDispatching = false
	if !written {
		n.OTAError = "write_failed"
		r.saveLocked()
		return
	}
	now := timeNow()
	n.PendingOTA = ""
	n.OTAStatus, n.OTAAt, n.OTAError = "sent", now.UTC().Format(time.RFC3339), ""
	n.otaFromFW, n.otaUptime = claim.FromFW, claim.Uptime
	if n.helloGeneration != claim.HelloGeneration && n.Firmware != claim.FromFW {
		// A fast target HELLO may have arrived before this write completion.
		// An old-version HELLO interleaving with the write cannot prove failure:
		// it may have preceded delivery, so wait for fresh evidence or timeout.
		resolveOTALocked(n, now, false, false)
	}
	r.saveLocked()
}

// A missing legacy OK after a complete command is ambiguous, so keep sent
// instead of risking a second OTA. A later HELLO or timeout resolves it.
func (r *registry) noteOTAUnconfirmed(n *node, claim otaClaim) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if claim.ID != 0 && r.nodes[n.MAC] == n && n.otaRevision == claim.Revision && n.otaClaimID == claim.ID && n.OTAStatus == "sent" {
		n.OTAError = "ack_unconfirmed"
		r.saveLocked()
	}
}

func expireOTALocked(n *node, now time.Time) bool {
	if n.OTAStatus != "sent" {
		return false
	}
	at, err := time.Parse(time.RFC3339, n.OTAAt)
	if err == nil && now.Sub(at) >= 0 && now.Sub(at) <= otaConfirmationTimeout {
		return false
	}
	// Invalid/missing dates cannot justify keeping an update in progress.
	n.OTAStatus, n.OTAError = "timeout", "confirmation_timeout"
	return true
}

// Success requires a subsequent HELLO reporting the requested version. A late
// successful confirmation may recover a timeout. Unchanged old firmware is a
// generic failed update; this evidence alone cannot prove a rollback.
func resolveOTALocked(n *node, now time.Time, rebooted, modern bool) bool {
	if n.OTAStatus != "sent" && n.OTAStatus != "timeout" && !(n.OTAStatus == "failed" && n.OTAError == "version_unchanged") {
		return false
	}
	done := n.Firmware != n.otaFromFW
	if n.OTATarget != "" {
		done = n.Firmware == n.OTATarget
	}
	if done {
		n.OTAStatus, n.OTAAt, n.OTAError = "ok", now.UTC().Format(time.RFC3339), ""
		return true
	}
	if n.OTAStatus == "sent" && (rebooted || modern) {
		n.OTAStatus, n.OTAAt, n.OTAError = "failed", now.UTC().Format(time.RFC3339), "version_unchanged"
		return true
	}
	return false
}
