package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"
)

const version = "3"

type job struct {
	ID      string   `json:"id"`
	Argv    []string `json:"argv"`
	Timeout int      `json:"timeout_seconds"`
}

type result struct {
	JobID  string `json:"job_id"`
	Code   int    `json:"exit_code"`
	Output string `json:"output"`
	Error  string `json:"error"`
}

type runner struct {
	base, key string
	tools     toolchain
	heartbeat time.Duration
}

type statusError int

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", e) }

var runnerHTTP = &http.Client{
	CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
}

func (r runner) request(ctx context.Context, action string, body any) (io.ReadCloser, error) {
	var input io.Reader
	method := http.MethodGet
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		input, method = bytes.NewReader(raw), http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, r.base+"/runner/"+action, input)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "mcpshka-runner/"+version)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if r.key != "" {
		req.Header.Set("Authorization", "Bearer "+r.key)
	}
	resp, err := runnerHTTP.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		resp.Body.Close()
		return nil, statusError(resp.StatusCode)
	}
	return resp.Body, nil
}

func (r runner) call(ctx context.Context, action string, input, output any, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	body, err := r.request(ctx, action, input)
	if err != nil {
		return err
	}
	defer body.Close()
	if output == nil {
		_, err := io.Copy(io.Discard, io.LimitReader(body, 64<<10))
		return err
	}
	return json.NewDecoder(io.LimitReader(body, 64<<10)).Decode(output)
}

func (r runner) poll(ctx context.Context, idle time.Duration) error {
	idleSince, refused := time.Now(), time.Time{}
	delay := 5 * time.Second
	log.Printf("waiting for jobs at %s", r.base)
	for ctx.Err() == nil {
		pollCtx := ctx
		cancel := func() {}
		if idle > 0 {
			if time.Since(idleSince) >= idle {
				return nil
			}
			pollCtx, cancel = context.WithDeadline(ctx, idleSince.Add(idle))
		}
		var response struct{ Job *job }
		err := r.call(pollCtx, "poll", map[string]any{"caps": r.tools.capabilities}, &response, 70*time.Second)
		idleExpired := errors.Is(pollCtx.Err(), context.DeadlineExceeded)
		cancel()
		if idleExpired {
			return nil
		}
		if err == nil {
			delay, refused = 5*time.Second, time.Time{}
			if response.Job != nil {
				r.handle(ctx, response.Job)
				idleSince = time.Now()
			}
			continue
		}
		log.Printf("poll: %v", err)
		pause := delay
		if errors.Is(err, statusError(401)) || errors.Is(err, statusError(403)) {
			if refused.IsZero() {
				refused = time.Now()
			}
			if time.Since(refused) >= 30*time.Minute {
				return errors.New("runner key refused for 30 minutes")
			}
			pause = time.Minute
		} else {
			refused = time.Time{}
			delay = min(2*delay, 5*time.Minute)
		}
		if idle > 0 && refused.IsZero() {
			pause = min(pause, time.Until(idleSince.Add(idle)))
		}
		sleep(ctx, pause)
	}
	return ctx.Err()
}

func sleep(ctx context.Context, delay time.Duration) {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func (r runner) handle(ctx context.Context, j *job) {
	beatCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	workCtx, cancel := context.WithCancelCause(ctx)
	defer func() {
		stop()
		cancel(nil)
		<-done
	}()
	go func() {
		defer close(done)
		ticker := time.NewTicker(r.heartbeat)
		defer ticker.Stop()
		for {
			select {
			case <-beatCtx.Done():
				return
			case <-ticker.C:
				var reply struct{ Cancel bool }
				if err := r.call(beatCtx, "heartbeat", map[string]string{"job_id": j.ID}, &reply, 20*time.Second); err != nil {
					if beatCtx.Err() == nil {
						log.Printf("heartbeat: %v", err)
					}
				} else if reply.Cancel {
					cancel(errors.New("cancelled by MCPshka"))
					return
				}
			}
		}
	}()
	res := r.execute(workCtx, j)
	// Account for JSON escaping before sending to the hub's 512 KiB decoder.
	res.Error = strings.ToValidUTF8(res.Error[:min(len(res.Error), 2048)], "")
	for raw, _ := json.Marshal(res); len(raw) > 500<<10; raw, _ = json.Marshal(res) {
		res.Output = strings.ToValidUTF8(res.Output[len(res.Output)/2:], "")
	}
	for attempt := 1; attempt <= 12 && ctx.Err() == nil; attempt++ {
		err := r.call(ctx, "result", res, nil, time.Minute)
		if err == nil {
			log.Printf("job %s exit %d", j.ID, res.Code)
			return
		}
		log.Printf("job %s result: %v", j.ID, err)
		var status statusError
		if errors.As(err, &status) && status >= 400 && status < 500 && status != 429 {
			return
		}
		if attempt < 12 {
			sleep(ctx, time.Duration(min(5*attempt, 30))*time.Second)
		}
	}
}
