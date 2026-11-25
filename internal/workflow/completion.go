package workflow

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
)

const completionPrefix = "pipelines/v1/completions/"

// The outbox precedes claim release. A crash between release and the mission
// checkpoint is therefore recoverable without relaunching a completed job.
func (e *Engine) finish(r Run) error {
	if e.OnTerminal == nil {
		return e.release(r)
	}
	key := completionPrefix + r.ID
	raw, _ := json.Marshal([]string{r.ID, r.Fingerprint, r.State})
	won, err := e.db.PutIfAbsentBounded(key, raw, completionPrefix, 256)
	if err != nil {
		return err
	}
	if !won {
		old, err := e.db.Get(key)
		if err != nil {
			return err
		}
		if string(old) != string(raw) {
			return errors.New("workflow completion identity changed: HOLD")
		}
	}
	if err := e.release(r); err != nil {
		return err
	}
	if err := e.OnTerminal(r.ID); err != nil {
		return err
	}
	ok, err := e.db.Delete(key, raw)
	if err == nil && !ok {
		return errors.New("workflow completion changed concurrently: HOLD")
	}
	return err
}
func (e *Engine) maintainCompletion() error {
	if e.OnTerminal == nil {
		return nil
	}
	cursorKey := "pipelines/v1/completion_cursor"
	previous, err := e.db.Get(cursorKey)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	keys, err := e.db.ListPage(context.Background(), completionPrefix, string(previous), 1)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		keys, err = e.db.ListPage(context.Background(), completionPrefix, "", 1)
		if err != nil || len(keys) == 0 {
			return err
		}
	}
	key := keys[0]
	var won bool
	if previous == nil {
		won, err = e.db.PutIfAbsent(cursorKey, []byte(key))
	} else {
		won, err = e.db.Replace(cursorKey, previous, []byte(key))
	}
	if err != nil {
		return err
	}
	if !won {
		return errors.New("completion maintenance changed concurrently; retry observation")
	}
	id := strings.TrimPrefix(key, completionPrefix)
	r, err := e.Get(id)
	if err != nil {
		return err
	}
	if !terminal(r) {
		return errors.New("nonterminal completion record: HOLD")
	}
	return e.finish(r)
}
