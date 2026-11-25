package systemdsvc

import (
	"context"
	"strings"
	"testing"
)

func cascadeFixture(depState *string, actions *[]string) *Service {
	allowed := map[string]struct{}{"lane-hostbridge-xray.service": {}}
	return newService(allowed, nil, allowed, func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] != "show" {
			*actions = append(*actions, args[0])
			return nil, nil
		}
		unit := args[len(args)-1]
		switch {
		case strings.Contains(strings.Join(args, " "), "RequiredBy"):
			return []byte("Id=" + unit + "\nRequiredBy=lane-connect.service lane-doh.service timers.target\nBoundBy=\nConsistsOf=\n"), nil
		case unit == "lane-hostbridge-xray.service":
			return []byte("Id=" + unit + "\nActiveState=active\n"), nil
		}
		return []byte("ActiveState=" + *depState + "\n"), nil
	})
}

func TestActionRefusesCascadingStop(t *testing.T) {
	state, actions := "active", []string{}
	s := cascadeFixture(&state, &actions)
	_, err := s.Action(context.Background(), "lane-hostbridge-xray.service", "stop")
	if err == nil || !strings.Contains(err.Error(), "CASCADE_HOLD") || !strings.Contains(err.Error(), "lane-connect.service, lane-doh.service") {
		t.Fatalf("cascading stop not refused: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("systemctl stop ran: %v", actions)
	}
}

func TestActionRestartVerifiesDependents(t *testing.T) {
	state, actions := "active", []string{}
	s := cascadeFixture(&state, &actions)
	if _, err := s.Action(context.Background(), "lane-hostbridge-xray.service", "restart"); err != nil {
		t.Fatal(err)
	}
	s = cascadeFixture(&state, &actions)
	calls := 0
	inner := s.run
	s.run = func(ctx context.Context, args ...string) ([]byte, error) {
		if args[0] == "restart" {
			state = "inactive"
		}
		calls++
		return inner(ctx, args...)
	}
	state = "active"
	if _, err := s.Action(context.Background(), "lane-hostbridge-xray.service", "restart"); err == nil || !strings.Contains(err.Error(), "CASCADE_INCOMPLETE") {
		t.Fatalf("dependents left down not reported: %v", err)
	}
}
