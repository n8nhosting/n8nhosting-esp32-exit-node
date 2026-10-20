// devices_store.go - operator-owned device records, keyed by MAC. The broker
// knows which boards exist; this table holds what the operator says about them.
package main

import (
	"context"
	"database/sql"
	"errors"
	"log"
)

// Device is one board's operator record. Kind is "shared" or "dedicated".
// Slot 0 means unassigned; Lat/Lon nil mean unset.
type Device struct {
	MAC                   string
	Name                  string
	Kind                  string
	Slot                  int
	CapacityMbps          float64 // 0 = not measured
	CapacityMeasuredAt    string
	CapacityActiveStreams int
	Lat, Lon              *float64
	WifiPasswordEnc       string
	Notes                 string
}

type DeviceStore struct{ db *sql.DB }

func NewDeviceStore(db *sql.DB) *DeviceStore { return &DeviceStore{db: db} }

const deviceCols = `mac, name, kind, slot, capacity_mbps, capacity_measured_at,
	capacity_active_streams, lat, lon, wifi_password_enc, notes`

// scanDevice reads one deviceCols row; no row is ErrNotFound. A NULL slot is 0.
func scanDevice(row interface{ Scan(...any) error }) (*Device, error) {
	d := &Device{}
	var slot sql.NullInt64
	var lat, lon sql.NullFloat64
	err := row.Scan(&d.MAC, &d.Name, &d.Kind, &slot, &d.CapacityMbps, &d.CapacityMeasuredAt,
		&d.CapacityActiveStreams, &lat, &lon, &d.WifiPasswordEnc, &d.Notes)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	d.Slot = int(slot.Int64)
	if lat.Valid {
		d.Lat = &lat.Float64
	}
	if lon.Valid {
		d.Lon = &lon.Float64
	}
	return d, nil
}

// deviceArgs lists the deviceCols values; slot 0 and unset lat/lon are NULL
// (slot is UNIQUE, so 0 must not be stored).
func deviceArgs(d *Device) []any {
	var slot any
	if d.Slot > 0 {
		slot = d.Slot
	}
	var lat, lon any
	if d.Lat != nil {
		lat = *d.Lat
	}
	if d.Lon != nil {
		lon = *d.Lon
	}
	return []any{d.MAC, d.Name, d.Kind, slot, d.CapacityMbps, d.CapacityMeasuredAt,
		d.CapacityActiveStreams, lat, lon, d.WifiPasswordEnc, d.Notes}
}

func (s *DeviceStore) Get(mac string) (*Device, error) {
	return scanDevice(s.db.QueryRow(`SELECT `+deviceCols+` FROM devices WHERE mac = ?`, mac))
}

// BySlot returns the device holding slot; none is ErrNotFound.
func (s *DeviceStore) BySlot(slot int) (*Device, error) {
	return scanDevice(s.db.QueryRow(`SELECT `+deviceCols+` FROM devices WHERE slot = ?`, slot))
}

func (s *DeviceStore) List() []*Device {
	out := []*Device{}
	rows, err := s.db.Query(`SELECT ` + deviceCols + ` FROM devices ORDER BY mac`)
	if err != nil {
		log.Printf("store: list devices: %v", err)
		return out
	}
	defer rows.Close()
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			log.Printf("store: list devices: %v", err)
			return out
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		log.Printf("store: list devices: %v", err)
	}
	return out
}

// Upsert applies fn to the device (a fresh shared record when mac has no row
// yet) and persists it in one write transaction. fn must not call back into
// the store.
func (s *DeviceStore) Upsert(mac string, fn func(*Device) error) error {
	ctx := context.Background()
	return inTx(ctx, s.db, func(c *sql.Conn) error {
		d, err := scanDevice(c.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE mac = ?`, mac))
		if errors.Is(err, ErrNotFound) {
			d = &Device{MAC: mac, Kind: "shared"}
		} else if err != nil {
			return err
		}
		if err := fn(d); err != nil {
			return err
		}
		d.MAC = mac
		_, err = c.ExecContext(ctx, `INSERT OR REPLACE INTO devices (`+deviceCols+`) VALUES (?,?,?,?,?,?,?,?,?,?,?)`, deviceArgs(d)...)
		return err
	})
}
