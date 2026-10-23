package core

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMemoryStorage_presignPostCarriesThePolicy(t *testing.T) {
	st := NewMemoryStorage()
	form, err := st.PresignPost("uploads/avatar.png", 5*time.Minute, StoragePostOptions{
		ContentType: "image/png",
		MaxSize:     2 << 20,
		Metadata:    map[string]string{"owner": "u1"},
	})
	require.Nil(t, err)
	assert.Equal(t, "uploads/avatar.png", form.Fields["key"])
	assert.Equal(t, "image/png", form.Fields["Content-Type"])
	assert.Equal(t, "u1", form.Fields["x-amz-meta-owner"])
	assert.Equal(t, "201", form.Fields["success_action_status"])
	assert.Equal(t, "300", form.Fields["x-amz-expires"])

	var policy struct{ Conditions []any }
	require.NoError(t, json.Unmarshal([]byte(form.Fields["policy"]), &policy))
	assert.Contains(t, form.Fields["policy"], `"content-length-range",0,2097152`,
		"the size range is what the bucket enforces, so a test can see it")
}

func TestMemoryStorage_presignPostKeyPrefixLetsTheClientNameTheFile(t *testing.T) {
	st := NewMemoryStorage()
	form, err := st.PresignPost("uploads/u1/", time.Minute, StoragePostOptions{
		KeyPrefix: true, ContentTypePrefix: "image/", SuccessRedirect: "https://app/done",
	})
	require.Nil(t, err)
	assert.Equal(t, "uploads/u1/${filename}", form.Fields["key"])
	assert.Equal(t, "https://app/done", form.Fields["success_action_redirect"])
	assert.NotContains(t, form.Fields, "success_action_status", "a redirect replaces the status answer")
	assert.Contains(t, form.Fields["policy"], `"starts-with","$key","uploads/u1/"`)
	assert.Contains(t, form.Fields["policy"], `"starts-with","$Content-Type","image/"`)
}

func TestPostConditions_shape(t *testing.T) {
	conds := postConditions("k", StoragePostOptions{})
	assert.Equal(t, []any{map[string]string{"success_action_status": "201"}}, conds,
		"no options: only the answer the SDK does not add itself")

	conds = postConditions("k", StoragePostOptions{MinSize: 10, Public: true, CacheControl: "max-age=60"})
	assert.Contains(t, conds, []any{"content-length-range", int64(10), int64(5 << 40)},
		"a minimum alone still needs a maximum, so S3's own ceiling stands in")
	assert.Contains(t, conds, map[string]string{"acl": "public-read"})
	assert.Contains(t, conds, map[string]string{"Cache-Control": "max-age=60"})
}

func TestNoopStorage_presignPostIsDisabled(t *testing.T) {
	_, err := noopStorage{}.PresignPost("k", time.Minute)
	require.NotNil(t, err)
	assert.Equal(t, "STORAGE_DISABLED", err.GetCode())
}
