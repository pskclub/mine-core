package core

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
)

// Closing a subscription while messages are being published to it must not
// panic: the publisher's select could pick the send over the done signal and
// hit a channel that was closed a moment earlier. Run with -race, this is also
// the ordering proof for the lock around the send.
func TestMemoryPubSub_closeDuringPublishDoesNotPanic(t *testing.T) {
	c := NewMemoryCache()
	t.Cleanup(func() { _ = c.Close() })
	ps := c.PubSub()

	for i := 0; i < 50; i++ {
		sub, err := ps.Subscribe("burst")
		require.NoError(t, err)

		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 20; j++ {
				_ = ps.Publish("burst", j)
			}
		}()
		go func() {
			defer wg.Done()
			_ = sub.Close()
		}()
		wg.Wait()

		// the channel still ends up closed, so a range over C() terminates
		_, open := <-sub.C()
		for open {
			_, open = <-sub.C()
		}
	}
}
