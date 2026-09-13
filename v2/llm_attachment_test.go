package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLLMPart_withAttachesWithoutLosingText(t *testing.T) {
	msg := LLMUser("ยอดรวมเท่าไหร่").With(
		LLMImage([]byte{0xFF, 0xD8}, "image/jpeg"),
		LLMFile([]byte("%PDF-"), "application/pdf", "invoice.pdf"),
	)

	assert.Equal(t, LLMRoleUser, msg.Role)
	assert.Equal(t, "ยอดรวมเท่าไหร่", msg.Text,
		"an image with no instruction gets described, which is rarely what the caller wanted")
	require.Len(t, msg.Parts, 2)
	assert.Equal(t, LLMPartImage, msg.Parts[0].Type)
	assert.Equal(t, "invoice.pdf", msg.Parts[1].Filename)
}

func TestLLMPart_withDoesNotMutateTheOriginal(t *testing.T) {
	// Messages get built once and reused across a batch. Appending in place
	// would make the second document carry the first one's scan.
	base := LLMUser("อ่านนี่")
	a := base.With(LLMImage([]byte{1}, "image/png"))
	b := base.With(LLMImage([]byte{2}, "image/png"))

	assert.Empty(t, base.Parts)
	require.Len(t, a.Parts, 1)
	require.Len(t, b.Parts, 1)
	assert.Equal(t, []byte{1}, a.Parts[0].Data)
	assert.Equal(t, []byte{2}, b.Parts[0].Data)
}

func TestLLMPart_validation(t *testing.T) {
	m := NewMemoryLLM()
	send := func(p LLMPart) IError {
		_, err := m.Generate(LLMRequest{Messages: []LLMMessage{LLMUser("x").With(p)}})
		return err
	}

	cases := []struct {
		name    string
		part    LLMPart
		wantMsg string
	}{
		{"no type", LLMPart{Data: []byte{1}, MediaType: "image/png"}, "no Type"},
		{"unknown type", LLMPart{Type: "video", Data: []byte{1}, MediaType: "video/mp4"}, "unknown Type"},
		{"empty", LLMPart{Type: LLMPartImage}, "neither Data nor URL"},
		{"both sources", LLMPart{Type: LLMPartImage, Data: []byte{1}, MediaType: "image/png", URL: "https://x"}, "pick one"},
		{"no media type", LLMPart{Type: LLMPartImage, Data: []byte{1}}, "MediaType"},
		{"bad media type", LLMPart{Type: LLMPartImage, Data: []byte{1}, MediaType: "jpeg"}, "not a media type"},
		{"bad detail", LLMPart{Type: LLMPartImage, URL: "https://x", Detail: "medium"}, "low, high or auto"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := send(tc.part)
			require.Error(t, err,
				"the layer underneath drops a part it cannot parse, and the model answers about the text alone")
			assert.Equal(t, "LLM_INVALID_REQUEST", err.GetCode())
			assert.Contains(t, err.GetMessage(), tc.wantMsg)
			assert.Contains(t, err.GetMessage(), "messages[0].Parts[0]",
				"the error must say which part, not just that one was wrong")
		})
	}
}

func TestLLMPart_validAttachmentsPass(t *testing.T) {
	m := NewMemoryLLM("ok")

	_, err := m.Generate(LLMRequest{Messages: []LLMMessage{
		LLMUser("x").With(
			LLMImage([]byte{1, 2}, "image/png"),
			LLMImageURL("https://example.com/a.png"),
			LLMFile([]byte("%PDF-"), "application/pdf", "a.pdf"),
			LLMPart{Type: LLMPartImage, Data: []byte{3}, MediaType: "image/webp", Detail: "low"},
		),
	}})
	require.NoError(t, err)
}

func TestLLMPart_sizeGuardIsCheckedAcrossTheWholeRequest(t *testing.T) {
	// The limit that matters is what the request carries in total — three
	// images under the cap individually still fail at the provider together.
	m := NewMemoryLLM("ok")
	half := make([]byte, LLMMaxAttachmentBytes/2+1)

	_, err := m.Generate(LLMRequest{Messages: []LLMMessage{
		LLMUser("a").With(LLMImage(half, "image/png")),
		LLMUser("b").With(LLMImage(half, "image/png")),
	}})
	require.Error(t, err)
	assert.Equal(t, "LLM_ATTACHMENT_TOO_LARGE", err.GetCode())
	assert.Equal(t, 413, err.GetStatus())
	assert.Contains(t, err.GetMessage(), "resize",
		"the message has to say what to do about it, not only that it is too big")
}

func TestLLMPart_recordedForAssertions(t *testing.T) {
	m := NewMemoryLLM("ok")
	app := llmTestApp(t, WithLLM(m))
	ctx := app.NewContext(t.Context(), ModeTest)

	_, err := LLM(ctx).Generate(LLMRequest{
		Messages: []LLMMessage{LLMUser("อ่านใบเสร็จ").With(LLMImage([]byte{0xFF, 0xD8}, "image/jpeg"))},
	})
	require.NoError(t, err)

	parts := LLMCalls(m)[0].Messages[0].Parts
	require.Len(t, parts, 1, "a test must be able to assert on what was attached")
	assert.Equal(t, "image/jpeg", parts[0].MediaType)
}

func TestLLMPart_attachmentsAreCountedSeparatelyFromPromptLength(t *testing.T) {
	// An image costs thousands of tokens that a character count says nothing
	// about — the debug log would otherwise imply a request was cheap when it
	// was the opposite.
	req := LLMRequest{Messages: []LLMMessage{
		LLMUser("hi").With(LLMImage(make([]byte, 1024), "image/png")),
		LLMUser("again").With(LLMImage(make([]byte, 2048), "image/png")),
	}}

	count, bytes := req.attachments()
	assert.Equal(t, 2, count)
	assert.Equal(t, 3072, bytes)
	assert.Equal(t, 7, req.promptChars(), "prompt length stays about the text alone")
}
