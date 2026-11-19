// Command client drives the gateway over MCP for the end-to-end test.
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

var endpoint = envOr("E2E_ENDPOINT", "http://127.0.0.1:8081/mcp")
var instance = envOr("E2E_INSTANCE", "store-us")

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

type rpcResponse struct {
	ID     int             `json:"id"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type toolResult struct {
	IsError           bool            `json:"isError"`
	StructuredContent json.RawMessage `json:"structuredContent"`
	Content           []struct {
		Text string `json:"text"`
	} `json:"content"`
}

var session string
var nextID = 1

func post(body map[string]any) (*toolResult, error) {
	raw, _ := json.Marshal(body)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if session != "" {
		req.Header.Set("Mcp-Session-Id", session)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if id := resp.Header.Get("Mcp-Session-Id"); id != "" {
		session = id
	}
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusNoContent {
		return &toolResult{}, nil
	}
	reader := bufio.NewReader(resp.Body)
	var payload []byte
	for {
		line, err := reader.ReadString('\n')
		if strings.HasPrefix(line, "data: ") {
			payload = []byte(strings.TrimSpace(strings.TrimPrefix(line, "data: ")))
		}
		if err != nil {
			break
		}
	}
	if len(payload) == 0 {
		body, _ := io.ReadAll(reader)
		if len(body) > 0 {
			payload = body
		}
	}
	var decoded rpcResponse
	if err := json.Unmarshal(payload, &decoded); err != nil {
		return nil, fmt.Errorf("decode rpc: %w (%s)", err, string(payload))
	}
	if len(decoded.Error) > 0 && string(decoded.Error) != "null" {
		return nil, fmt.Errorf("rpc error: %s", string(decoded.Error))
	}
	var result toolResult
	if err := json.Unmarshal(decoded.Result, &result); err != nil {
		return nil, fmt.Errorf("decode result: %w (%s)", err, string(decoded.Result))
	}
	return &result, nil
}

func initialize() error {
	if _, err := post(map[string]any{"jsonrpc": "2.0", "id": nextID, "method": "initialize",
		"params": map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{},
			"clientInfo": map[string]any{"name": "e2e", "version": "1"}}}); err != nil {
		return err
	}
	nextID++
	_, err := post(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"})
	return err
}

func callTool(name string, args map[string]any) (*toolResult, error) {
	nextID++
	return post(map[string]any{"jsonrpc": "2.0", "id": nextID, "method": "tools/call",
		"params": map[string]any{"name": name, "arguments": args}})
}

func errorDetails(result *toolResult) (string, map[string]any) {
	var structured struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	}
	_ = json.Unmarshal(result.StructuredContent, &structured)
	if structured.Details == nil {
		structured.Details = map[string]any{}
	}
	return structured.Code, structured.Details
}

func productArgs() map[string]any {
	return map[string]any{
		"instance": instance, "sku": "E2E-1", "name": "E2E Blue Widget", "attribute_set_id": 4,
		"type_id": "simple", "price": "19.99", "status": 1, "visibility": 4,
		"website_ids": []int{1}, "category_ids": []int{2},
		"description":       strings.Repeat("Unique SEO copy for the E2E widget. ", 10),
		"short_description": "A short benefit statement.",
		"url_key":           "e2e-blue-widget", "meta_title": "E2E Blue Widget | Example Store",
		"meta_description": strings.Repeat("a", 140),
		"weight":           "1.25", "special_price": "17.99", "special_from_date": "2026-11-01",
		"special_to_date": "2026-11-30", "cost": "8.00", "tax_class_id": 2,
	}
}

func importCSV() string {
	return "sku,name,type_id,price,attribute_set_id,description,url_key,meta_title,meta_description\n" +
		"E2E-2,Imported Widget,simple,12.50,4," +
		strings.Repeat("copy ", 100) + ",imported-widget,Imported Widget | Example Store," + strings.Repeat("b", 140) + "\n"
}

func main() {
	if err := initialize(); err != nil {
		fmt.Println("INIT FAIL:", err)
		os.Exit(1)
	}
	mode := "create"
	if len(os.Args) > 1 {
		mode = os.Args[1]
	}
	switch mode {
	case "tools":
		nextID++
		res, err := post(map[string]any{"jsonrpc": "2.0", "id": nextID, "method": "tools/list"})
		_ = res
		if err != nil {
			fmt.Println("TOOLS FAIL:", err)
			os.Exit(1)
		}
		fmt.Println("TOOLS OK")
	case "create":
		result, err := callTool("magento.product.create", productArgs())
		if err != nil {
			fmt.Println("CREATE FAIL:", err)
			os.Exit(1)
		}
		code, details := errorDetails(result)
		if code == "confirmation_required" {
			fmt.Println("CONFIRMATION", details["confirmation_id"])
			return
		}
		fmt.Println("UNEXPECTED", code, string(result.StructuredContent))
	case "create-retry":
		args := productArgs()
		args["confirmation_id"] = os.Args[2]
		result, err := callTool("magento.product.create", args)
		if err != nil || result.IsError {
			code, _ := errorDetails(result)
			fmt.Println("CREATE RETRY FAIL:", err, code, string(result.StructuredContent))
			os.Exit(1)
		}
		fmt.Println("CREATED", string(result.StructuredContent))
	case "seo":
		result, err := callTool("magento.product.seo", map[string]any{"instance": instance, "sku": "E2E-1"})
		if err != nil || result.IsError {
			fmt.Println("SEO FAIL:", err, string(result.StructuredContent))
			os.Exit(1)
		}
		var out struct {
			Audit struct {
				Score int `json:"score"`
			} `json:"audit"`
		}
		_ = json.Unmarshal(result.StructuredContent, &out)
		fmt.Println("SEO SCORE", out.Audit.Score)
	case "validate":
		result, err := callTool("magento.import.validate", map[string]any{
			"instance": instance, "format": "csv",
			"data_base64": base64.StdEncoding.EncodeToString([]byte("sku,name\nE2E-3,No SEO Row\n")),
		})
		if err != nil || result.IsError {
			fmt.Println("VALIDATE FAIL:", err, string(result.StructuredContent))
			os.Exit(1)
		}
		var out struct {
			Preview struct {
				Valid    int `json:"valid"`
				Warnings []struct {
					Field string `json:"field"`
				} `json:"warnings"`
			} `json:"preview"`
		}
		_ = json.Unmarshal(result.StructuredContent, &out)
		fmt.Printf("VALIDATE valid=%d warnings=%d\n", out.Preview.Valid, len(out.Preview.Warnings))
	case "import-start":
		result, err := callTool("magento.import.start", map[string]any{
			"instance": instance, "format": "csv",
			"data_base64": base64.StdEncoding.EncodeToString([]byte(importCSV())),
		})
		if err != nil {
			fmt.Println("IMPORT START FAIL:", err)
			os.Exit(1)
		}
		code, details := errorDetails(result)
		if code == "confirmation_required" {
			fmt.Println("CONFIRMATION", details["confirmation_id"])
			return
		}
		fmt.Println("UNEXPECTED", code, string(result.StructuredContent))
	case "import-retry":
		result, err := callTool("magento.import.start", map[string]any{
			"instance": instance, "format": "csv",
			"data_base64":     base64.StdEncoding.EncodeToString([]byte(importCSV())),
			"confirmation_id": os.Args[2],
		})
		if err != nil || result.IsError {
			fmt.Println("IMPORT RETRY FAIL:", err, string(result.StructuredContent))
			os.Exit(1)
		}
		var out struct {
			JobID string `json:"job_id"`
		}
		_ = json.Unmarshal(result.StructuredContent, &out)
		fmt.Println("JOB", out.JobID)
	case "import-status":
		jobID := os.Args[2]
		deadline := time.Now().Add(90 * time.Second)
		for time.Now().Before(deadline) {
			result, err := callTool("magento.import.status", map[string]any{"job_id": jobID})
			if err != nil || result.IsError {
				fmt.Println("STATUS FAIL:", err, string(result.StructuredContent))
				os.Exit(1)
			}
			var out struct {
				Job struct {
					State  string `json:"state"`
					Result string `json:"result"`
				} `json:"job"`
			}
			_ = json.Unmarshal(result.StructuredContent, &out)
			if out.Job.State == "succeeded" || out.Job.State == "failed" {
				fmt.Printf("JOB %s result=%s\n", out.Job.State, out.Job.Result)
				return
			}
			time.Sleep(2 * time.Second)
		}
		fmt.Println("STATUS TIMEOUT")
		os.Exit(1)
	}
}
