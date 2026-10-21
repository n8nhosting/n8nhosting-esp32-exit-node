// db.go - the platform's SQLite database: open (pure-Go modernc driver, no
// cgo) and schema migrations.
package main

import (
	"context"
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite"
)

// migrations[i] is schema version i+1. Applied in order and recorded in
// schema_version; append new versions, never edit a shipped one.
var migrations = []string{
	// v1: proxy users (formerly users.json)
	`CREATE TABLE users (
		username    TEXT PRIMARY KEY,
		pass_hash   TEXT NOT NULL DEFAULT '',
		cred_enc    TEXT NOT NULL DEFAULT '',
		enabled     INTEGER NOT NULL DEFAULT 0,
		expires_at  TEXT NOT NULL DEFAULT '',
		rate_mbps   REAL NOT NULL DEFAULT 0,
		quota_gb    REAL NOT NULL DEFAULT 0,
		usage_bytes INTEGER NOT NULL DEFAULT 0,
		usage_month TEXT NOT NULL DEFAULT '',
		created_at  TEXT NOT NULL DEFAULT '',
		note        TEXT NOT NULL DEFAULT '',
		device_mac  TEXT NOT NULL DEFAULT ''
	)`,
	// v2: operator-owned device records, keyed by MAC
	`CREATE TABLE devices (
		mac                     TEXT PRIMARY KEY,
		name                    TEXT NOT NULL DEFAULT '',
		kind                    TEXT NOT NULL DEFAULT 'shared',
		slot                    INTEGER UNIQUE,
		capacity_mbps           REAL NOT NULL DEFAULT 0,
		capacity_measured_at    TEXT NOT NULL DEFAULT '',
		capacity_active_streams INTEGER NOT NULL DEFAULT 0,
		lat                     REAL,
		lon                     REAL,
		wifi_password_enc       TEXT NOT NULL DEFAULT '',
		notes                   TEXT NOT NULL DEFAULT ''
	)`,
	// v3: the Google Maps share link the operator pasted for a board's position
	// (lat/lon are resolved from it server side; the link is kept so the
	// dashboard can open the original pin with its place name)
	`ALTER TABLE devices ADD COLUMN maps_url TEXT NOT NULL DEFAULT ''`,
	// v4: complete metrics schema in one atomic, append-only migration.
	`CREATE TABLE metrics_fleet (
	 ts INTEGER NOT NULL, step TEXT NOT NULL, nodes_online REAL NOT NULL, nodes_total INTEGER NOT NULL,
	 mux_links REAL NOT NULL, users_total INTEGER NOT NULL, users_seated INTEGER NOT NULL,
	 users_connected INTEGER NOT NULL, users_over_quota INTEGER NOT NULL, users_expired INTEGER NOT NULL,
	 bytes_up INTEGER NOT NULL, bytes_down INTEGER NOT NULL, dials_ok INTEGER NOT NULL, dials_err INTEGER NOT NULL,
	 PRIMARY KEY(step,ts));
	 CREATE TABLE metrics_node (
	 ts INTEGER NOT NULL, step TEXT NOT NULL, mac TEXT NOT NULL, online_frac REAL NOT NULL,
	 bytes_up INTEGER NOT NULL, bytes_down INTEGER NOT NULL, dials_ok INTEGER NOT NULL,
	 dials_err INTEGER NOT NULL, active_max INTEGER NOT NULL, PRIMARY KEY(step,mac,ts));
	 CREATE TABLE metrics_user (
	 ts INTEGER NOT NULL, step TEXT NOT NULL, username TEXT NOT NULL, bytes INTEGER NOT NULL,
	 PRIMARY KEY(step,username,ts));`,
	// v5: activity and alert transitions share the durable event feed.
	`CREATE TABLE events (
	 id INTEGER PRIMARY KEY AUTOINCREMENT, ts TEXT NOT NULL, kind TEXT NOT NULL,
	 severity TEXT NOT NULL DEFAULT 'info', subject_type TEXT NOT NULL, subject_id TEXT NOT NULL,
	 actor TEXT NOT NULL DEFAULT '', message TEXT NOT NULL, data TEXT NOT NULL DEFAULT '{}');
	 CREATE INDEX events_subject ON events(subject_type,subject_id,id);
	 CREATE INDEX events_kind ON events(kind,id);`,
}

// openDB opens (creating if needed) the database at path and migrates it to
// the latest schema. One connection only: SQLite has a single writer anyway,
// and our own goroutines then queue in database/sql instead of hitting
// SQLITE_BUSY.
func openDB(path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if err := migrate(db); err != nil {
		db.Close()
		return nil, err
	}
	return db, nil
}

// migrate applies all pending migrations in one transaction.
func migrate(db *sql.DB) error {
	ctx := context.Background()
	return inTx(ctx, db, func(c *sql.Conn) error {
		if _, err := c.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (version INTEGER PRIMARY KEY)`); err != nil {
			return err
		}
		var cur int
		if err := c.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_version`).Scan(&cur); err != nil {
			return err
		}
		for v := cur + 1; v <= len(migrations); v++ {
			if _, err := c.ExecContext(ctx, migrations[v-1]); err != nil {
				return fmt.Errorf("migration v%d: %w", v, err)
			}
			if _, err := c.ExecContext(ctx, `INSERT INTO schema_version (version) VALUES (?)`, v); err != nil {
				return err
			}
		}
		return nil
	})
}

// inTx runs fn on one connection inside BEGIN IMMEDIATE ... COMMIT, taking the
// write lock up front so a read-modify-write cannot interleave with another
// writer. An error or panic from fn rolls back. fn must use c only: with one
// pooled connection, any other query on db would wait for c forever.
func inTx(ctx context.Context, db *sql.DB, fn func(c *sql.Conn) error) error {
	c, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			c.ExecContext(ctx, "ROLLBACK")
		}
		c.Close()
	}()
	if _, err := c.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return err
	}
	if err := fn(c); err != nil {
		return err
	}
	if _, err := c.ExecContext(ctx, "COMMIT"); err != nil {
		return err
	}
	committed = true
	return nil
}
