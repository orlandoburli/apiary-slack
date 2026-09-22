package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"

	"github.com/orlandoburli/apiary-slack/internal/config"
	"github.com/orlandoburli/apiary-slack/internal/slack"
	"github.com/orlandoburli/apiary-slack/internal/slacktest"
)

// invoke drives serve through the real single-shot protocol, the way the
// daemon does: one JSON request in, one JSON response out.
func invoke(t *testing.T, cfg map[string]any, method string, payload, result any) *pluginsdk.ResponseError {
	t.Helper()
	raw, _ := json.Marshal(payload)
	req, _ := json.Marshal(pluginsdk.Request{Protocol: pluginsdk.ProtocolVersion, RequestID: "r1", Capability: pluginsdk.CapabilitySource, Method: method, Config: cfg, Payload: raw})
	var out bytes.Buffer
	if err := pluginsdk.ServeOne(context.Background(), bytes.NewReader(req), &out, serve); err != nil {
		t.Fatalf("ServeOne: %v", err)
	}
	var resp struct {
		Result json.RawMessage          `json:"result"`
		Error  *pluginsdk.ResponseError `json:"error"`
	}
	if err := json.Unmarshal(out.Bytes(), &resp); err != nil {
		t.Fatalf("response %q: %v", out.String(), err)
	}
	if resp.Error == nil && result != nil {
		if err := json.Unmarshal(resp.Result, result); err != nil {
			t.Fatal(err)
		}
	}
	return resp.Error
}

func TestConversationRoundTrip(t *testing.T) {
	fake := slacktest.New(t)
	t.Setenv(config.TokenEnv, slacktest.Token)
	cfg := map[string]any{
		"state_file":   filepath.Join(t.TempDir(), "slack.state.json"),
		"api_url":      fake.URL,
		"channels":     []any{map[string]any{"id": "C1"}},
		"ack_reaction": "eyes",
	}
	poll := func() []pluginsdk.SourceItem {
		t.Helper()
		var res pluginsdk.SourcePollResult
		if e := invoke(t, cfg, pluginsdk.SourceMethodPoll, pluginsdk.SourcePollRequest{}, &res); e != nil {
			t.Fatalf("poll: %+v", e)
		}
		return res.Items
	}
	if n := len(poll()); n != 0 {
		t.Fatalf("priming poll emitted %d", n)
	}

	// Stamp messages after the cursor the priming poll just pinned to now.
	root := slack.NowTS(time.Now().Add(time.Minute))
	fake.Add("C1", slack.Message{User: "U1", Text: "<@UBOT> status?", TS: root})
	items := poll()
	if len(items) != 1 || items[0].ID != "slack:C1:"+root || items[0].State != "pending" {
		t.Fatalf("items = %+v", items)
	}

	if e := invoke(t, cfg, pluginsdk.SourceMethodAcknowledge, pluginsdk.SourceAckRequest{Item: items[0], Action: "dispatched"}, nil); e != nil {
		t.Fatalf("ack: %+v", e)
	}
	if n := len(poll()); n != 0 {
		t.Fatalf("still pending after ack: %d items", n)
	}
	if e := invoke(t, cfg, pluginsdk.SourceMethodWriteResult, pluginsdk.SourceWriteResultRequest{Item: items[0], Success: true, Output: "All **green**."}, nil); e != nil {
		t.Fatalf("write_result: %+v", e)
	}
	if len(fake.Reactions) != 1 || fake.Reactions[0].TS != root || len(fake.Posted) != 1 || fake.Posted[0] != (slacktest.Posted{Channel: "C1", ThreadTS: root, Text: "All *green*."}) {
		t.Fatalf("reactions %+v posted %+v", fake.Reactions, fake.Posted)
	}

	// The follow-up reopens the same item.
	reply := slack.NowTS(time.Now().Add(2 * time.Minute))
	fake.Add("C1", slack.Message{User: "U1", Text: "and staging?", TS: reply, ThreadTS: root})
	items = poll()
	if len(items) != 1 || items[0].ID != "slack:C1:"+root || items[0].Metadata["ts"] != reply {
		t.Fatalf("follow-up = %+v", items)
	}

	if e := invoke(t, cfg, "close", nil, nil); e == nil || e.Code != "unsupported_method" {
		t.Errorf("unknown method: %+v", e)
	}
	t.Setenv(config.TokenEnv, "")
	if e := invoke(t, cfg, pluginsdk.SourceMethodPoll, pluginsdk.SourcePollRequest{}, nil); e == nil || e.Code != "invalid_config" {
		t.Errorf("missing token: %+v", e)
	}
}
