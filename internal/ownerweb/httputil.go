package ownerweb

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
)

func sum256(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func RandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func connSetCookie(w http.ResponseWriter, name, value string, maxAge int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/connections", MaxAge: maxAge, HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode})
}

func ReadBody(r *http.Request, v any) error {
	if r.ContentLength > connMaxBody {
		return errors.New("слишком большой запрос")
	}
	raw, err := io.ReadAll(io.LimitReader(r.Body, connMaxBody+1))
	if err != nil || len(raw) > connMaxBody {
		return errors.New("слишком большой запрос")
	}
	if json.Unmarshal(raw, v) != nil {
		return errors.New("некорректный JSON")
	}
	return nil
}
