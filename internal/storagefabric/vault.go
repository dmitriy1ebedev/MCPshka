package storagefabric

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Vault is the root-only store of cloud credentials, one rclone config per
// provider; nothing in it crosses the broker's response boundary.
type Vault struct{ Dir string }

func (v Vault) Config(provider string) (string, error) {
	if e := supported(provider); e != nil {
		return "", e
	}
	if e := privateDir(v.Dir); e != nil {
		return "", e
	}
	p := filepath.Join(v.Dir, provider+".conf")
	if _, e := privateFile(p); e != nil {
		return "", errors.New("cloud credential unavailable")
	}
	return p, nil
}

// oauthAppValue bounds OAuth app identifiers written into rclone config lines.
var oauthAppValue = regexp.MustCompile(`^[A-Za-z0-9._~-]{8,256}$`)

// oneDriveValue bounds the OneDrive drive id and type written into config.
var oneDriveValue = regexp.MustCompile(`^[A-Za-z0-9!._-]{1,128}$`)

func (v Vault) Accept(provider, token string) error {
	if e := supported(provider); e != nil {
		return e
	}
	if e := privateDir(v.Dir); e != nil {
		return e
	}
	lock, err := providerLock(v.Dir, provider)
	if err != nil {
		return err
	}
	defer lock.Close()
	if len(token) > 3072 {
		return ErrInvalid
	}
	var t struct {
		Access    string `json:"access_token"`
		Refresh   string `json:"refresh_token"`
		Type      string `json:"token_type"`
		Expiry    string `json:"expiry"`
		ExpiresIn int64  `json:"expires_in"`
	}
	if json.Unmarshal([]byte(token), &t) != nil || t.Access == "" || t.ExpiresIn < 0 || strings.ContainsFunc(t.Access+t.Refresh+t.Type, unicode.IsControl) || strings.ContainsAny(t.Access+t.Refresh, "\\[]") {
		return ErrInvalid
	}
	// Preserve rclone's canonical OAuth field names, never caller-supplied config.
	var obj map[string]any
	if json.Unmarshal([]byte(token), &obj) != nil {
		return ErrInvalid
	}
	// A token issued to MCPshka's own OAuth app (web connect) refreshes only
	// with that app's client id and secret; they become rclone options.
	app := map[string]string{}
	for _, k := range []string{"client_id", "client_secret"} {
		if v, ok := obj[k]; ok {
			s, isString := v.(string)
			if !isString || !oauthAppValue.MatchString(s) {
				return ErrInvalid
			}
			app[k] = s
			delete(obj, k)
		}
	}
	if len(app) == 1 {
		return ErrInvalid
	}
	// OneDrive also needs the drive rclone talks to (Graph /me/drive).
	drive := map[string]string{}
	if provider == "onedrive" {
		for _, k := range []string{"drive_id", "drive_type"} {
			s, ok := obj[k].(string)
			if !ok || !oneDriveValue.MatchString(s) {
				return ErrInvalid
			}
			drive[k] = s
			delete(obj, k)
		}
	}
	// Drive scope of the owner's own app: the whole Drive or only files
	// MCPshka created; absent on tokens from older releases (drive.file).
	scope := ""
	if v, ok := obj["scope"]; ok {
		if scope, _ = v.(string); provider != "drive" || scope != "drive" && scope != "drive.file" {
			return ErrInvalid
		}
		delete(obj, "scope")
	}
	for k := range obj {
		if k != "access_token" && k != "refresh_token" && k != "token_type" && k != "expiry" && k != "expires_in" {
			return ErrInvalid
		}
	}
	raw, e := json.Marshal(obj)
	if e != nil {
		return ErrInvalid
	}
	cfg := "[" + provider + "]\ntype = " + provider + "\ntoken = " + string(raw) + "\n"
	if len(app) == 2 {
		cfg += "client_id = " + app["client_id"] + "\nclient_secret = " + app["client_secret"] + "\n"
	}
	if provider == "drive" {
		// A token from the owner's own app carries drive.file (no Google
		// verification, no 7-day Testing expiry); the scope must match it.
		if scope == "" {
			scope = "drive"
			if len(app) == 2 {
				scope = "drive.file"
			}
		}
		cfg += "scope = " + scope + "\nuse_trash = true\n"
	}
	if provider == "onedrive" {
		cfg += "drive_id = " + drive["drive_id"] + "\ndrive_type = " + drive["drive_type"] + "\n"
	}
	return atomicFile(filepath.Join(v.Dir, provider+".conf"), []byte(cfg))
}
