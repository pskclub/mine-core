package core

import (
	"context"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// A handler stores a tenant or a locale on the context, then hands the work to
// a background context through WithContext. What it stored must still be there
// — and a later SetData on either side must stay on that side.
func TestContext_withContextCarriesTheDataBag(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(context.Background())
	Set(ctx, "tenant", "acme")

	child := ctx.WithContext(context.Background())
	got, ok := Get[string](child, "tenant")
	require.True(t, ok, "the child must see what the parent stored")
	assert.Equal(t, "acme", got)

	Set(child, "only-child", 1)
	_, onParent := Get[int](ctx, "only-child")
	assert.False(t, onParent, "the bag is copied, not shared")
}

// GetUser/SetUser race when a middleware sets the principal while a goroutine
// the handler spawned reads it. Run under -race this is the proof.
func TestContext_userIsSafeToSetAndReadConcurrently(t *testing.T) {
	app := newTestApp(t)
	ctx := app.NewContext(context.Background())

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() { defer wg.Done(); ctx.SetUser(&ContextUser{ID: "u"}) }()
		go func() { defer wg.Done(); _ = ctx.GetUser() }()
	}
	wg.Wait()
	assert.Equal(t, "u", ctx.GetUser().ID)
}

// An App built with no environment still has to answer the questions every
// capability asks of it — IsDev, Config, the boot log — rather than panic in
// whichever one is reached first.
func TestNewApp_toleratesANilEnvironment(t *testing.T) {
	app, err := NewApp(nil)
	require.Nil(t, err)
	assert.NotNil(t, app.Config())
	assert.False(t, app.ENV().IsDev())
	app.LogCapabilities() // must not panic
}
