package statestore

import (
	"errors"
	"fmt"
	"strings"
)

type Capacity struct {
	PageSize       int64   `json:"page_size"`
	PageCount      int64   `json:"page_count"`
	FreelistCount  int64   `json:"freelist_count"`
	MaxPageCount   int64   `json:"max_page_count"`
	AvailablePages int64   `json:"available_pages"`
	UsedPercent    float64 `json:"used_percent"`
	Warning        string  `json:"warning,omitempty"`
}

// Capacity reads all page counters on the connection that enforces the cap.
func (db *DB) Capacity() (Capacity, error) {
	raw, err := db.run("SELECT page_size,page_count,freelist_count,max_page_count FROM pragma_page_size(),pragma_page_count(),pragma_freelist_count(),pragma_max_page_count();")
	if err != nil {
		return Capacity{}, err
	}
	var size, pages, free, maxPages int64
	if n, err := fmt.Sscanf(strings.TrimSpace(string(raw)), "%d|%d|%d|%d", &size, &pages, &free, &maxPages); err != nil || n != 4 {
		return Capacity{}, errors.New("invalid SQLite page counters")
	}
	return pageCapacity(size, pages, free, maxPages)
}

func pageCapacity(size, pages, free, maxPages int64) (Capacity, error) {
	if size <= 0 || free < 0 || free > pages || maxPages <= 0 || maxPages > 4294967294 || pages > maxPages {
		return Capacity{}, errors.New("inconsistent SQLite page counters")
	}
	used := pages - free
	result := Capacity{
		PageSize: size, PageCount: pages, FreelistCount: free, MaxPageCount: maxPages,
		AvailablePages: maxPages - used,
		UsedPercent:    100 * float64(used) / float64(maxPages),
	}
	if used*5 > maxPages*4 {
		result.Warning = "SQLITE_CAPACITY_HIGH"
	}
	return result, nil
}

// CountWithCapacity reports the record count and limits from the active store.
func (db *DB) CountWithCapacity() (int64, Capacity, error) {
	count, err := db.Count("")
	if err != nil {
		return 0, Capacity{}, err
	}
	capacity, err := db.Capacity()
	return count, capacity, err
}
