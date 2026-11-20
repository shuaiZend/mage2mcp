// SPDX-License-Identifier: Apache-2.0

//go:build live

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"sort"
)

func main() {
	mode := flag.String("mode", "schemas", "schemas | read | write | all")
	outPath := flag.String("out", "", "results JSON path (default test/live/out/results-<ts>.json)")
	rawTool := flag.String("tool", "", "tool name for -mode=raw")
	rawArgs := flag.String("args", "", "JSON arguments for -mode=raw")
	flag.Parse()

	if os.Getenv("MCP_LIVE") != "1" {
		fmt.Fprintln(os.Stderr, "refusing to run: set MCP_LIVE=1 to acknowledge this drives a live store")
		os.Exit(2)
	}

	c := NewClient()
	if err := c.Initialize(); err != nil {
		fmt.Fprintf(os.Stderr, "initialize: %v\n", err)
		os.Exit(1)
	}
	tools, err := c.ListTools()
	if err != nil {
		fmt.Fprintf(os.Stderr, "tools/list: %v\n", err)
		os.Exit(1)
	}

	switch *mode {
	case "schemas":
		dumpSchemas(tools)
	case "schema":
		for _, tool := range tools {
			if tool.Name == *rawTool {
				pretty, _ := json.MarshalIndent(tool.InputSchema, "", "  ")
				fmt.Println(string(pretty))
				return
			}
		}
		fmt.Fprintf(os.Stderr, "tool %q not registered\n", *rawTool)
		os.Exit(2)
	case "raw":
		// raw calls one tool and prints its structured content, for learning a
		// response shape before writing an assertion against it.
		args := map[string]any{}
		if *rawArgs != "" {
			if err := json.Unmarshal([]byte(*rawArgs), &args); err != nil {
				fmt.Fprintf(os.Stderr, "parse -args: %v\n", err)
				os.Exit(2)
			}
		}
		res, err := c.Call(*rawTool, args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "call: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("is_error=%v code=%s\n", res.IsError, res.Code())
		pretty, _ := json.MarshalIndent(res.StructuredContent, "", "  ")
		fmt.Println(string(pretty))
		if res.IsError {
			fmt.Println("text:", res.Text())
		}
	case "read", "write", "all":
		env := NewEnv(c)
		if err := seedReadEnv(env); err != nil {
			fmt.Fprintf(os.Stderr, "seed: %v\n", err)
			os.Exit(1)
		}
		var cases []Case
		switch *mode {
		case "read":
			cases = readCases()
		case "write", "all":
			seedWriteEnv(env)
			cases = append(readCases(), writeCases()...)
		}
		results := make([]Result, 0, len(cases))
		for _, cs := range cases {
			r := Run(env, cs)
			results = append(results, r)
			fmt.Printf("%-10s %-46s %-44s %s\n", r.Status, r.Tool, r.Case, r.Detail)
		}
		summarize(results)
		// A hard failure must fail the run: a harness that always exits 0 turns a
		// red live check into a green one the moment it is wired into CI.
		hardFailures := 0
		for _, r := range results {
			if r.Status == StatusFail {
				hardFailures++
			}
		}
		out := *outPath
		if out == "" {
			out = "test/live/out/results-" + ts + ".json"
		}
		if err := os.MkdirAll(dirOf(out), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "mkdir: %v\n", err)
			os.Exit(1)
		}
		if err := writeResults(out, results); err != nil {
			fmt.Fprintf(os.Stderr, "write results: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("\nresults written to %s\n", out)
		if hardFailures > 0 {
			fmt.Fprintf(os.Stderr, "\n%d case(s) failed\n", hardFailures)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "mode %q not implemented yet\n", *mode)
		os.Exit(2)
	}
}

// dirOf returns the directory part of a path.
func dirOf(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' {
			return p[:i]
		}
	}
	return "."
}

// dumpSchemas prints each tool with its required properties, so scenarios can be
// written against the real contract instead of a guess.
func dumpSchemas(tools []Tool) {
	names := make([]string, 0, len(tools))
	byName := make(map[string]Tool, len(tools))
	for _, t := range tools {
		names = append(names, t.Name)
		byName[t.Name] = t
	}
	sort.Strings(names)
	fmt.Printf("TOTAL %d\n", len(tools))
	for _, name := range names {
		t := byName[name]
		req, _ := t.InputSchema["required"].([]any)
		required := make([]string, 0, len(req))
		for _, r := range req {
			required = append(required, fmt.Sprint(r))
		}
		props := []string{}
		if p, ok := t.InputSchema["properties"].(map[string]any); ok {
			for k := range p {
				props = append(props, k)
			}
		}
		sort.Strings(props)
		raw, _ := json.Marshal(required)
		fmt.Printf("%-46s required=%-40s props=%v\n", name, string(raw), props)
	}
}
