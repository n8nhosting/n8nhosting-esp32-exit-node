// store_import.go - one-time import of the legacy users.json into SQLite.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"os"
)

// importLegacyJSON copies the users in the JSON file at path into the users
// table if that table is empty, upgrading v1.0 plaintext passwords to hashes,
// then moves the file aside as <path>.imported so it is never read again. A
// missing file, or a table that already has users, leaves everything as is.
func importLegacyJSON(db *sql.DB, path string) error {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var list []*User
	if err := json.Unmarshal(data, &list); err != nil {
		return err
	}
	migrated := false
	for _, u := range list {
		if u.PassHash == "" && u.LegacyPassword != "" {
			u.PassHash = hashPassword(u.LegacyPassword) // upgrade v1.0 plaintext
			u.LegacyPassword = ""
			migrated = true
		}
	}

	ctx := context.Background()
	imported := false
	err = inTx(ctx, db, func(c *sql.Conn) error {
		var n int
		if err := c.QueryRowContext(ctx, `SELECT COUNT(*) FROM users`).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return nil // the database already has users; leave the file alone
		}
		for _, u := range list {
			if _, err := c.ExecContext(ctx, insertUser, userArgs(u)...); err != nil {
				return err
			}
		}
		imported = true
		return nil
	})
	if err != nil || !imported {
		return err
	}

	// The .imported copy is kept for rollback, but never with v1.0 plaintext.
	dst := path + ".imported"
	if !migrated {
		return os.Rename(path, dst)
	}
	data, _ = json.MarshalIndent(list, "", "  ")
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return err
	}
	return os.Remove(path)
}
