package memory

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// Due lists reminders whose time has come and marks them delivered when mark
// is set (MOTD, «Подключения» and HA push read them).
func (s *Store) Due(ctx context.Context, mark bool) ([]Record, error) {
	now := s.now().UTC()
	rows, err := s.exec(ctx, "SELECT * FROM mem WHERE reminded=0 AND remind_at IS NOT NULL AND remind_at<="+litTime(&now)+" AND valid_to IS NULL ORDER BY remind_at LIMIT 32;")
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	out := make([]Record, 0, len(rows))
	ids := []string{}
	for _, row := range rows {
		r := fromRow(row)
		out = append(out, r)
		ids = append(ids, lit(r.ID))
	}
	if mark {
		if _, err = s.exec(ctx, "UPDATE mem SET reminded=1 WHERE id IN ("+strings.Join(ids, ",")+");"); err != nil {
			return out, err
		}
	}
	return out, nil
}

// CloseReminders marks delivered every open reminder whose source starts
// with prefix (a finished release closes the stops before it).
func (s *Store) CloseReminders(ctx context.Context, prefix string) error {
	if prefix == "" {
		return errors.New("memory: empty reminder source prefix")
	}
	_, err := s.exec(ctx, "UPDATE mem SET reminded=1 WHERE kind='reminder' AND reminded=0 AND valid_to IS NULL AND substr(source,1,"+fmt.Sprint(len([]rune(prefix)))+")="+lit(prefix)+";")
	return err
}
