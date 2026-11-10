package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// This file is the delay provider (see declare.go).

// delayProvider is a wait, made once per statement:
//
//	delay:
//	  select: 30s        (a Go duration, 1m30s say, or plain seconds)
//	  alias: first-wait
//	  after: new-dir     (optional: the statements it waits for)
//
// Measuring it sleeps for that long, in every mode -- reading a document
// waits as asserting it does, so its phases take the time they will. Its
// state is when it began and completed (began, completed: RFC 3339;
// began_unix, completed_unix), the duration asked for (duration, seconds)
// and how long it waited (waited_ms), for ${{first-wait.completed}} and the
// like. It can't be asserted, and isn't measured again after the asserts.
// A delay names nothing it waits for with a reference, so after: is how it
// waits for the statements before it in the run's plan, and after: on a
// later statement naming its alias is how that one waits for it.
type delayProvider struct{}

// maxDelay is the longest a delay may wait.
const maxDelay = time.Hour

func (delayProvider) checkSelect(st *statement) error {
	if len(st.path) != 1 {
		return fmt.Errorf("a delay's path is just delay")
	}
	if st.sel == nil || st.sel.kind != yScalar {
		return fmt.Errorf("a delay's select is how long it waits: a duration (30s, 1m30s) or seconds")
	}
	if strings.Contains(st.sel.str, "${{") {
		return nil
	}
	_, err := parseDelay(st.sel.str)
	return err
}

// parseDelay reads a delay's select: a Go duration, or plain seconds.
func parseDelay(s string) (time.Duration, error) {
	s = strings.TrimSpace(s)
	d, err := time.ParseDuration(s)
	if err != nil {
		n, nerr := strconv.ParseFloat(s, 64)
		if nerr != nil {
			return 0, fmt.Errorf("select %q should be a duration (30s, 1m30s) or seconds", s)
		}
		d = time.Duration(n * float64(time.Second))
	}
	if d < 0 || d > maxDelay {
		return 0, fmt.Errorf("select %q: a delay waits from 0 to %s", s, maxDelay)
	}
	return d, nil
}

func (delayProvider) checkAssert([]string, *yNode) error {
	return fmt.Errorf("a delay can only be waited for (select), not asserted")
}

func (delayProvider) resolve(ctx context.Context, e *engine, st *statement) ([]*dobj, error) {
	s, err := e.text(st.sel, "select")
	if err != nil {
		return nil, err
	}
	d, err := parseDelay(s)
	if err != nil {
		return nil, err
	}
	id := fmt.Sprintf("delay[line %d]", st.line)
	if st.alias != "" {
		id = "delay[" + st.alias + "]"
	}
	sleep := e.sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	if e.log != nil {
		fmt.Fprintf(e.log, "%-12s %s (line %d): %s\n", "waiting", id, st.line, d)
	}
	began := e.now()
	if err := sleep(ctx, d); err != nil {
		return nil, fmt.Errorf("waiting %s: %w", d, err)
	}
	done := e.now()
	state := map[string]any{
		"duration":       d.String(),
		"seconds":        d.Seconds(),
		"began":          began.UTC().Format(time.RFC3339),
		"began_unix":     began.Unix(),
		"completed":      done.UTC().Format(time.RFC3339),
		"completed_unix": done.Unix(),
		"waited_ms":      done.Sub(began).Milliseconds(),
	}
	return []*dobj{e.addObject(id, "delay", st.line, state, nil)}, nil
}

func (delayProvider) assert(context.Context, *engine, *dobj, *yNode) error {
	return fmt.Errorf("a delay can only be waited for (select), not asserted")
}

// sleepCtx waits d, or until ctx is done.
func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
