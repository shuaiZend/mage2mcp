// SPDX-License-Identifier: Apache-2.0

//go:build live

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"time"
)

// Status is the outcome of one case.
type Status string

// Case outcomes.
const (
	StatusPass    Status = "pass"
	StatusFail    Status = "fail"
	StatusSkipped Status = "skipped"
	StatusSoft    Status = "soft-fail"
)

// Result is one recorded case.
type Result struct {
	Group     string `json:"group"`
	Tool      string `json:"tool"`
	Case      string `json:"case"`
	Status    Status `json:"status"`
	Code      string `json:"code,omitempty"`
	LatencyMS int64  `json:"latency_ms"`
	Detail    string `json:"detail,omitempty"`
}

// Env carries state produced by earlier cases (created SKUs, ids, tokens).
type Env struct {
	values map[string]string
	client *Client
}

// NewEnv returns an empty environment bound to the client.
func NewEnv(c *Client) *Env { return &Env{values: map[string]string{}, client: c} }

// Set records a value for later cases.
func (e *Env) Set(k, v string) { e.values[k] = v }

// Get returns a recorded value.
func (e *Env) Get(k string) string { return e.values[k] }

// Int returns a recorded value parsed as an integer, or 0.
func (e *Env) Int(k string) int {
	n, _ := strconv.Atoi(e.values[k])
	return n
}

// Need returns an error when a required value was not produced.
func (e *Env) Need(keys ...string) error {
	for _, k := range keys {
		if e.values[k] == "" {
			return fmt.Errorf("missing prerequisite %q", k)
		}
	}
	return nil
}

// Case is one tool invocation with an assertion.
type Case struct {
	Group string
	Tool  string
	Case  string
	// Args builds the arguments; it may depend on values recorded earlier.
	Args func(e *Env) (map[string]any, error)
	// Check validates a successful result. nil accepts any non-error result.
	Check func(e *Env, res *ToolResult) error
	// WantCode expects a tool error with this taxonomy code.
	WantCode string
	// Need names values that must exist before the case runs.
	Need []string
	// Soft marks a case whose failure is expected to be environmental (for
	// example a demo store that does not support a feature). It is reported,
	// never silently dropped.
	Soft bool
}

// Run executes one case and records the outcome.
func Run(e *Env, c Case) Result {
	r := Result{Group: c.Group, Tool: c.Tool, Case: c.Case}
	if err := e.Need(c.Need...); err != nil {
		r.Status = StatusSkipped
		r.Detail = err.Error()
		return r
	}
	args := map[string]any{}
	if c.Args != nil {
		built, err := c.Args(e)
		if err != nil {
			r.Status = StatusSkipped
			r.Detail = err.Error()
			return r
		}
		args = built
	}
	// Every data-source tool routes by instance; only platform.info does not.
	// Defaulting it here keeps the scenario table focused on the payload.
	if c.Tool != "platform.info" {
		if _, ok := args["instance"]; !ok {
			args["instance"] = instance()
		}
	}
	start := time.Now()
	res, err := e.client.Call(c.Tool, args)
	r.LatencyMS = time.Since(start).Milliseconds()
	switch {
	case err != nil:
		r.Status = StatusFail
		r.Detail = err.Error()
	case c.WantCode != "":
		if res.Code() == c.WantCode {
			r.Status = StatusPass
			r.Code = res.Code()
			return r
		}
		r.Status = StatusFail
		r.Code = res.Code()
		r.Detail = fmt.Sprintf("want error %q, got code %q: %s", c.WantCode, res.Code(), truncate(res.Text(), 300))
	case res.IsError:
		r.Status = StatusFail
		r.Code = res.Code()
		r.Detail = truncate(res.Text(), 400)
	default:
		if c.Check != nil {
			if err := c.Check(e, res); err != nil {
				r.Status = StatusFail
				r.Detail = err.Error()
				return r
			}
		}
		r.Status = StatusPass
	}
	if r.Status == StatusFail && c.Soft {
		r.Status = StatusSoft
	}
	return r
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// output returns the decoded structured content of a successful result.
func output(res *ToolResult) (map[string]any, error) {
	raw, err := json.Marshal(res.StructuredContent)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode structured content: %w", err)
	}
	return out, nil
}

// num reads a numeric field from a decoded output.
func num(out map[string]any, key string) (float64, bool) {
	v, ok := out[key]
	if !ok {
		return 0, false
	}
	f, ok := v.(float64)
	return f, ok
}

// str reads a string field from a decoded output.
func str(out map[string]any, key string) string {
	if v, ok := out[key].(string); ok {
		return v
	}
	return ""
}

// writeResults persists the run as JSON next to the report.
func writeResults(path string, results []Result) error {
	raw, err := json.MarshalIndent(results, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

// summarize prints a per-group tally.
func summarize(results []Result) {
	type tally struct{ pass, fail, soft, skipped int }
	byGroup := map[string]*tally{}
	order := []string{}
	for _, r := range results {
		t, ok := byGroup[r.Group]
		if !ok {
			t = &tally{}
			byGroup[r.Group] = t
			order = append(order, r.Group)
		}
		switch r.Status {
		case StatusPass:
			t.pass++
		case StatusFail:
			t.fail++
		case StatusSoft:
			t.soft++
		case StatusSkipped:
			t.skipped++
		}
	}
	sort.Strings(order)
	fmt.Printf("\n%-22s %5s %5s %6s %8s\n", "GROUP", "PASS", "FAIL", "SOFT", "SKIPPED")
	for _, g := range order {
		t := byGroup[g]
		fmt.Printf("%-22s %5d %5d %6d %8d\n", g, t.pass, t.fail, t.soft, t.skipped)
	}
}

// base64Encode is the encoding the import and media tools expect.
func base64Encode(s string) string {
	return base64.StdEncoding.EncodeToString([]byte(s))
}
