package agents

import (
	"mcpshka/internal/gitops"
)

// VerifyCompletion requires a new commit on the task branch and exact-HEAD test evidence.
func VerifyCompletion(t Task, git *gitops.Service, tested func(string, string) (bool, string)) Task {
	if t.State != "done" {
		return t
	}
	hold := func(reason string) Task { t.State = "done_unverified"; t.NextAction = reason; return t }
	if t.Journal == nil || t.Journal.HeadBefore == "" || t.Journal.HeadAfter == "" || t.Journal.Branch == "" {
		return hold("журнал завершения неполон")
	}
	j := t.Journal
	if j.HeadBefore == j.HeadAfter {
		return hold("нет коммитов после head_before")
	}
	if git == nil {
		return hold("доказательства Git недоступны")
	}
	st, err := git.Status(t.Path, 4096)
	if err != nil || st.Dirty || st.Truncated || st.Branch != j.Branch {
		return hold("ветка задачи изменилась, содержит правки или не проверена")
	}
	head, err := git.Revision(t.Path)
	if err != nil || head != j.HeadAfter {
		return hold("HEAD отличается от результата задачи")
	}
	ancestor, err := git.IsAncestor(t.Path, j.HeadBefore)
	if err != nil || !ancestor {
		return hold("head_before не является предком HEAD задачи")
	}
	if tested == nil {
		return hold("доказательства project_test недоступны")
	}
	pass, job := tested(st.Root, head)
	if !pass || job == "" {
		return hold("нет PASS project_test для HEAD задачи")
	}
	t.NextAction = "завершение подтверждено project_test job:" + job + "; интеграция и выпуск требуют отдельной проверки"
	return t
}
