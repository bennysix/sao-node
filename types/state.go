package types

import (
	"github.com/ipfs/go-cid"
)

// ----------------
// order state
// ----------------

/**
 * order index for quick access to OrderInfo datastore keys.
 */
type OrderIndex struct {
	Alls []OrderKey
}

type OrderKey struct {
	DataId string
}

/**
 * order state
 */
type OrderInfo struct {
	// commit id
	DataId string
	Owner  string
	Cid    cid.Cid

	// Staged
	StagePath string

	// ready
	OrderId     uint64
	OrderHash   string
	OrderTxType AssignTxType
	OrderHeight int64
	Shards      map[string]OrderShardInfo

	ExpireHeight uint64

	State   OrderState
	Tries   uint64
	RetryAt int64
	LastErr string
}

type OrderState uint64

const (
	OrderStateStaged OrderState = iota
	OrderStateReady
	OrderStateComplete
	OrderStateTerminate
	OrderStateExpired
)

var orderStateString = map[OrderState]string{
	OrderStateStaged:   "Staged",
	OrderStateReady:    "Ready",
	OrderStateComplete: "Complete",
}

func (s OrderState) String() string {
	return orderStateString[s]
}

/**
 * shard state in order
 */
type OrderShardInfo struct {
	ShardId      uint64
	Peer         string
	Cid          string
	Provider     string
	State        OrderShardState
	CompleteHash string
}

type OrderShardState string

const (
	ShardStateAssigned  OrderShardState = "assigned"
	ShardStateNotified  OrderShardState = "notified"
	ShardStateCompleted OrderShardState = "completed"
	ShardStateError     OrderShardState = "error"
)

// ----------------
// shard state
// ----------------
/**
 * shard index for quick access to ShardInfo datastore keys.
 */
type ShardIndex struct {
	All []ShardKey
}

/**
 * ShardInfo key
 */
type ShardKey struct {
	OrderId uint64
	Cid     cid.Cid
}

/**
 * shard state
 */
type ShardInfo struct {
	OrderId uint64
	DataId  string
	Cid     cid.Cid

	Owner          string
	Gateway        string
	OrderOperation string
	ShardOperation string
	CompleteHash   string
	CompleteHeight int64
	Size           uint64

	Tries        uint64
	ExpireHeight uint64
	State        ShardState
	LastErr      string
}

type ShardState uint64

const (
	ShardStateValidated ShardState = iota
	ShardStateStored
	ShardStateTxSent
	ShardStateComplete
	ShardStateTerminate
)

var shardStateString = map[ShardState]string{
	ShardStateValidated: "validated",
	ShardStateStored:    "stored",
	ShardStateTxSent:    "txSent",
	ShardStateComplete:  "completed",
}

func (s ShardState) String() string {
	return shardStateString[s]
}

type MigrateInfo struct {
	DataId       string
	OrderId      uint64
	Cid          string
	FromProvider string
	ToProvider   string

	MigrateTxHash   string
	MigrateTxHeight int64

	CompleteTxHash   string
	CompleteTxHeight int64

	State MigrateState
}

type MigrateState uint64

const (
	MigrateStateTxSent MigrateState = iota
	MigrateStateComplete
)

var migrateStateString = map[MigrateState]string{
	MigrateStateTxSent:   "txSent",
	MigrateStateComplete: "complete",
}

func (m MigrateState) String() string {
	return migrateStateString[m]
}

type MigrateKey struct {
	DataId       string
	FromProvider string
}

type MigrateIndex struct {
	All []MigrateKey
}
