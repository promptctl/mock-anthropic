package messages

import (
	"reflect"
	"testing"
)

func TestReadKeyReadsOnlyThePositionThePromptAndTheResults(t *testing.T) {
	create := `{"diagnostics":{"previous_message_id":null},"model":"a","system":"s","metadata":{"x":1},"messages":[
		{"role":"user","content":[{"type":"text","text":"<system-reminder>\nhook\n</system-reminder>"},{"type":"text","text":"  fix it "},{"type":"text","text":"<system-reminder>\nlate\n</system-reminder>"}]},
		{"role":"system","content":"ignored"}]}`
	k, err := ReadKey([]byte(create))
	if want := (Key{LastText: "fix it"}); err != nil || !reflect.DeepEqual(k, want) {
		t.Errorf("create: %+v, %v; want %+v", k, err, want)
	}
	cont := `{"diagnostics":{"previous_message_id":"msg_1"},"messages":[{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"toolu_1","content":"hello\n\n<system-reminder>\ntokens\n</system-reminder>","cache_control":{"type":"ephemeral"}},
		{"type":"tool_result","tool_use_id":"toolu_2","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}],"is_error":true}]}]}`
	k, err = ReadKey([]byte(cont))
	want := Key{Thread: Thread{Continue: true, Previous: "msg_1"}, Results: []Result{{Text: "hello"}, {Text: "a\nb", IsError: true}}}
	if err != nil || !reflect.DeepEqual(k, want) {
		t.Errorf("continue: %+v, %v; want %+v", k, err, want)
	}
	other := `{"diagnostics":{"previous_message_id":"msg_1"},"messages":[{"role":"user","content":[
		{"type":"tool_result","tool_use_id":"toolu_9","content":"hello"},
		{"type":"tool_result","tool_use_id":"toolu_8","content":"a\nb","is_error":true}]}]}`
	if o, _ := ReadKey([]byte(other)); o.ID() != k.ID() {
		t.Errorf("tool_use ids and reminders changed the key: %s vs %s", o.ID(), k.ID())
	}
}

func TestReadKeyRefusesABodyWithoutAUsablePosition(t *testing.T) {
	for name, raw := range map[string]string{
		"no diagnostics":     `{"messages":[{"role":"user","content":"x"}]}`,
		"no previous":        `{"diagnostics":{},"messages":[{"role":"user","content":"x"}]}`,
		"an empty previous":  `{"diagnostics":{"previous_message_id":""},"messages":[{"role":"user","content":"x"}]}`,
		"a numeric previous": `{"diagnostics":{"previous_message_id":7},"messages":[{"role":"user","content":"x"}]}`,
		"no user message":    `{"diagnostics":{"previous_message_id":null},"messages":[{"role":"system","content":"x"}]}`,
		"not a JSON object":  `[]`,
		"content not blocks": `{"diagnostics":{"previous_message_id":null},"messages":[{"role":"user","content":7}]}`,
	} {
		if k, err := ReadKey([]byte(raw)); err == nil {
			t.Errorf("%s: key %+v", name, k)
		}
	}
}
