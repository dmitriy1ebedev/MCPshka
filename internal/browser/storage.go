package browser

import (
	"encoding/json"
	"errors"
	"net/url"
)

// StorageScript imports an owner's exported localStorage on exactly the first
// goto origin, before the application reads it. The script is removed after goto.
func (s Spec) StorageScript() (string, error) {
	if len(s.LocalStorage) == 0 {
		return "", nil
	}
	size := 0
	for k, v := range s.LocalStorage {
		size += len(k) + len(v)
		if k == "" || len(k) > 256 || len(v) > 16384 {
			return "", errors.New("local_storage key or value exceeds limit")
		}
	}
	if len(s.LocalStorage) > 32 || size > 32768 {
		return "", errors.New("local_storage exceeds limit")
	}
	for _, step := range s.Steps {
		if step.Action != "goto" {
			continue
		}
		if !OnSite(step.URL, s.Site) {
			return "", errors.New("local_storage needs an allowed goto origin")
		}
		u, err := url.Parse(step.URL)
		if err != nil {
			return "", err
		}
		origin, _ := json.Marshal(u.Scheme + "://" + u.Host)
		entries, _ := json.Marshal(s.LocalStorage)
		return "if(location.origin===" + string(origin) + "){for(const [k,v] of Object.entries(" + string(entries) + ")){localStorage.setItem(k,v)}}", nil
	}
	return "", errors.New("local_storage import needs a goto step")
}
