package statestore

import "os"

// Footprint returns current on-disk SQLite database and WAL sizes.
func (db *DB) Footprint() (data, wal int64, err error) {
	info, err := os.Stat(db.file)
	if err != nil {
		return 0, 0, err
	}
	data = info.Size()
	info, err = os.Stat(db.file + "-wal")
	if os.IsNotExist(err) {
		return data, 0, nil
	}
	if err != nil {
		return 0, 0, err
	}
	return data, info.Size(), nil
}
