package main

import (
	"strings"
	"sync"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
)

// --- Example 3: what pub/sub is actually good at -----------------------------
//
// Three shapes that fit, and the line where it stops fitting. The test for
// everything below: if losing a message would page somebody, it should not have
// been a pub/sub message.

// --- Cache invalidation across replicas --------------------------------------
//
// The canonical use. Deleting a redis key already invalidates it everywhere,
// because there is one redis — this is for state held *in a process*, which no
// amount of deleting reaches.

type Settings struct {
	TenantID string `gorm:"primaryKey" json:"tenant_id"`
	Version  int64  `json:"version"`
}

func (Settings) TableName() string { return "settings" }

// localCache is the in-process copy. It is created per hub/registry rather than
// living in a package-level var, so two of them in one test never collide.
type localCache struct {
	mu   sync.RWMutex
	byID map[string]Settings
}

func newLocalCache() *localCache {
	return &localCache{byID: map[string]Settings{}}
}

func (l *localCache) Delete(id string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.byID, id)
}

// UpdateSettings is the writer: the row is the truth, the message only says the
// truth moved.
func UpdateSettings(ctx core.IContext, s *Settings) core.IError {
	if err := repository.New[Settings](ctx).Save(s); err != nil {
		return err
	}
	ctx.PubSub().Publish("settings.changed", s.TenantID)
	return nil
}

// onSettingsChanged runs in every replica.
//
// A replica that misses the message keeps its stale copy until the TTL runs out,
// which is exactly why the in-process copy still needs one. Pub/sub makes
// invalidation *fast*; the TTL is what makes it *correct*.
func onSettingsChanged(local *localCache) core.PubSubHandler {
	return func(ctx core.IContext, msg *core.PubSubMessage) error {
		tenant := msg.String()
		core.Forget(ctx.Cache(), "settings:"+tenant)
		local.Delete(tenant)
		return nil
	}
}

// --- Feature flags and config reloads ----------------------------------------
//
// Keep the old value when the reload fails. A reload that fails and leaves the
// service with no config is worse than one that leaves it with yesterday's.
//
// Reload on a timer as well as on the message: the timer is what makes a replica
// that happened to be restarting during the publish eventually correct.
func onConfigReload(store *localCache) core.PubSubHandler {
	return func(ctx core.IContext, msg *core.PubSubMessage) error {
		cfg, err := repository.New[Settings](ctx).FindOne("tenant_id = ?", msg.String())
		if err != nil {
			return err // logged and reported; the old config stays in place
		}

		store.mu.Lock()
		store.byID[cfg.TenantID] = *cfg
		store.mu.Unlock()

		ctx.Log().Info("config reloaded", "tenant", cfg.TenantID, "version", cfg.Version)
		return nil
	}
}

// --- Live updates to websockets ----------------------------------------------
//
// One redis subscription per *process*, fanned out in memory to however many
// sockets are connected. Ten thousand clients is then ten thousand map entries,
// not ten thousand redis connections — which redis would refuse long before you
// got there.
//
// The channel-per-user is what lets a replica ignore messages for users whose
// sockets it is not holding.
type hub struct {
	mu      sync.RWMutex
	clients map[string][]chan []byte // user id → its open sockets
}

func newHub() *hub { return &hub{clients: map[string][]chan []byte{}} }

func (h *hub) Run(app *core.App) core.IError {
	sub, err := app.PubSub().PSubscribe("notify.*")
	if err != nil {
		return err
	}
	defer sub.Close()

	for msg := range sub.C() {
		userID := strings.TrimPrefix(msg.Channel, "notify.")

		h.mu.RLock()
		for _, out := range h.clients[userID] {
			select {
			case out <- msg.Payload:
			default:
				// Deliberately dropping, rather than blocking the read loop until
				// redis disconnects this subscriber for falling behind. Choosing
				// what is lost beats being cut off silently.
				app.Log().Warn("dropping notification: socket is not keeping up",
					"user", userID)
			}
		}
		h.mu.RUnlock()
	}
	return nil
}

// Notify is the publishing side, from anywhere in the service.
func Notify(ctx core.IContext, userID, text string) {
	ctx.PubSub().Publish("notify."+userID, map[string]string{"text": text})
}

// --- Where the line is -------------------------------------------------------
//
//	work that must happen exactly once      → jobs
//	work another service must acknowledge   → MQ
//	a request/response across services      → HTTP, via Requester
//	an event stream with history            → a table, or redis streams via Redis()
//
// And the rules that follow from having no acknowledgement at all: a subscriber
// must tolerate both duplicates and losses, so write handlers that *sync state*
// ("make it match") rather than ones that *step* ("add one"). Keep them fast —
// a subscriber redis considers slow is a subscriber redis disconnects; hand
// heavy work to a job and return.
