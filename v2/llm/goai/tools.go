package goai

import (
	core "github.com/pskclub/mine-core/v2"
	"github.com/zendev-sh/goai/provider"
	"github.com/zendev-sh/goai/provider/google"
)

// The tools in this file are run by the provider, not by the service. They are
// constructors rather than a documented map of strings because that is the
// difference between a typo caught at compile time and a request that a provider
// accepts, ignores, and bills for — a grounded answer that was never grounded
// reads exactly like one that was.
//
// They live in the driver package rather than in core because each one exists
// only for its own provider: sending google_search to Anthropic is not a
// degraded request, it is a rejected one.

// GoogleSearch grounds the answer in Google Search. Gemini decides for itself
// whether to search; what it read comes back in LLMResponse.Sources, and showing
// those to the user is a term of Google's grounding service, not a nicety.
//
//	resp, err := core.LLM(ctx).Generate(core.LLMRequest{
//	    Messages: []core.LLMMessage{core.LLMUser(question)},
//	    Tools:    []core.LLMTool{goai.GoogleSearch()},
//	})
//
// Requires Gemini 2.0 or newer. Sources arrive with Generate; a streamed
// generation carries the text but not the citations, because the provider does
// not send them as chunks.
func GoogleSearch(opts ...google.GoogleSearchOption) core.LLMTool {
	return toolFrom(google.Tools.GoogleSearch(opts...),
		"Search Google and ground the answer in what it finds.")
}

// GoogleSearchWebOnly is GoogleSearch restricted to web results — the usual
// choice for a text answer, since image results cost tokens a text answer
// cannot use.
func GoogleSearchWebOnly() core.LLMTool {
	return GoogleSearch(google.WithWebSearch())
}

// GoogleSearchSince restricts grounding to a time range, both RFC3339. Worth it
// for anything where a three-year-old page is a wrong answer rather than an old
// one — prices, regulations, a schedule.
func GoogleSearchSince(startRFC3339, endRFC3339 string) core.LLMTool {
	return GoogleSearch(google.WithWebSearch(), google.WithTimeRange(startRFC3339, endRFC3339))
}

// URLContext lets Gemini fetch the URLs in the prompt and read them.
//
// It is the tool for "summarise this page": the model retrieves the content
// itself, so the service does not need an HTTP client, a fetch timeout, or an
// opinion about which URLs are safe to follow — and does not get one either, so
// keep it away from prompts carrying URLs a user chose.
func URLContext() core.LLMTool {
	return toolFrom(google.Tools.URLContext(),
		"Fetch and read the URLs mentioned in the conversation.")
}

// CodeExecution lets Gemini write Python and run it in Google's sandbox, using
// the output in its answer. The arithmetic a language model gets wrong is
// exactly what this fixes.
func CodeExecution() core.LLMTool {
	return toolFrom(google.Tools.CodeExecution(),
		"Write and run Python to compute an answer.")
}

func toolFrom(def provider.ToolDefinition, description string) core.LLMTool {
	return core.LLMTool{
		Name:            def.Name,
		Description:     description,
		ProviderType:    def.ProviderDefinedType,
		ProviderOptions: def.ProviderDefinedOptions,
	}
}
