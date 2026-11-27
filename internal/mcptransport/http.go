package mcptransport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"mcpshka/internal/artifacts"
	"mcpshka/internal/authn"
	"mime"
	"net"
	"net/http"
	"strings"
	"time"
)

func Serve(ctx context.Context, addr string, mainHandler http.Handler, portalListener net.Listener, portalHandler http.Handler) error {
	var portalServer *http.Server
	var portalErrors chan error
	if portalListener != nil {
		portalServer = &http.Server{Handler: portalHandler, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 30 * time.Second}
		portalErrors = make(chan error, 1)
		go func() { portalErrors <- portalServer.Serve(portalListener) }()
		defer portalServer.Close()
	}

	srv := &http.Server{
		Addr:              addr,
		Handler:           MarkOrigin(mainHandler),
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       90 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case err := <-portalErrors:
		_ = srv.Close()
		if errors.Is(err, http.ErrServerClosed) {
			return errors.New("passkey portal stopped unexpectedly")
		}
		return fmt.Errorf("passkey portal: %w", err)
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if portalServer != nil {
			if err := portalServer.Shutdown(shutdownCtx); err != nil {
				return err
			}
		}
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		err := <-errCh
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	}
}

func Mux(version string, authenticator *authn.Authenticator, artifactManager *artifacts.Manager, mcpHandler http.Handler) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"status":  "ok",
			"name":    "MCPshka",
			"version": version,
		})
	})
	mux.Handle("/mcp", MeasureMCP(BearerAuth(authenticator, mcpHandler), ""))
	mux.Handle("/artifacts/", BearerAuth(authenticator, ArtifactDownloadHandler(artifactManager)))
	return mux
}

func ArtifactDownloadHandler(manager *artifacts.Manager) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		id := strings.TrimPrefix(r.URL.Path, "/artifacts/")
		if id == "" || strings.Contains(id, "/") {
			http.NotFound(w, r)
			return
		}
		principal := ""
		if identity, ok := authn.IdentityFromContext(r.Context()); ok {
			principal = identity.ID
		}
		ticket, file, err := manager.OpenFor(id, principal)
		if err != nil {
			switch {
			case errors.Is(err, artifacts.ErrExpired):
				http.Error(w, "artifact ticket expired", http.StatusGone)
			case errors.Is(err, artifacts.ErrChanged):
				http.Error(w, "artifact changed since ticket creation", http.StatusConflict)
			default:
				http.NotFound(w, r)
			}
			return
		}
		defer file.Close()

		w.Header().Set("Cache-Control", "no-store")
		if ticket.SHA256 != "" {
			// A strong ETag lets If-Range resume only the exact same bytes.
			w.Header().Set("ETag", `"sha256-`+ticket.SHA256+`"`)
			w.Header().Set("X-Content-SHA256", ticket.SHA256)
		}
		w.Header().Set("Content-Type", ticket.ContentType)
		w.Header().Set("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": ticket.Name}))
		http.ServeContent(w, r, ticket.Name, ticket.ModTime, file)
	})
}

func BearerAuth(authenticator *authn.Authenticator, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		token, ok := strings.CutPrefix(header, "Bearer ")
		if !ok || authenticator == nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		identity, err := authenticator.Authenticate(token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", "Bearer")
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r.WithContext(authn.WithIdentity(r.Context(), identity)))
	})
}

func RequireLoopbackAddr(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid MCPSHKA_HTTP_ADDR: %w", err)
	}
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("mcp-http currently requires a loopback bind address, got %q", addr)
	}
	return nil
}
