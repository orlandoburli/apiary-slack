// Command apiary-plugin-slack is an Apiary protocol-1 source plugin that turns
// Slack conversations into work items and posts the agents' answers back.
//
// poll reads new messages from the configured channels and DMs through the
// Slack Web API — outbound calls only, no Socket Mode, no Events API, no
// listener — and emits one work item per conversation that is waiting for an
// answer. The item is the thread (or the DM), so every turn lands on the same
// Apiary task; its state is `pending` until acknowledge records the dispatch,
// and its description carries the thread so far. write_result posts what the
// workflow publishes as a reply in the same thread.
//
// The Slack workflow's trigger must match `states: [pending]` and must NOT be
// `once: true`: the same item is dispatched again for every turn.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	pluginsdk "github.com/orlandoburli/apiary/sdk/plugin"

	"github.com/orlandoburli/apiary-slack/internal/config"
	"github.com/orlandoburli/apiary-slack/internal/poll"
	"github.com/orlandoburli/apiary-slack/internal/reply"
	"github.com/orlandoburli/apiary-slack/internal/slack"
)

// version is stamped at build time (see the Makefile).
var version = "dev"

func main() { pluginsdk.Main(serve) }

func serve(ctx context.Context, req pluginsdk.Request) (any, *pluginsdk.ResponseError) {
	if req.Capability != pluginsdk.CapabilitySource {
		return nil, errorf("unsupported_capability",
			"this plugin implements %q, host asked for %q", pluginsdk.CapabilitySource, req.Capability)
	}
	cfg, err := config.Parse(req.Config)
	if err != nil {
		return nil, errorf("invalid_config", "%v", err)
	}
	client := slack.New(cfg.APIURL, cfg.Token)

	switch req.Method {
	case pluginsdk.SourceMethodPoll:
		p := &poll.Poller{Config: cfg, Client: client, Now: time.Now, Logf: logf}
		result, err := p.Poll(ctx)
		if err != nil {
			return nil, errorf("poll_failed", "%v", err)
		}
		return result, nil

	case pluginsdk.SourceMethodAcknowledge:
		// Recording the dispatch is what stops the conversation being emitted
		// as pending on every poll, so a failure here must be surfaced.
		payload, err := poll.DecodeAck(cfg, req.Payload)
		if err != nil {
			return nil, errorf("acknowledge_failed", "%v", err)
		}
		// The reaction is cosmetic; failing the ack would only add noise.
		if err := reply.Acknowledge(ctx, cfg, client, payload); err != nil {
			logf("acknowledge %s: reaction: %v", payload.Item.ID, err)
		}
		return pluginsdk.SourceOKResult{OK: true}, nil

	case pluginsdk.SourceMethodWriteResult:
		var payload pluginsdk.SourceWriteResultRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, errorf("invalid_payload", "%v", err)
		}
		// Unlike the reaction, a lost answer is the whole point of the run:
		// surface it.
		if err := reply.Write(ctx, cfg, client, payload); err != nil {
			return nil, errorf("write_result_failed", "%v", err)
		}
		return pluginsdk.SourceOKResult{OK: true}, nil

	default:
		return nil, errorf("unsupported_method", "unknown method %q", req.Method)
	}
}

func errorf(code, format string, args ...any) *pluginsdk.ResponseError {
	return &pluginsdk.ResponseError{Code: code, Message: fmt.Sprintf(format, args...)}
}

// logf writes operator diagnostics to stderr, which the host captures and
// attributes to this plugin. stdout is reserved for the single JSON response.
func logf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "apiary-plugin-slack[%s]: "+format+"\n", append([]any{version}, args...)...)
}
