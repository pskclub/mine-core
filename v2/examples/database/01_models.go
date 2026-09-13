package main

import (
	"time"

	core "github.com/pskclub/mine-core/v2"
	"github.com/pskclub/mine-core/v2/utils"
	"gorm.io/gorm"
)

// --- Example 1: models, column types, and who owns the schema ---------------
//
// core.IModel asks for exactly one method — TableName on the value, which is
// what makes repository.New[User] work rather than New[*User]. Everything else
// is ordinary GORM tagging.
//
// The struct *describes* a table a migration already created; it does not own
// one. devMigrate at the bottom of this file exists so `go run .` works against
// an empty sqlite database and for no other reason.

// A status is a string with constants in front of it, not an int. An int enum
// is unreadable in a psql session and silently reassigns itself the day someone
// inserts a value in the middle of the list.
type UserStatus string

const (
	UserActive  UserStatus = "active"
	UserTrial   UserStatus = "trial"
	UserDormant UserStatus = "dormant"
)

type OrderStatus string

const (
	OrderPending   OrderStatus = "pending"
	OrderPaid      OrderStatus = "paid"
	OrderCancelled OrderStatus = "cancelled"
)

type User struct {
	ID     string     `json:"id"     gorm:"column:id;primaryKey"`
	Email  string     `json:"email"  gorm:"column:email;uniqueIndex"`
	Name   string     `json:"name"   gorm:"column:name"`
	Status UserStatus `json:"status" gorm:"column:status;index"`

	// Money is an integer count of the smallest unit, never a float. 0.1 + 0.2
	// is not 0.3 in binary floating point, and a balance that drifts by a
	// satang every few thousand transactions is a bug nobody can reproduce.
	CreditsSatang int64 `json:"credits_satang" gorm:"column:credits_satang"`

	// GORM maintains these two, in UTC — core.NewDatabase sets NowFunc — so what
	// is written does not depend on the server's timezone. Convert to the user's
	// zone at the very top layer, never in the database.
	//
	// They are pointers because that is the house rule for every time column: a
	// value type cannot tell "not set yet" apart from year 1, and the zero time
	// is what a nullable column decodes into. utils.ToNonPointer reads one back
	// when the call site wants a value.
	CreatedAt *time.Time `json:"created_at" gorm:"column:created_at"`
	UpdatedAt *time.Time `json:"updated_at" gorm:"column:updated_at"`

	// gorm.DeletedAt is what turns Delete() into a soft delete and adds
	// "deleted_at IS NULL" to every query. Worth knowing before you add it: the
	// uniqueIndex on email above still sees the dead rows, so a deleted address
	// can never be registered again. On postgres that wants to be a partial
	// index instead — CREATE UNIQUE INDEX ... WHERE deleted_at IS NULL.
	DeletedAt gorm.DeletedAt `json:"-" gorm:"column:deleted_at;index"`

	Profile *Profile `json:"profile,omitempty" gorm:"foreignKey:UserID"`
	Orders  []Order  `json:"orders,omitempty"  gorm:"foreignKey:UserID"`
}

func (User) TableName() string { return "users" }

// BeforeCreate is the one job hooks are good for: deriving a field from the row
// itself. A hook that sends mail, publishes an event or queries another table
// makes every write an invisible side effect that runs inside whatever
// transaction happens to be open — that belongs in a service method.
func (u *User) BeforeCreate(*gorm.DB) error {
	if u.ID == "" {
		u.ID = utils.NewUUID()
	}
	return nil
}

type Profile struct {
	ID     string `json:"id"      gorm:"column:id;primaryKey"`
	UserID string `json:"user_id" gorm:"column:user_id;uniqueIndex"`
	City   string `json:"city"    gorm:"column:city"`
}

func (Profile) TableName() string { return "profiles" }

func (p *Profile) BeforeCreate(*gorm.DB) error {
	if p.ID == "" {
		p.ID = utils.NewUUID()
	}
	return nil
}

type Order struct {
	ID     string      `json:"id"      gorm:"column:id;primaryKey"`
	UserID string      `json:"user_id" gorm:"column:user_id;index"`
	Status OrderStatus `json:"status"  gorm:"column:status;index"`
	// The pair (user_id, created_at) is the index this table actually wants:
	// one composite answers both "this user's orders" and "newest first",
	// which two single-column indexes do not.
	TotalSatang int64      `json:"total_satang" gorm:"column:total_satang"`
	CreatedAt   *time.Time `json:"created_at"   gorm:"column:created_at;index"`

	User  *User       `json:"user,omitempty"  gorm:"foreignKey:UserID"`
	Items []OrderItem `json:"items,omitempty" gorm:"foreignKey:OrderID"`
}

func (Order) TableName() string { return "orders" }

func (o *Order) BeforeCreate(*gorm.DB) error {
	if o.ID == "" {
		o.ID = utils.NewUUID()
	}
	return nil
}

type OrderItem struct {
	ID          string `json:"id"       gorm:"column:id;primaryKey"`
	OrderID     string `json:"order_id" gorm:"column:order_id;index"`
	SKU         string `json:"sku"      gorm:"column:sku"`
	Qty         int64  `json:"qty"      gorm:"column:qty"`
	PriceSatang int64  `json:"price_satang" gorm:"column:price_satang"`
}

func (OrderItem) TableName() string { return "order_items" }

func (i *OrderItem) BeforeCreate(*gorm.DB) error {
	if i.ID == "" {
		i.ID = utils.NewUUID()
	}
	return nil
}

// allModels is the list devMigrate and the seeds walk, in one place so a new
// table is one edit rather than three.
func allModels() []any { return []any{&User{}, &Profile{}, &Order{}, &OrderItem{}} }

// devMigrate builds the schema from the structs above.
//
// This is a development and test convenience only. AutoMigrate never drops a
// column, never changes a type, never renames, and knows nothing about partial
// indexes, check constraints or backfills — so the schema it produces drifts
// away from the one you think you have, silently, until a query fails in
// production for a reason that is nowhere in git history. Real schema belongs
// to a migration tool run as its own deploy step (v2/docs/migrations.md).
func devMigrate(db *gorm.DB) core.IError {
	if err := db.AutoMigrate(allModels()...); err != nil {
		return core.Wrap(err, "database example: dev migrate")
	}
	return nil
}
