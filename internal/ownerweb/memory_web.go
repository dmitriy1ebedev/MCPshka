package ownerweb

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"mcpshka/internal/memory"
	"mcpshka/internal/webpages"
)

// «Память»: search, write, close reminders and upload files from the phone
// in chunks small enough for the portal's 16 KiB request limit.
const (
	uploadMax   = 32 << 20
	uploadChunk = 9000
)

var uploadID = regexp.MustCompile(`^[a-f0-9]{16,32}$`)

func memoryAPI(ctx context.Context, w http.ResponseWriter, r *http.Request, rest string) {
	fail := func(err error, code int) { JSON(w, map[string]string{"error": err.Error()}, code) }
	st, err := memory.Open(MemoryDir)
	if err != nil {
		fail(err, http.StatusInternalServerError)
		return
	}
	switch {
	case rest == "" && r.Method == http.MethodGet:
		q := r.URL.Query()
		counts, _ := st.Count(ctx)
		due, _ := st.Due(ctx, false)
		out := map[string]any{"counts": counts, "due": due}
		if text := strings.TrimSpace(q.Get("q")); text != "" {
			hits, err := st.Recall(ctx, memory.Query{Text: text, Project: q.Get("project"), Limit: 20})
			if err != nil {
				fail(err, http.StatusBadRequest)
				return
			}
			out["hits"] = hits
		} else {
			recent, _ := st.Recent(ctx, 30)
			out["recent"] = recent
		}
		JSON(w, out, http.StatusOK)
	case rest == "write" && r.Method == http.MethodPost:
		var rec memory.Record
		if err := ReadBody(r, &rec); err != nil {
			fail(err, http.StatusBadRequest)
			return
		}
		rec.Source = "owner web"
		out, changed, err := st.Write(ctx, rec)
		if err != nil {
			fail(err, http.StatusBadRequest)
			return
		}
		JSON(w, map[string]any{"record": out, "changed": changed}, http.StatusOK)
	case rest == "upload" && r.Method == http.MethodPost:
		var in struct {
			ID    string `json:"id"`
			Seq   int    `json:"seq"`
			Total int    `json:"total"`
			Name  string `json:"name"`
			Data  string `json:"data"`
		}
		if err := ReadBody(r, &in); err != nil {
			fail(err, http.StatusBadRequest)
			return
		}
		out, err := memoryUpload(ctx, st, in.ID, in.Seq, in.Total, in.Name, in.Data)
		if err != nil {
			fail(err, http.StatusBadRequest)
			return
		}
		JSON(w, out, http.StatusOK)
	default:
		http.NotFound(w, r)
	}
}

// memoryUpload appends chunk seq of total to the upload; the last chunk
// moves the file to memory-inbox and imports .md/.txt at once. Other files
// (chat exports) wait in the inbox for an AI to distil them.
func memoryUpload(ctx context.Context, st *memory.Store, id string, seq, total int, name, data string) (map[string]any, error) {
	if !uploadID.MatchString(id) || total < 1 || total > uploadMax/uploadChunk+1 || seq < 0 || seq >= total {
		return nil, errors.New("неверный кусок загрузки")
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil || len(raw) > uploadChunk {
		return nil, errors.New("кусок не base64 или больше 9000 байт")
	}
	inbox := filepath.Join(MemoryDir, "memory-inbox")
	if err = os.MkdirAll(inbox, 0o700); err != nil {
		return nil, err
	}
	part := filepath.Join(inbox, id+".part")
	info, err := os.Stat(part)
	switch {
	case seq == 0:
		err = os.WriteFile(part, raw, 0o600)
	case err != nil || info.Size() != int64(seq)*uploadChunk:
		// The phone resends a lost chunk; a gap restarts the upload.
		return nil, fmt.Errorf("ожидался другой кусок; начните загрузку заново")
	default:
		var f *os.File
		if f, err = os.OpenFile(part, os.O_WRONLY|os.O_APPEND, 0o600); err == nil {
			_, err = f.Write(raw)
			err = errors.Join(err, f.Close())
		}
	}
	if err != nil || seq < total-1 {
		return map[string]any{"received": seq + 1, "total": total}, err
	}
	base := strings.Map(func(r rune) rune {
		if r == '/' || r == '\\' || r < 32 {
			return '_'
		}
		return r
	}, filepath.Base(name))
	if base == "" || base == "." || base == ".." {
		base = "upload"
	}
	final := filepath.Join(inbox, time.Now().UTC().Format("20060102T150405Z")+"-"+base)
	if err = os.Rename(part, final); err != nil {
		return nil, err
	}
	out := map[string]any{"saved": final}
	if ext := strings.ToLower(filepath.Ext(base)); ext == ".md" || ext == ".txt" {
		tmp, err := os.MkdirTemp(inbox, "import-")
		if err != nil {
			return out, err
		}
		defer os.RemoveAll(tmp)
		if err = copyFile(final, filepath.Join(tmp, strings.TrimSuffix(base, ext)+".md")); err != nil {
			return out, err
		}
		rep, err := st.ImportMarkdown(ctx, tmp, "")
		out["import"] = rep
		return out, err
	}
	if ext := strings.ToLower(filepath.Ext(base)); ext == ".json" || ext == ".zip" {
		rep, err := st.InboxScan(ctx)
		out["inbox"] = rep
		if err == nil {
			out["next"] = "архив чата нарезан: попросите ИИ «выжми архив чатов в память» (memory_recall inbox=true)"
		}
		return out, err
	}
	out["next"] = "файл ждёт разбора: попросите ИИ «разбери загрузку в память»"
	return out, nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(to, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	return errors.Join(err, out.Close())
}

func memoryPage(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	nonce := RandomHex(16)
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = io.WriteString(w, strings.Replace(webpages.Memory, "<script>", "<script nonce=\""+nonce+"\">", 1))
}
