package goai_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/llm/goai"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The embedding options are asserted on the outgoing body for the same reason
// the Gemini ones are: a provider given a length it does not recognise returns
// vectors of its default length and reports success. The row then fails to
// insert — or, worse, inserts into a column that happens to match and can never
// be compared against the rest of the corpus.

// fakeEmbedProvider records request bodies and answers with vectors of dims
// floats, so a test can assert both what was asked for and what came back.
func fakeEmbedProvider(t *testing.T, google bool, dims int) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var seen []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var decoded map[string]any
		_ = json.Unmarshal(raw, &decoded)
		seen = append(seen, decoded)

		vec := make([]float64, dims)
		for i := range vec {
			vec[i] = 0.5
		}
		encoded, _ := json.Marshal(vec)

		w.Header().Set("Content-Type", "application/json")
		if google {
			_, _ = fmt.Fprintf(w, `{"embeddings":[{"values":%s}]}`, encoded)
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":[{"embedding":%s,"index":0}],"usage":{"prompt_tokens":3}}`, encoded)
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

func newGoogleEmbedder(t *testing.T, url string, opts ...func(*goai.EmbedConfig)) core.IEmbedder {
	t.Helper()
	cfg := goai.EmbedConfig{Provider: "google", Model: "gemini-embedding-001", APIKey: "test-key", BaseURL: url}
	for _, o := range opts {
		o(&cfg)
	}
	e, err := goai.NewEmbedConfig(cfg)
	require.NoError(t, err)
	return e
}

// googleEmbedRequest digs out the per-text request Gemini's batch endpoint
// wraps everything in.
func googleEmbedRequest(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	requests, ok := body["requests"].([]any)
	require.True(t, ok, "the batch endpoint takes a requests array: %v", body)
	require.NotEmpty(t, requests)
	first, ok := requests[0].(map[string]any)
	require.True(t, ok)
	return first
}

func TestEmbed_googleDimensionsAndTaskTypeReachTheWire(t *testing.T) {
	srv, seen := fakeEmbedProvider(t, true, 768)
	e := newGoogleEmbedder(t, srv.URL)

	vecs, err := e.EmbedWith(core.EmbedRequest{
		Texts:      []string{"หนังสือพิมพ์ราชกิจจานุเบกษา"},
		Dimensions: 768,
		Task:       core.EmbedDocument,
	})
	require.NoError(t, err)
	require.Len(t, vecs, 1)
	assert.Len(t, vecs[0], 768)

	req := googleEmbedRequest(t, (*seen)[0])
	assert.Equal(t, float64(768), req["outputDimensionality"],
		"gemini-embedding-001 returns 3072 unless asked otherwise, which no vector(768) column accepts")
	assert.Equal(t, "RETRIEVAL_DOCUMENT", req["taskType"])
}

func TestEmbed_taskTypesMapToGeminisOwnNames(t *testing.T) {
	cases := map[core.EmbedTask]string{
		core.EmbedDocument:       "RETRIEVAL_DOCUMENT",
		core.EmbedQuery:          "RETRIEVAL_QUERY",
		core.EmbedSimilarity:     "SEMANTIC_SIMILARITY",
		core.EmbedClassification: "CLASSIFICATION",
		core.EmbedClustering:     "CLUSTERING",
	}
	for task, want := range cases {
		t.Run(string(task), func(t *testing.T) {
			srv, seen := fakeEmbedProvider(t, true, 8)
			e := newGoogleEmbedder(t, srv.URL)

			_, err := e.EmbedWith(core.EmbedRequest{Texts: []string{"x"}, Task: task})
			require.NoError(t, err)
			assert.Equal(t, want, googleEmbedRequest(t, (*seen)[0])["taskType"])
		})
	}
}

func TestEmbed_configuredDimensionsApplyToEveryCall(t *testing.T) {
	// The number belongs to the index, not to the call site. A service that has
	// to remember it at every call site is a service where one job forgets.
	srv, seen := fakeEmbedProvider(t, true, 768)
	e := newGoogleEmbedder(t, srv.URL, goai.WithEmbedDimensions(768))

	assert.Equal(t, 768, e.Dimensions(),
		"a configured length is known before the first call, which is when a migration declares the column")

	_, err := e.Embed("ประกาศกระทรวง")
	require.NoError(t, err)
	assert.Equal(t, float64(768), googleEmbedRequest(t, (*seen)[0])["outputDimensionality"])
}

func TestEmbed_requestDimensionsOverrideTheConfiguredOne(t *testing.T) {
	srv, seen := fakeEmbedProvider(t, true, 256)
	e := newGoogleEmbedder(t, srv.URL, goai.WithEmbedDimensions(768))

	_, err := e.EmbedWith(core.EmbedRequest{Texts: []string{"x"}, Dimensions: 256})
	require.NoError(t, err)
	assert.Equal(t, float64(256), googleEmbedRequest(t, (*seen)[0])["outputDimensionality"])
	assert.Equal(t, 768, e.Dimensions(),
		"one call at another length says nothing about the column the corpus lives in")
}

func TestEmbed_openAIDimensionsUseItsOwnFlatKey(t *testing.T) {
	// OpenAI takes a flat "dimensions"; Google nests "outputDimensionality"
	// under a namespace. Sending either one's spelling to the other is accepted
	// and ignored.
	srv, seen := fakeEmbedProvider(t, false, 512)
	e, err := goai.NewEmbedConfig(goai.EmbedConfig{
		Provider: "compat", Model: "text-embedding-3-small", APIKey: "k", BaseURL: srv.URL,
	})
	require.NoError(t, err)

	vecs, err := e.EmbedWith(core.EmbedRequest{Texts: []string{"x"}, Dimensions: 512})
	require.NoError(t, err)
	assert.Len(t, vecs[0], 512)
	assert.Equal(t, float64(512), (*seen)[0]["dimensions"])
}

func TestEmbed_unsupportedOptionsAreRejectedNotDropped(t *testing.T) {
	// The whole point of the typed fields: a wrong vector is not a degraded
	// result, it is a wrong one that ranks plausibly and never errors.
	srv, _ := fakeEmbedProvider(t, false, 8)

	openai, err := goai.NewEmbedConfig(goai.EmbedConfig{
		Provider: "compat", Model: "text-embedding-3-small", APIKey: "k", BaseURL: srv.URL,
	})
	require.NoError(t, err)
	_, err = openai.EmbedWith(core.EmbedRequest{Texts: []string{"x"}, Task: core.EmbedQuery})
	require.Error(t, err, "OpenAI embeds one way whatever the vector is for")
	assert.ErrorIs(t, err, core.ErrEmbedUnsupported)
	assert.Equal(t, "EMBED_UNSUPPORTED", err.GetCode())

	ollama, err := goai.NewEmbedConfig(goai.EmbedConfig{
		Provider: "ollama", Model: "nomic-embed-text", BaseURL: srv.URL,
	})
	require.NoError(t, err)
	_, err = ollama.EmbedWith(core.EmbedRequest{Texts: []string{"x"}, Dimensions: 256})
	require.Error(t, err, "ollama returns whatever length its model produces")
	assert.ErrorIs(t, err, core.ErrEmbedUnsupported)
}

func TestEmbed_providerOptionsMergeIntoTheMappedNamespace(t *testing.T) {
	srv, seen := fakeEmbedProvider(t, true, 8)
	e := newGoogleEmbedder(t, srv.URL)

	_, err := e.EmbedWith(core.EmbedRequest{
		Texts:           []string{"x"},
		Dimensions:      768,
		ProviderOptions: map[string]any{"google": map[string]any{"taskType": "FACT_VERIFICATION"}},
	})
	require.NoError(t, err)

	req := googleEmbedRequest(t, (*seen)[0])
	assert.Equal(t, float64(768), req["outputDimensionality"], "the mapped option must survive the caller's own")
	assert.Equal(t, "FACT_VERIFICATION", req["taskType"],
		"the escape hatch is what reaches a task type this driver has no constant for")
}

func TestEmbed_perRequestModelIsRouted(t *testing.T) {
	// Re-indexing a corpus under a new model means embedding with two of them,
	// and the alternative — a second embedder built outside the App — is one
	// nothing meters.
	srv, _ := fakeEmbedProvider(t, true, 8)
	e := newGoogleEmbedder(t, srv.URL)

	_, err := e.EmbedWith(core.EmbedRequest{Texts: []string{"x"}, Model: "text-embedding-004"})
	require.NoError(t, err)
}

func TestEmbed_emptyInputIsACallerBug(t *testing.T) {
	srv, _ := fakeEmbedProvider(t, true, 8)
	e := newGoogleEmbedder(t, srv.URL)

	for name, req := range map[string]core.EmbedRequest{
		"no texts":     {},
		"blank text":   {Texts: []string{"  "}},
		"unknown task": {Texts: []string{"x"}, Task: "retrieval"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := e.EmbedWith(req)
			require.Error(t, err)
			assert.Equal(t, 400, err.GetStatus(),
				"a caller bug must not be reported as a provider failure, or it will be retried forever")
		})
	}
}
