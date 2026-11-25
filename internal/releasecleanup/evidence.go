package releasecleanup

type Build struct{ Permit, Commit, Tree, Image, Broker string }

// A sealed build with no installation proof is still a future release. Keep
// every test of its commit, including evidence sealed before release_local.
func ProtectedPermits(builds []Build, manifests map[string]string, installed map[string]bool, images [2]string) map[string]bool {
	keep, commits := map[string]bool{}, map[string]bool{}
	for _, b := range builds {
		if b.Image == "" || !installed[b.Image] || b.Image == images[0] || b.Image == images[1] {
			keep[b.Permit], commits[b.Commit] = true, true
		}
	}
	for id, commit := range manifests {
		if commits[commit] {
			keep[id] = true
		}
	}
	return keep
}
