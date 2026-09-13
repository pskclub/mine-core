package main

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"

	core "github.com/pskclub/mine-core/v2"
)

// --- Example 6: embeddings and a minimal RAG --------------------------------
//
// Embedding is its own capability rather than a method on ILLM because
// Anthropic has no embedding endpoint at all: a service generating with Claude
// embeds with somebody else, which means a different model id, key and base
// URL. core.Embedder(ctx) is never nil — with no AI_EMBED_MODEL every call
// fails with EMBED_DISABLED rather than returning an empty vector, because an
// empty vector is not a degraded answer, it is an index that silently returns
// nothing relevant forever.

// embedDimensions is the number that has to agree in three places at once: the
// embedding model, AI_EMBED_DIMENSIONS, and the width of the column the vectors
// are stored in.
//
// gemini-embedding-001 returns 3072 unless asked for less, so a vector(768)
// column rejects every row — and the worse case is when the widths happen to
// match and the vectors are simply incomparable with the corpus, which nothing
// downstream can detect because every similarity score still looks fine.
const embedDimensions = 768

// Passage is one chunk of a document plus the metadata an answer needs to cite
// itself. An answer that cannot say where it came from cannot be checked.
type Passage struct {
	DocID  string
	Page   int
	Text   string
	Vector []float32
	// Model records what produced the vector. Changing embedding model means
	// re-embedding everything, and storing the version is what makes that a
	// migration you can do in batches rather than a full-stop rebuild.
	Model string
}

// indexChunks embeds the corpus side of a search.
//
// One call with many texts, not many calls with one: providers charge per token
// rather than per request, and for short chunks the round trip is most of the
// wall clock.
func indexChunks(ctx context.Context, docID string, chunks []string) ([]Passage, core.IError) {
	e := core.Embedder(ctx)

	vecs, err := e.EmbedWith(core.EmbedRequest{
		Texts:      chunks,
		Dimensions: embedDimensions,
		// The corpus side. A driver that cannot honour a task type refuses
		// rather than dropping it, because a vector built for the wrong task is
		// wrong rather than merely worse.
		Task: core.EmbedDocument,
	})
	if err != nil {
		return nil, err
	}

	out := make([]Passage, 0, len(chunks))
	for i, chunk := range chunks {
		// vecs[i] pairs with chunks[i] — the driver guarantees one vector per
		// text or returns an error, never a short slice quietly misaligned.
		out = append(out, Passage{
			DocID:  docID,
			Page:   i + 1,
			Text:   chunk,
			Vector: vecs[i],
			Model:  e.Model(),
		})
	}
	return out, nil
}

// searchTopK embeds the query side and ranks by cosine similarity.
//
// EmbedQuery pairs with EmbedDocument above. Using one without the other
// retrieves measurably worse, silently — which is why they are written next to
// each other in this file.
func searchTopK(ctx context.Context, question string, corpus []Passage, k int) ([]Passage, core.IError) {
	vecs, err := core.Embedder(ctx).EmbedWith(core.EmbedRequest{
		Texts:      []string{question},
		Dimensions: embedDimensions,
		Task:       core.EmbedQuery,
	})
	if err != nil {
		return nil, err
	}
	q := vecs[0]

	type scored struct {
		p     Passage
		score float64
	}
	ranked := make([]scored, 0, len(corpus))
	for _, p := range corpus {
		// Vectors of different lengths score 0: they came from different models
		// and any other number would be meaningless. That is the failure
		// surfacing rather than turning into a scrambled ranking.
		ranked = append(ranked, scored{p, core.CosineSimilarity(q, p.Vector)})
	}
	slices.SortFunc(ranked, func(a, b scored) int { return cmp.Compare(b.score, a.score) })

	out := make([]Passage, 0, k)
	for _, s := range ranked[:min(k, len(ranked))] {
		out = append(out, s.p)
	}
	return out, nil
}

const ragRules = `Answer only from the passages provided.
Cite the passage number [n] for every fact you state.
If the passages do not contain the answer, say so — never fill the gap yourself.`

// answerFromCorpus is retrieval-augmented generation with nothing clever in it:
// find the passages, put them in the prompt, constrain the model to them.
//
// In-memory ranking is fine up to a few thousand passages. Past that the
// database's own vector index is the answer — core returns vectors and leaves
// storage to the service, because pgvector and Atlas vector search pull the
// repository layer in different directions.
func answerFromCorpus(ctx context.Context, question string, corpus []Passage) (string, core.IError) {
	passages, err := searchTopK(ctx, question, corpus, 5)
	if err != nil {
		return "", err
	}
	if len(passages) == 0 {
		return "", core.New(404, "NO_RELEVANT_DOCUMENTS", "nothing in the corpus is close enough to answer this")
	}

	var b strings.Builder
	for i, p := range passages {
		fmt.Fprintf(&b, "[%d] (%s p.%d) %s\n\n", i+1, p.DocID, p.Page, p.Text)
	}

	resp, err := core.LLM(ctx).Generate(core.LLMRequest{
		System: ragRules,
		// The rules are long and identical every time; the passages are not, so
		// they go in the message where they cannot move the cache prefix.
		CacheSystem: true,
		Messages:    []core.LLMMessage{core.LLMUser("Passages:\n" + b.String() + "\nQuestion: " + question)},
		MaxTokens:   1024,
	})
	if err != nil {
		return "", err
	}
	return readAnswer(resp)
}
