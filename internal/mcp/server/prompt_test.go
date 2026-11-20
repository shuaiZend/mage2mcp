// SPDX-License-Identifier: Apache-2.0

package server

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestWriteSafetyPromptIsListed(t *testing.T) {
	srv, _ := newTestServer(t)
	ts := httptest.NewServer(Handler(srv, Options{Registry: mustRegistry(t)}))
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "prompt-client", Version: "0"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = session.Close() }()

	list, err := session.ListPrompts(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, prompt := range list.Prompts {
		if prompt.Name == writeSafetyPromptName {
			found = true
		}
	}
	if !found {
		t.Fatalf("prompts = %+v", list.Prompts)
	}
	result, err := session.GetPrompt(ctx, &mcp.GetPromptParams{Name: writeSafetyPromptName})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) == 0 {
		t.Fatal("prompt has no messages")
	}
}
