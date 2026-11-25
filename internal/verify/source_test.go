package verify

import (
	"strings"
	"testing"
)

func TestProductionSourceRequiresActualPairedImages(t *testing.T) {
	p := SourceProof{Schema: "mcpshka.production-source/v1", DeploymentID: "1793950000000-123456789abc", Version: "0.8.2", CIRun: 42,
		SourceSHA: strings.Repeat("1", 40), Tree: strings.Repeat("2", 40), SourceDigest: strings.Repeat("3", 64),
		ControllerSHA256: strings.Repeat("4", 64), BrokerSHA256: strings.Repeat("5", 64), CIReceiptSHA256: strings.Repeat("6", 64)}
	check := func(p SourceProof, version, tree, controller, brokerVersion, broker string) error {
		return BindSource(p, "1793950000000-123456789abc", version, tree, controller, brokerVersion, broker)
	}
	if err := check(p, p.Version, p.Tree, p.ControllerSHA256, p.Version, p.BrokerSHA256); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"controller", "broker", "broker_version", "version", "tree", "ci", "source", "deployment"} {
		t.Run(field, func(t *testing.T) {
			other := p
			version, tree, controller, brokerVersion, broker := p.Version, p.Tree, p.ControllerSHA256, p.Version, p.BrokerSHA256
			switch field {
			case "controller":
				controller = strings.Repeat("0", 64)
			case "broker":
				broker = strings.Repeat("0", 64)
			case "broker_version":
				brokerVersion = "0.8.1"
			case "version":
				version = "0.8.1"
			case "tree":
				tree = strings.Repeat("0", 40)
			case "ci":
				other.CIRun = 0
			case "source":
				other.SourceSHA = "unknown"
			case "deployment":
				other.DeploymentID = "another"
			}
			if check(other, version, tree, controller, brokerVersion, broker) == nil {
				t.Fatal("unproven source accepted")
			}
		})
	}
}
