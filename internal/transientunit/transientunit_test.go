package transientunit

import "testing"

func TestSystemdRunValidation(t *testing.T) {
	id := "1791375474638-afa82bd97354"
	ok := [][]string{
		{
			"--unit=mcpshka-deploy-" + id,
			"--collect", "--no-block", "--property=Type=oneshot",
			"--property=TimeoutStartSec=120s", "--property=UMask=0077",
			"/usr/local/libexec/mcpshka-self-deploy",
			"deploy", id,
			"/root/repos/mcpshka/bin/mcpshka-0.1.a.49.candidate",
			"0.1.a.49", "0.1.a.48",
			"/var/lib/mcpshka/deploys/" + id + ".json",
		},
		{
			"--unit=mcpshka-user-migrate-" + id,
			"--collect", "--no-block", "--property=Type=oneshot",
			"--property=TimeoutStartSec=120s", "--property=UMask=0077",
			"/usr/local/libexec/mcpshka-migrate-service-user",
			"apply", id,
		},
		{
			"--unit=mcpshka-user-rollback-" + id,
			"--collect", "--no-block", "--property=Type=oneshot",
			"--property=TimeoutStartSec=120s", "--property=UMask=0077",
			"/usr/local/libexec/mcpshka-migrate-service-user",
			"rollback", id,
		},
	}
	for _, args := range ok {
		if err := ValidateRun(args); err != nil {
			t.Fatalf("accepted systemd-run rejected %v: %v", args, err)
		}
	}

	bad := [][]string{
		{"--unit=x", "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=90s", "--property=UMask=0077", "/bin/sh"},
		{"--unit=mcpshka-deploy-" + id, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=120s", "--property=UMask=0077", "/root/repos/mcpshka/packaging/native/self-deploy.sh", "deploy", id, "/root/repos/mcpshka/bin/mcpshka-0.1.a.49.candidate", "0.1.a.49", "0.1.a.48", "/var/lib/mcpshka/deploys/" + id + ".json"},
		{"--unit=mcpshka-broker-install-" + id, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=90s", "--property=UMask=0077", "/root/repos/mcpshka/packaging/native/install-broker.sh", "extra"},
		{"--unit=mcpshka-deploy-" + id, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=120s", "--property=UMask=0077", "/usr/local/libexec/mcpshka-self-deploy", "deploy", id, "/tmp/evil", "0.1.a.49", "0.1.a.48", "/var/lib/mcpshka/deploys/" + id + ".json"},
		{"--unit=mcpshka-deploy-" + id, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=120s", "--property=UMask=0077", "/usr/local/libexec/mcpshka-self-deploy", "deploy", id, "/root/repos/mcpshka/bin/mcpshka-0.1.a.49.candidate", "0.1.a.49", "0.1.a.48", "/tmp/status.json"},
		{"--unit=mcpshka-user-migrate-" + id, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=120s", "--property=UMask=0077", "/root/repos/mcpshka/packaging/native/migrate-service-user.sh", "apply", id},
		{"--unit=mcpshka-user-migrate-" + id, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=120s", "--property=UMask=0077", "/usr/local/libexec/mcpshka-migrate-service-user", "apply", "deadbeef"},
		{"--unit=mcpshka-user-migrate-" + id, "--collect", "--no-block", "--property=Type=oneshot", "--property=TimeoutStartSec=120s", "--property=UMask=0077", "/usr/local/libexec/mcpshka-migrate-service-user", "rollback", id},
	}
	for _, args := range bad {
		if err := ValidateRun(args); err == nil {
			t.Fatalf("unsafe systemd-run accepted: %v", args)
		}
	}
}
