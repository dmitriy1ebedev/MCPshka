package rootbroker

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strconv"
	"time"

	"mcpshka/internal/executor"
)

func decodeStalePermitPlan(raw string) (executor.StalePermitPlan, error) {
	var p executor.StalePermitPlan
	d := json.NewDecoder(bytes.NewReader([]byte(raw)))
	d.DisallowUnknownFields()
	if len(raw) > 16384 || d.Decode(&p) != nil || d.Decode(new(any)) != io.EOF || !executor.ValidStalePermitPlan(p) {
		return p, errors.New("invalid pinned stale permit plan")
	}
	return p, nil
}
func adminStalePermits(args []string, now time.Time) (any, error) {
	lock, session, err := adminLegacyLeaseSession(args, now)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	if args[0] == "preview-stale-permits" {
		return executor.PreviewStalePermitsAuthority(ctx)
	}
	p, err := decodeStalePermitPlan(args[3])
	if err != nil {
		return nil, err
	}
	authorization := "admin-session:" + session.ID + ":generation:" + strconv.FormatInt(session.Generation, 10) + ":owner:" + session.Owner
	return executor.ReconcileStalePermitsAuthority(ctx, p, authorization)
}
