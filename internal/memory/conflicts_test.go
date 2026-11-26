package memory

import (
	"reflect"
	"testing"
	"time"
)

func TestConflictsFindsSameTopicDifferentBody(t *testing.T) {
	now := time.Now()
	later := now.Add(time.Hour)

	tests := []struct {
		name   string
		input  []auditRec
		expect []Conflict
	}{
		{
			name: "same topic different bodies",
			input: []auditRec{
				{id: "1", kind: "fact", title: "Туннель MCPshka работает через Funnel", body: "тело1", from: later},
				{id: "2", kind: "fact", title: "Туннель MCPshka работает через Funnel", body: "тело2", from: now},
			},
			expect: []Conflict{{A: "1", B: "2", Title: "Туннель MCPshka работает через Funnel"}},
		},
		{
			name: "no conflict with same body",
			input: []auditRec{
				{id: "1", kind: "fact", title: "Туннель MCPshka работает через Funnel", body: "тело1", from: later},
				{id: "2", kind: "fact", title: "Туннель MCPshka работает через Funnel", body: "тело1", from: now},
			},
			expect: nil,
		},
		{
			name: "ignore lesson kind",
			input: []auditRec{
				{id: "1", kind: "lesson", title: "Туннель MCPshka работает через Funnel", body: "тело1", from: later},
				{id: "2", kind: "fact", title: "Туннель MCPshka работает через Funnel", body: "тело2", from: now},
			},
			expect: nil,
		},
		{
			name: "ignore short titles",
			input: []auditRec{
				{id: "1", kind: "fact", title: "MCPshka", body: "тело1", from: later},
				{id: "2", kind: "fact", title: "MCPshka", body: "тело2", from: now},
			},
			expect: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := conflicts(tt.input)
			if len(got) != len(tt.expect) || len(got) > 0 && !reflect.DeepEqual(got, tt.expect) {
				t.Errorf("expected %v, got %v", tt.expect, got)
			}
		})
	}
}
