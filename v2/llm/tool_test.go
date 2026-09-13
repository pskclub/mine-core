package llm_test

import (
	"context"
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type city struct {
	City string `json:"city" jsonschema:"description=City name in English"`
}

func weather(context.Context, city) (string, core.IError) { return "hot", nil }

func TestTool_schemaComesFromTheInputType(t *testing.T) {
	tool, err := llm.Tool("get_weather", "Current weather for a city. Call this when asked about weather.", weather)
	require.NoError(t, err)

	assert.Equal(t, "get_weather", tool.Name)
	require.NotNil(t, tool.Execute, "an ordinary tool is run here, so it must carry the function")
	props, ok := tool.Schema["properties"].(map[string]any)
	require.True(t, ok, "the model needs the argument shape described: %v", tool.Schema)
	assert.Contains(t, props, "city")
}

func TestTool_missingDescriptionIsRejected(t *testing.T) {
	// The description is the only thing the model reads to decide whether to
	// call it — without one the tool is called at random, or never.
	_, err := llm.Tool("get_weather", "", weather)
	require.Error(t, err)
	assert.Equal(t, "LLM_INVALID_TOOL", err.GetCode())
}

func TestToolSet_collectsBothKindsAndKeepsTheFirstError(t *testing.T) {
	provided := core.LLMTool{
		Name:         "google_search",
		Description:  "Search Google.",
		ProviderType: "google.google_search",
	}

	tools, err := llm.NewToolSet().
		AddTool(provided).
		Add(llm.Tool("get_weather", "Current weather for a city.", weather)).
		Build()
	require.NoError(t, err)
	require.Len(t, tools, 2, "a provider-defined tool and one of ours belong in the same set")
	assert.True(t, tools[0].IsProviderDefined())
	assert.False(t, tools[1].IsProviderDefined())

	// A tool that cannot be described is a startup bug, and Build is where it
	// has to surface — the alternative is a request rejected at runtime.
	_, err = llm.NewToolSet().
		Add(llm.Tool("", "no name", weather)).
		Add(llm.Tool("ok", "fine", weather)).
		Build()
	require.Error(t, err)
	assert.Equal(t, "LLM_INVALID_TOOL", err.GetCode())
}
