package policy

import "testing"

// Admission is not a sandbox; reject code-bearing interpreter entrypoints.
func TestScriptEntrypointsRequireIsolation(t *testing.T) {
	p := Default()
	cases := []struct {
		command string
		args    []string
	}{
		{"python3", []string{"script.py"}},
		{"python3.12", []string{"script.py"}},
		{"bash", []string{"build.sh"}},
		{"sh", nil},
		{"node", []string{"build.js"}},
		{"ruby", []string{"task.rb"}},
		{"env", []string{"python3", "script.py"}},
		{"busybox", []string{"sh", "script.sh"}},
	}
	for _, tc := range cases {
		d, err := p.DecideCommand(tc.command, tc.args)
		if err == nil || d.Allowed || d.Class != ClassPrivileged {
			t.Errorf("%s admitted: %+v err=%v", tc.command, d, err)
		}
	}
}
