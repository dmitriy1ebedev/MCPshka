package projectprofile

// PipelinePin is a read-only preview of one existing trusted profile pipeline.
// It carries no commands or additional permissions.
type PipelinePin struct {
	ID          string `json:"id"`
	Path        string `json:"path"`
	Pipeline    string `json:"pipeline"`
	RoadmapSHA  string `json:"roadmap_sha256"`
	Scope       string `json:"scope"`
	Revision    string `json:"revision"`
	Fingerprint string `json:"fingerprint"`
}
