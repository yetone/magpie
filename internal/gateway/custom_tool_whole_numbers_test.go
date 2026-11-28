package gateway

import (
	"encoding/json"
	"testing"
)

// recordedArgs is an encoder that keeps each call's arguments as given.
type recordedArgs struct {
	name string
	args map[string]string
}

func (r *recordedArgs) event(ev Event) {
	switch ev.Kind {
	case KToolStart:
		r.name = ev.Name
	case KToolArgs:
		r.args[r.name] += ev.Text
	}
}
func (r *recordedArgs) finish()    {}
func (r *recordedArgs) keepalive() {}

// A custom tool's arguments reach the client as the model wrote them,
// numbers and all, while a function's whole floats still reach it as
// integers. The model wrote quotes in the input it did not escape, and the
// text between them lost its zero fractions: "2.0" in the code became "2".
func TestCustomToolCallKeepsItsNumbers(t *testing.T) {
	for _, args := range []string{
		`{"input":"x = "2.0"; v = "1.0.2"; n = 3.0"}`,
		`{"input":"echo "release 1.0 ready"}`,
		`{"input":"x = \"2.0\"; v = \"1.0.2\"; n = 3.0"}`,
	} {
		rec := &recordedArgs{args: map[string]string{}}
		enc := &wholeEncoder{streamEncoder: rec, named: map[string]nsTool{"exec": {Name: "exec", Custom: true}}}
		enc.event(Event{Kind: KToolStart, ID: "call_1", Name: "exec"})
		for i := 0; i < len(args); i += 3 {
			enc.event(Event{Kind: KToolArgs, Text: args[i:min(i+3, len(args))]})
		}
		enc.event(Event{Kind: KToolStart, ID: "call_2", Name: "wait"})
		for _, piece := range []string{`{"ms":25`, `00.0`, `}`} {
			enc.event(Event{Kind: KToolArgs, Text: piece})
		}
		if rec.args["exec"] != args {
			t.Errorf("custom tool's arguments %s, want %s", rec.args["exec"], args)
		}
		if rec.args["wait"] != `{"ms":2500}` {
			t.Errorf("function's arguments %s, want its whole float as an integer", rec.args["wait"])
		}
	}
}

// A whole reply the same way: a custom tool's call keeps its arguments,
// a function's loses its zero fractions.
func TestWholeResultLeavesCustomToolsAsTheyCame(t *testing.T) {
	named := map[string]nsTool{"exec": {Name: "exec", Custom: true}}
	res := wholeResult(Result{Parts: []Part{
		{Kind: ToolCall, ID: "call_1", Name: "exec", Args: json.RawMessage(`{"input":"x = "2.0""}`)},
		{Kind: ToolCall, ID: "call_2", Name: "wait", Args: json.RawMessage(`{"ms":2500.0}`)},
	}}, named)
	if got := string(res.Parts[0].Args); got != `{"input":"x = "2.0""}` {
		t.Errorf("custom tool's arguments %s", got)
	}
	if got := string(res.Parts[1].Args); got != `{"ms":2500}` {
		t.Errorf("function's arguments %s", got)
	}
}
