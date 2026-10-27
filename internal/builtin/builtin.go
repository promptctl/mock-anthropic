// Package builtin is PROJECT.md "Claude Code's own model calls": the model calls Claude Code makes beside
// a turn, each recognized by its prompt and answered here so no consumer has to script them. They are
// scripts tried after every consumer directory, so a consumer script that matches one of these requests
// wins.
package builtin

import (
	"encoding/json"
	"slices"
	"strings"

	"github.com/promptctl/mock-anthropic/internal/messages"
	"github.com/promptctl/mock-anthropic/internal/script"
)

// promptBlock is the system block a side query's prompt opens. Claude Code gives the billing header and
// its identity line blocks of their own and joins the rest into one, so a side query's prompt opens its
// block, while a main turn's block opens with Claude Code's own prompt, whatever it quotes.
func promptBlock(r messages.Request, opening string) (string, bool) {
	i := slices.IndexFunc(r.SystemTexts, func(text string) bool { return strings.HasPrefix(text, opening) })
	if i < 0 {
		return "", false
	}
	return r.SystemTexts[i], true
}

func says(text string, stop script.StopReason) script.Reply {
	return script.MessageReply{Content: []script.Block{script.Text{Text: text}}, StopReason: stop}
}

// [LAW:one-type-per-behavior] Both phrasings are one call: from 2.1.283 on, "You are naming a coding
// session…", in older builds "Generate a concise, sentence-case title". Both ask, through
// `output_config.format`, for a JSON object with one string field, `title`, and Claude Code parses the
// reply's text as that object.
var titlePrompts = []string{
	"You are naming a coding session so the user can pick it out of a long list of sessions.",
	"Generate a concise, sentence-case title",
}

// The classifier's prompt names the one answer format it reads, and each format's instruction appears
// only in its own prompts: a severity from 0 to 100 (2.1.289, when the `tengu_auto_mode_config` flag
// asks for it), `<block>yes|no</block>` (2.1.289 otherwise, and 2.1.88 when the flag's
// `twoStageClassifier` is set), or a call to the `classify_result` tool (2.1.88 otherwise).
// [LAW:one-source-of-truth] The request says which, so the answer is the allowing one in that format.
// [LAW:nothing-unseen] Each format is its own built-in, so the record names the answer that was sent.
// [LAW:no-silent-failure] A prompt asking for none of them is left unmatched, a 400.
const classifierPrompt = "You are a security monitor for autonomous AI coding agents."

var classifierAllows = []struct {
	format, asks string
	reply        script.Reply
}{
	{"severity", "<severity>N</severity>", says("<severity>0</severity>", script.StopEndTurn)},
	{"block", "<block>no</block>", says("<block>no</block>", script.StopEndTurn)},
	{
		"classify_result tool",
		"Use the classify_result tool to report your classification.",
		script.MessageReply{
			// Marshaled by hand: the key order is the one Claude Code's schema lists.
			Content:    []script.Block{script.ToolUse{Name: "classify_result", Input: json.RawMessage(`{"thinking":"","shouldBlock":false,"reason":"Allowed by default."}`)}},
			StopReason: script.StopToolUse,
		},
	},
}

// The quota probe asks for one token so Claude Code can read the rate-limit headers; its body is unread.
var quotaProbe = says("OK", script.StopMaxTokens)

// The title answer, like every reply here, is built once and never mutated.
var sessionTitle = says(`{"title":"Mock session"}`, script.StopEndTurn)

// All is the built-in answers, in the order they are tried.
func All() []script.Script {
	scripts := []script.Script{{
		Origin: "built-in: session title",
		Answer: func(r messages.Request) script.Reply {
			for _, opening := range titlePrompts {
				if _, ok := promptBlock(r, opening); ok {
					return sessionTitle
				}
			}
			return nil
		},
	}}
	for _, allow := range classifierAllows {
		scripts = append(scripts, script.Script{
			Origin: "built-in: auto mode classifier, " + allow.format,
			Answer: func(r messages.Request) script.Reply {
				if block, ok := promptBlock(r, classifierPrompt); ok && strings.Contains(block, allow.asks) {
					return allow.reply
				}
				return nil
			},
		})
	}
	return append(scripts, script.Script{
		Origin: "built-in: quota probe",
		Answer: func(r messages.Request) script.Reply {
			if r.MaxTokens == 1 && len(r.LastUserTexts) == 1 && r.LastUserTexts[0] == "quota" {
				return quotaProbe
			}
			return nil
		},
	})
}
