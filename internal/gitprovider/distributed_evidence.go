package gitprovider

import (
	"errors"
	"mcpshka/internal/distributedci"
	"os"
	"strings"
	"syscall"
)

// Root preparation binds only the existing service-controlled token files.
// No token enters the report, command arguments or journal.
func DistributedEvidenceProviders(repo string) []distributedci.Provider {
	if os.Geteuid() != 0 {
		return nil
	}
	out := DistributedProviders(repo)
	for _, p := range out {
		p.(*CIProvider).Client.token = func(site Site) (string, error) {
			path := "/var/lib/mcpshka/" + string(site) + "/api_token"
			if site == SourceCraft {
				path = "/var/lib/mcpshka/sourcecraft/token"
			}
			fd, e := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
			if e != nil {
				return "", errors.New("CI credential unavailable")
			}
			f := os.NewFile(uintptr(fd), path)
			defer f.Close()
			var st syscall.Stat_t
			if syscall.Fstat(fd, &st) != nil || st.Mode&syscall.S_IFMT != syscall.S_IFREG || st.Mode&0077 != 0 || st.Nlink != 1 || st.Size < 1 || st.Size > 4096 || (st.Uid != 0 && st.Uid != 994) {
				return "", errors.New("unsafe CI credential")
			}
			b := make([]byte, st.Size)
			if _, e = f.Read(b); e != nil {
				return "", errors.New("CI credential unreadable")
			}
			token := strings.TrimSpace(string(b))
			if token == "" || strings.ContainsAny(token, " \r\n\t") {
				return "", errors.New("invalid CI credential")
			}
			return token, nil
		}
	}
	return out
}
