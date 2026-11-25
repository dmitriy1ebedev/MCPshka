package statestore

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Active selects one authority. A bad marker/database never falls back to JSON.
func Active(root string) (*DB, error) {
	marker, err := os.ReadFile(filepath.Join(root, "state.mode"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil || strings.TrimSpace(string(marker)) != "sqlite-v1" {
		return nil, errors.New("invalid state authority marker")
	}
	file := filepath.Join(root, "state.db")
	if _, err := os.Lstat(file); err != nil {
		return nil, errors.New("active state database missing")
	}
	return Open(file)
}

func (db *DB) Put(key string, payload []byte) error {
	if !validKey(key) || len(payload) > maxRecord {
		return errors.New("state record rejected")
	}
	columns, values := "path,payload,sha256", text(key)+","+blob(payload)+","+text(checksum(payload))
	set := "payload=excluded.payload,sha256=excluded.sha256"
	if db.version == 2 {
		columns += ",size_bytes"
		values += "," + strconv.Itoa(len(payload))
		set += ",size_bytes=excluded.size_bytes"
	}
	_, err := db.run("INSERT INTO records (" + columns + ") VALUES (" + values + ") ON CONFLICT(path) DO UPDATE SET " + set + ";")
	return err
}
