package roadmaprun

import (
	"strings"
	"testing"
)

func TestGoalRoundTripAndBrokerLimit(t *testing.T) {
	for _, goal := range []string{"исправь задачу\nбез ROADMAP", strings.Repeat("x", MaxGoalBytes)} {
		encoded, err := EncodeGoal(goal)
		if err != nil || len(encoded) > 4096 {
			t.Fatalf("goal encoding: %d %v", len(encoded), err)
		}
		decoded, err := DecodeGoal(encoded)
		if err != nil || decoded != goal {
			t.Fatal("goal changed", err)
		}
	}
	for _, goal := range []string{"", " \n", "x\x00y", string([]byte{0xff}), strings.Repeat("x", MaxGoalBytes+1)} {
		if _, err := EncodeGoal(goal); err == nil {
			t.Fatal("invalid goal accepted")
		}
	}
	for _, encoded := range []string{"!bad", strings.Repeat("x", 4097), ""} {
		if _, err := DecodeGoal(encoded); err == nil {
			t.Fatal("invalid encoded goal accepted")
		}
	}
}
