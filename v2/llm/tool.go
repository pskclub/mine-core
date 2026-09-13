package llm

import (
	"context"
	"encoding/json"

	core "github.com/pskclub/mine-core/v2"
)

// Tool builds a core.LLMTool from a typed input struct.
//
// The JSON Schema comes from In — the same reflection New[T] uses — and the
// model's arguments are unmarshalled into In before the function runs, so a
// tool is ordinary Go code that happens to be reachable by a model:
//
//	weather, err := llm.Tool("get_weather", "Current weather for a city. Call this when asked about weather.",
//	    func(ctx context.Context, in struct {
//	        City string `json:"city" jsonschema:"description=City name in English"`
//	    }) (string, core.IError) {
//	        return s.weather.Lookup(ctx, in.City)
//	    })
//
// It returns an error rather than panicking on a type that cannot be described,
// so the failure lands at startup where tools are wired — not on the first
// request that happened to need it.
//
// Use struct{} for a tool that takes no arguments.
func Tool[In any](name, description string, execute func(ctx context.Context, in In) (string, core.IError)) (core.LLMTool, core.IError) {
	var out core.LLMTool

	schema, err := SchemaOf[In]()
	if err != nil {
		return out, core.Wrapf(err, "llm: tool %q input", name)
	}
	if name == "" {
		return out, core.New(400, "LLM_INVALID_TOOL", "llm: a tool needs a name")
	}
	if description == "" {
		// The description is what the model reads to decide whether to call it.
		// A nameless-in-practice tool gets called at random, or never.
		return out, core.Newf(400, "LLM_INVALID_TOOL",
			"llm: tool %q needs a description saying when to call it", name)
	}

	return core.LLMTool{
		Name:        name,
		Description: description,
		Schema:      schema.Schema,
		Execute: func(ctx context.Context, raw json.RawMessage) (string, core.IError) {
			var in In
			if len(raw) > 0 {
				if jerr := json.Unmarshal(raw, &in); jerr != nil {
					// Back to the model rather than up to the caller: a model
					// that sent malformed arguments can fix them on the next
					// step, and failing the whole generation throws away the
					// work already paid for.
					return "", core.Wrapf(jerr, "llm: tool %q got arguments it could not read", name)
				}
			}
			return execute(ctx, in)
		},
	}, nil
}

// ToolSet collects tools without an error check between each one. Go lets a
// two-value call be passed straight through, so the wiring reads as a list:
//
//	tools, err := llm.NewToolSet().
//	    Add(llm.Tool("get_order", "Look up an order by id.", h.getOrder)).
//	    Add(llm.Tool("refund_order", "Refund an order. Only for a cancelled one.", h.refund)).
//	    Build()
//	if err != nil {
//	    return err          // a tool that cannot be described is a startup bug
//	}
type ToolSet struct {
	tools []core.LLMTool
	err   core.IError
}

func NewToolSet() *ToolSet { return &ToolSet{} }

// Add appends a tool. The first failure is kept and reported by Build; the
// rest are still evaluated, so one bad tool does not hide the others' output.
func (s *ToolSet) Add(tool core.LLMTool, err core.IError) *ToolSet {
	if err != nil {
		if s.err == nil {
			s.err = err
		}
		return s
	}
	s.tools = append(s.tools, tool)
	return s
}

// AddTool appends tools that were not built by llm.Tool — a provider-defined
// one from the driver, or a core.LLMTool assembled by hand:
//
//	tools, err := llm.NewToolSet().
//	    AddTool(goai.GoogleSearch()).
//	    Add(llm.Tool("get_order", "Look up an order by id.", h.getOrder)).
//	    Build()
//
// It exists so those do not have to be written as Add(tool, nil), which reads
// like an error was discarded.
func (s *ToolSet) AddTool(tools ...core.LLMTool) *ToolSet {
	s.tools = append(s.tools, tools...)
	return s
}

func (s *ToolSet) Build() ([]core.LLMTool, core.IError) {
	if s.err != nil {
		return nil, s.err
	}
	return s.tools, nil
}
