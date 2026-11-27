package agents

import (
	"strings"
	"testing"
	"time"
)

func TestMarkInterruptedOnlyForTasksTheRebootKilled(t *testing.T) {
	boot := time.Date(2026, 10, 22, 17, 16, 0, 0, time.UTC)
	killed := Task{ID: "3a192659e7736268", State: "failed", Error: "signal: killed",
		StartedAt: boot.Add(-16 * time.Minute), FinishedAt: boot.Add(-90 * time.Second)}
	if got := MarkInterrupted(killed, boot); got.State != "interrupted" || !strings.Contains(got.NextAction, "resume_of=3a192659e7736268") {
		t.Fatal("reboot-killed task not interrupted", got)
	}
	running := Task{ID: "aaaaaaaaaaaaaaaa", State: "running", StartedAt: boot.Add(-time.Hour)}
	if MarkInterrupted(running, boot).State != "interrupted" {
		t.Fatal("task left running across the reboot not interrupted")
	}
	for _, task := range []Task{
		{State: "running", StartedAt: boot.Add(time.Minute)},
		{State: "done", StartedAt: boot.Add(-time.Hour)},
		{State: "failed", Error: "exit status 1", StartedAt: boot.Add(-time.Hour), FinishedAt: boot.Add(-time.Minute)},
		{State: "failed", Error: "signal: killed", StartedAt: boot.Add(-48 * time.Hour), FinishedAt: boot.Add(-47 * time.Hour)},
	} {
		if MarkInterrupted(task, boot).State == "interrupted" {
			t.Fatal("task wrongly interrupted", task)
		}
	}
	if MarkInterrupted(running, time.Time{}).State != "running" {
		t.Fatal("unknown boot time must not change state")
	}
}

func TestResumePromptKeepsTaskAndBounds(t *testing.T) {
	prev := Task{ID: "3a192659e7736268", Agent: Claude, Path: "/root/repos/diag", StartedAt: time.Now()}
	p, err := ResumePrompt(prev, "Сделай P05", "и закоммить")
	if err != nil || !strings.Contains(p, "Сделай P05") || !strings.Contains(p, "и закоммить") || !strings.Contains(p, "git status") {
		t.Fatal(p, err)
	}
	if _, err := ResumePrompt(prev, strings.Repeat("x", 64<<10), ""); err == nil {
		t.Fatal("oversized resumed prompt accepted")
	}
}
