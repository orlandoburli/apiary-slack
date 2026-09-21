// Command apiary-plugin-slack is an Apiary protocol-1 source plugin that turns
// Slack conversations into work items and posts the agents' answers back.
//
// poll reads new messages from the configured channels and DMs through the
// Slack Web API — outbound calls only, no Socket Mode, no Events API, no
// listener — and emits one work item per human turn. write_result posts what
// the workflow publishes as a reply in the same thread. A follow-up in that
// thread is a new work item carrying the thread so far as a transcript, which
// is how a conversation spans turns even though plugin sources cannot resume a
// parked task.
//
// Pair every Slack workflow with `trigger.once: true` and pin
// `match.source`: the plugin never re-emits a message it has read, and `once`
// is the second lock if one ever reappears (a restored state file).
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
		var payload pluginsdk.SourceAckRequest
		if err := json.Unmarshal(req.Payload, &payload); err != nil {
			return nil, errorf("invalid_payload", "%v", err)
		}
		// A missing reaction is cosmetic; failing the ack would only add
		// noise to the host's log.
		if err := reply.Acknowledge(ctx, cfg, client, payload); err != nil {
			logf("acknowledge %s: %v", payload.Item.ID, err)
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
