package hostconfig

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func faceIDFixture(extra string) string {
	route := "@allowed path /approval/* /oauth/* /mcp /.well-known/oauth-authorization-server"
	return "http://:8798 {\n    bind 172.17.0.1\n    " + route + "\n    reverse_proxy 127.0.0.1:8799 {\n        header_up Host h96-host.example.ts.net\n        header_up X-Forwarded-Proto https\n    }\n}\n" +
		"h96-host.example.ts.net {\n    bind 192.168.77.10\n    " + route + "\n    reverse_proxy 127.0.0.1:8799 {\n        header_up Host h96-host.example.ts.net\n        header_up X-Forwarded-Proto https\n    }\n" + extra + "\n}\n"
}

func TestMCP247248249CaddyInspectReturnsOnlySafeStructuredFacts(t *testing.T) {
	dir := t.TempDir()
	p, main := filepath.Join(dir, "mcpshka-faceid.caddy"), filepath.Join(dir, "Caddyfile")
	secret := "PRIVATE-CADDY-SECRET-s3cr3t"
	content := faceIDFixture("    basicauth /oauth/user " + secret)
	if err := os.WriteFile(p, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(main, []byte("import /etc/caddy/mcpshka-faceid.caddy\n"), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := inspect(p, main)
	if err != nil || report.State != "OBSERVED" || report.Imported != "YES" ||
		!report.RoutesMatch || !report.LANConfigPresent || len(report.Sites) != 2 {
		t.Fatalf("missing caddy diagnostics: %+v, %v", report, err)
	}
	if report.Size != int64(len(content)) || len(report.SHA256) != 64 {
		t.Fatal("metadata/hash missing", report.Size, report.SHA256)
	}
	if report.Sites[0].Bind != "172.17.0.1" || report.Sites[1].Bind != "192.168.77.10" ||
		report.Sites[1].Upstream != "127.0.0.1:8799" ||
		report.Sites[1].ForwardedProto != "https" {
		t.Fatalf("RDC inventory mismatch: %+v", report.Sites)
	}
	out := report.SHA256 + report.Source + report.Imported
	for _, site := range report.Sites {
		out += site.Name + site.Bind + site.Upstream + site.ForwardedHost + site.ForwardedProto +
			strings.Join(site.AllowedPaths, ",")
	}
	if strings.Contains(out, secret) || strings.Contains(out, "basicauth") {
		t.Fatal("raw secret leaked")
	}
}

func TestMCP247249RejectsSymlinkOversizeAndUnknownDirectives(t *testing.T) {
	dir := t.TempDir()
	p, main := filepath.Join(dir, "faceid"), filepath.Join(dir, "main")
	if err := os.WriteFile(main, []byte("no imports"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/shadow", p); err != nil {
		t.Fatal(err)
	}
	if _, err := inspect(p, main); err == nil || !strings.Contains(err.Error(), "UNAVAILABLE") {
		t.Fatal("symlink admitted", err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(strings.Repeat("x", (128<<10)+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspect(p, main); err == nil {
		t.Fatal("oversize admitted")
	}
	if err := os.WriteFile(p, []byte(faceIDFixture("    @allowed path /oauth/SECRET-PRIVATE\n    bind 1.2.3.4")), 0600); err != nil {
		t.Fatal(err)
	}
	report, err := inspect(p, main)
	if err != nil || report.LANConfigPresent || report.RoutesMatch || report.Imported != "NO" {
		t.Fatal("invalid router scope admitted", report, err)
	}
	if report.Sites[1].Bind != "UNEXPECTED_REDACTED" || report.Sites[1].UnknownRoutes != 1 {
		t.Fatal("unexpected value leaked or not classified", report)
	}
}

func TestMCP249CaddyImportRequiresActiveExactDirective(t *testing.T) {
	dir := t.TempDir()
	snippet := filepath.Join(dir, "faceid")
	main := filepath.Join(dir, "Caddyfile")
	if err := os.WriteFile(snippet, []byte(faceIDFixture("")), 0600); err != nil {
		t.Fatal(err)
	}

	for _, input := range []string{
		"# import /etc/caddy/mcpshka-faceid.caddy\n",
		"import /etc/caddy/mcpshka-faceid.caddy.backup\n",
		"respond \"import /etc/caddy/mcpshka-faceid.caddy\"\n",
	} {
		if err := os.WriteFile(main, []byte(input), 0600); err != nil {
			t.Fatal(err)
		}
		report, err := inspect(snippet, main)
		if err != nil || report.Imported != "NO" {
			t.Fatalf("inactive import reported for %q: %s, %v", input, report.Imported, err)
		}
	}
}

func TestMCP247CaddyRefusesFIFOWithoutBlocking(t *testing.T) {
	path := filepath.Join(t.TempDir(), "faceid")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := readFixedNoFollow(path); err == nil || err.Error() != "HOST_CONFIG_UNSAFE_OR_OVERSIZE" {
		t.Fatalf("FIFO must be rejected as a non-regular file: %v", err)
	}
}
