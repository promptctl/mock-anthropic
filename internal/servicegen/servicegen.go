// Package servicegen holds the generators whose answers name no one (PROJECT.md "Every other in-scope
// endpoint"): feature flags, telemetry intake, organization settings and policy, the MCP directories,
// the plugin marketplace pointer, the changelog and token counting. Each body is the least both Claude
// Code builds in use accept: 2.1.88 (its source) and 2.1.289 (its shipped binary). Where a value turns
// a feature on or off, it is the one that keeps Claude Code from calling an endpoint the table refuses.
package servicegen

import (
	"bytes"
	"context"
	"net/http"
	"strings"

	"github.com/promptctl/mock-anthropic/internal/generator"
	"github.com/promptctl/mock-anthropic/internal/messages"
)

// Set is one generator per endpoint it answers, for the table's rows to name. [LAW:types-are-the-program]
// A row takes its generator from a field, so no row can be given another endpoint's.
type Set struct {
	CountTokens, FeatureFlags, EventLoggingBatch, EventLoggingV2Batch, DatadogLogs, DatadogErrorTracking generator.Func
	MetricsEnabled, RemoteManagedSettings, PolicyLimits, PenguinMode, GroveConfig                        generator.Func
	MCPRegistry, MCPServers, MarketplaceVersion, Changelog                                               generator.Func
}

// The marketplace commit the version pointer names, obviously not a real one.
var marketplaceSHA = strings.Repeat("0", 40)

// New is the set of generators.
func New() Set {
	return Set{
		CountTokens: countTokens,
		// No flags. Both builds read an empty map as no payload and keep the flags the config dir cached,
		// so a fresh one runs every flag at its default.
		FeatureFlags: json(map[string]any{"features": map[string]any{}}),
		// Neither build reads these answers' bodies, and 2.1.289 may gzip what it sends, so the request
		// body is not read either. 2.1.289's error tracking logs a status other than 2xx as a warning.
		EventLoggingBatch:    json(map[string]any{}),
		EventLoggingV2Batch:  json(map[string]any{}),
		DatadogLogs:          json(map[string]any{}),
		DatadogErrorTracking: json(map[string]any{}),
		// Off: on, 2.1.289's BigQuery exporter posts to the refused `otel-metrics` row.
		MetricsEnabled: json(map[string]any{"metrics_logging_enabled": false}),
		// No settings: both builds read a 204 as none configured.
		RemoteManagedSettings: noContent,
		// No restrictions. 2.1.289 reads a 404 here as a proxy that did not forward the request.
		PolicyLimits: json(map[string]any{"restrictions": map[string]any{}}),
		PenguinMode:  json(map[string]any{"enabled": false, "disabled_reason": nil}),
		// Off. Claude Code decides from the config dir's cached answer, which this one replaces in the
		// background, so on a fresh config dir the consent notice and its refused `account-settings` call
		// never run.
		GroveConfig: json(map[string]any{"grove_enabled": false, "domain_excluded": false, "notice_is_grace_period": true, "notice_reminder_frequency": nil}),
		// No servers. 2.1.289 asks for the next page while `metadata.nextCursor` is set.
		MCPRegistry: json(map[string]any{"servers": []any{}, "metadata": map[string]any{}}),
		MCPServers:  json(map[string]any{"data": []any{}, "next_page": nil}),
		// Claude Code next fetches `{sha}.zip`, which the table refuses, unless the config dir it runs
		// with says the official marketplace is installed or its auto-install already ran.
		MarketplaceVersion: text(marketplaceSHA + "\n"),
		// No `## ` section, so no release notes.
		Changelog: text("# Changelog\n"),
	}
}

// [LAW:one-type-per-behavior] A fixed answer: the same response to every request, with nothing decided
// to report. A body is read once, so each request gets a new reader.
func json(body map[string]any) generator.Func {
	return func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
		return generator.JSON(body, nil)
	}
}

func text(body string) generator.Func {
	return func(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
		response := generator.Response{
			Status: http.StatusOK,
			Header: http.Header{"Content-Type": {"text/plain; charset=utf-8"}},
			Body:   strings.NewReader(body),
		}
		return generator.Answer{Reply: response}, nil
	}
}

func noContent(context.Context, map[string]string, generator.Request) (generator.Answer, error) {
	return generator.Answer{Reply: generator.Response{Status: http.StatusNoContent, Header: http.Header{}, Body: bytes.NewReader(nil)}}, nil
}

func countTokens(_ context.Context, _ map[string]string, r generator.Request) (generator.Answer, error) {
	req, err := messages.ParseCountTokens(r.Body)
	if err != nil {
		return generator.Answer{Reply: generator.Declined{Why: err.Error()}}, nil
	}
	// [LAW:nothing-unseen] The count given, for the record.
	return generator.JSON(map[string]any{"input_tokens": req.InputTokens}, generator.Facts{"input_tokens": req.InputTokens})
}
