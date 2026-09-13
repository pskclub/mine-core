package main

import (
	"encoding/json"
	"time"

	"gorm.io/gorm"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/repository"
	"github.com/pskclub/mine-core/v2/utils"
)

// --- Example 5: transactional outbox -----------------------------------------
//
// Publishing after the commit is the right default and still leaves a hole: the
// commit succeeds, the process dies, and nobody is ever told the order exists.
// The database says one thing, the rest of the system another, and nothing
// anywhere notices.
//
// The outbox closes it by making the message part of the transaction. The row
// and the message are written together or not at all, and a job does the
// publishing afterwards from a table that survives the crash.
//
// The price is honest: one more table, latency of up to one job tick, and
// duplicate sends when the publish succeeds but the sent_at update does not.
// That last one is the same at-least-once every consumer already has to
// tolerate, which is why the outbox is not an excuse to stop being idempotent.
//
// Use it when the message is money, or a promise to another service. Not for
// every event.

// OrderRow is the work. Outbox is the message that says the work happened.
type OrderRow struct {
	ID    string `gorm:"primaryKey" json:"id"`
	Total int64  `json:"total"`
}

func (OrderRow) TableName() string { return "orders" }

type Outbox struct {
	ID         int64 `gorm:"primaryKey"`
	Exchange   string
	RoutingKey string
	MessageID  string
	Payload    []byte
	SentAt     *time.Time
	CreatedAt  *time.Time
}

func (Outbox) TableName() string { return "outbox" }

// createOrderWithOutbox writes both rows in one transaction. Either the order
// exists and the message is queued to go, or neither happened.
func createOrderWithOutbox(ctx core.IContext, order OrderRow) core.IError {
	payload, err := json.Marshal(order)
	if err != nil {
		return core.Wrap(err, "outbox: marshal")
	}

	return repository.New[OrderRow](ctx).Transaction(func(tx *gorm.DB) error {
		if err := repository.NewWithDB[OrderRow](ctx, tx).Create(&order); err != nil {
			return err
		}
		return repository.NewWithDB[Outbox](ctx, tx).Create(&Outbox{
			Exchange:   ordersExchange,
			RoutingKey: "order.created",
			// A stable id taken from the work itself. The drain may publish this
			// row twice; dedupe on the consumer is what makes that harmless, and
			// it has nothing to match on if this is a fresh UUID each time.
			MessageID: order.ID,
			Payload:   payload,
			CreatedAt: utils.ToPointer(time.Now()),
		})
	})
}

// registerOutboxDrain adds the sender to the service's own job registry. It
// needs no broker to register — only to run.
func registerOutboxDrain(reg *core.JobRegistry) {
	_ = reg.Register(core.JobDef{
		Name:        "outbox-drain",
		Description: "publish everything the outbox table is still holding",
		Schedule:    core.Every(5 * time.Second),
		// Two replicas draining at once publish the same rows twice. This collapses
		// the overlap; on more than one replica the limit has to be counted
		// somewhere shared (see the jobs example).
		MaxConcurrent: 1,
		Concurrency:   core.ConcurrencySkip,
	}, drainOutbox)
}

func drainOutbox(ctx core.ICronjobContext) error {
	rows, err := repository.New[Outbox](ctx).
		Where("sent_at IS NULL").
		Order("id").
		Limit(100).
		FindAll()
	if err != nil {
		return err
	}

	for _, row := range rows {
		// Payload is []byte that is already JSON, so the content type has to be
		// said out loud — otherwise it goes out as application/octet-stream.
		if err := ctx.MQ().PublishWith(row.Exchange, row.RoutingKey, row.Payload,
			core.PublishOptions{
				ContentType: "application/json",
				MessageID:   row.MessageID,
			}); err != nil {
			// Stop at the first failure and leave the rest for the next tick.
			// Nothing is lost, and the messages keep leaving in the order they
			// were written — which continuing past a failure would break.
			return err
		}

		now := time.Now()
		if err := repository.New[Outbox](ctx).
			Where("id = ?", row.ID).
			Update("sent_at", now); err != nil {
			return err
		}
	}

	if len(rows) > 0 {
		ctx.Log().Info("outbox drained", "rows", len(rows))
	}
	return nil
}
