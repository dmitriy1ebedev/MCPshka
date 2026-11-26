package browser

import (
	"net/url"
	"os"
)

// OperatorSite keeps the GigaIDE alias on the configured workspace's saved profile.
func OperatorSite(site string) string {
	if site != "gigaide" {
		return site
	}
	u, err := url.Parse(os.Getenv("MCPSHKA_GIGAIDE_WORKSPACE_URL"))
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || !ValidSite(u.Hostname()) {
		return site
	}
	return u.Hostname()
}
