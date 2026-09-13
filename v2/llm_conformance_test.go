package core_test

import (
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm/llmtest"
)

// The memory model runs the same contract as every real driver.
//
// That is the point of shipping it: a test that swaps a provider for
// NewMemoryLLM is only trustworthy if the two behave the same way at the edges
// — same error codes for a malformed request, same usage reporting, same
// streaming and Close semantics. Anywhere they diverge, a test passes against
// the memory model and the service fails in production.
func TestMemoryLLMConformance(t *testing.T) {
	llmtest.RunSuite(t, func(t *testing.T) core.ILLM {
		return core.NewMemoryLLM()
	})
}
