package utils

import (
	"bytes"
	"context"
	"fmt"
	"sao-node/types"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
)

const (
	ORDER_INDEX_KEY   = "order-index"
	ORDER_KEY         = "order-%s"
	SHARD_INDEX_KEY   = "shard-index"
	SHARD_KEY         = "order-%d-shard-%v"
	MIGRATE_INDEX_KEY = "migrate-index"
	MIGRATE_KEY       = "migrate-%d-shard-%v"
)

// -----
// order
// -----

/**
 * get order key in datastore.
 */
func orderDatastoreKey(id string) datastore.Key {
	return datastore.NewKey(fmt.Sprintf(ORDER_KEY, id))
}

/**
 * Save order state in datastore.
 */
func SaveOrder(ctx context.Context, ds datastore.Batching, order types.OrderInfo) error {
	key := orderDatastoreKey(order.DataId)

	exists, err := ds.Has(ctx, key)
	if err != nil {
		return err
	}

	buf := new(bytes.Buffer)
	err = order.MarshalCBOR(buf)
	if err != nil {
		return err
	}
	err = ds.Put(ctx, key, buf.Bytes())
	if err != nil {
		return err
	}

	if !exists {
		err = UpdateOrderIndex(ctx, ds, order.DataId)
		if err != nil {
			return err
		}
	}
	return nil
}

/**
 * Get order state from datastore.
 */
func GetOrder(ctx context.Context, ds datastore.Batching, id string) (types.OrderInfo, error) {
	key := orderDatastoreKey(id)
	exists, err := ds.Has(ctx, key)
	if err != nil {
		return types.OrderInfo{}, err
	}
	if !exists {
		return types.OrderInfo{}, nil
	}

	bs, err := ds.Get(ctx, key)
	if err != nil {
		return types.OrderInfo{}, err
	}

	var orderInfo types.OrderInfo
	err = orderInfo.UnmarshalCBOR(bytes.NewReader(bs))
	if err != nil {
		return types.OrderInfo{}, err
	}
	return orderInfo, nil
}

/**
 * update order index.
 */
func UpdateOrderIndex(ctx context.Context, ds datastore.Batching, id string) error {
	key := datastore.NewKey(ORDER_INDEX_KEY)
	exists, err := ds.Has(ctx, key)
	if err != nil {
		return err
	}
	var index types.OrderIndex
	if exists {
		data, err := ds.Get(ctx, key)
		if err != nil {
			return err
		}
		err = index.UnmarshalCBOR(bytes.NewReader(data))
		if err != nil {
			return err
		}
	}
	if len(index.All) > 0 {
		index.All = index.All + "," + id
	} else {
		index.All = id
	}
	buf := new(bytes.Buffer)
	err = index.MarshalCBOR(buf)
	if err != nil {
		return err
	}
	err = ds.Put(ctx, key, buf.Bytes())
	if err != nil {
		return err
	}
	return nil
}

/**
 * Get order index.
 */
func GetOrderIndex(ctx context.Context, ds datastore.Batching) (types.OrderIndex, error) {
	key := datastore.NewKey(ORDER_INDEX_KEY)
	exists, err := ds.Has(ctx, key)
	if err != nil {
		return types.OrderIndex{}, err
	}
	if !exists {
		return types.OrderIndex{}, nil
	}

	data, err := ds.Get(ctx, key)
	if err != nil {
		return types.OrderIndex{}, err
	}

	var index types.OrderIndex
	err = index.UnmarshalCBOR(bytes.NewReader(data))
	return index, err
}

// -----
// migrate
// -----
func migrateDatastoreKey(orderId uint64, cid cid.Cid) datastore.Key {
	return datastore.NewKey(fmt.Sprintf(MIGRATE_KEY, orderId, cid))
}

func SaveMigrate(ctx context.Context, ds datastore.Batching, migrate types.MigrateInfo) error {
	key := migrateDatastoreKey(migrate.OrderId, migrate.Cid)
	exists, err := ds.Has(ctx, key)
	if err != nil {
		return err
	}

	buf := new(bytes.Buffer)
	err = migrate.MarshalCBOR(buf)
	if err != nil {
		return err
	}
	err = ds.Put(ctx, key, buf.Bytes())
	if err != nil {
		return err
	}
	if !exists {
		err = UpdateMigrateIndex(ctx, ds, migrate.OrderId, migrate.Cid)
		if err != nil {
			return err
		}
	}
	return nil
}

func GetMigrate(ctx context.Context, ds datastore.Batching, orderId uint64, cid cid.Cid) (types.MigrateInfo, error) {
	key := migrateDatastoreKey(orderId, cid)
	exists, err := ds.Has(ctx, key)
	if err != nil {
		return types.MigrateInfo{}, err
	}
	if !exists {
		return types.MigrateInfo{}, nil
	}

	bs, err := ds.Get(ctx, key)
	if err != nil {
		return types.MigrateInfo{}, err
	}

	var migrateInfo types.MigrateInfo
	err = migrateInfo.UnmarshalCBOR(bytes.NewReader(bs))
	if err != nil {
		return types.MigrateInfo{}, err
	}
	return migrateInfo, nil
}

func UpdateMigrateIndex(
	ctx context.Context,
	ds datastore.Batching,
	orderId uint64,
	cid cid.Cid,
) error {
	key := datastore.NewKey(MIGRATE_INDEX_KEY)
	exists, err := ds.Has(ctx, key)
	if err != nil {
		return err
	}

	var index types.ShardIndex
	if exists {
		data, err := ds.Get(ctx, key)
		if err != nil {
			return err
		}
		err = index.UnmarshalCBOR(bytes.NewReader(data))
		if err != nil {
			return err
		}
	}
	index.All = append(index.All, types.ShardKey{
		OrderId: orderId,
		Cid:     cid,
	})

	buf := new(bytes.Buffer)
	err = index.MarshalCBOR(buf)
	if err != nil {
		return err
	}
	err = ds.Put(ctx, key, buf.Bytes())
	if err != nil {
		return err
	}
	return nil
}

// -----
// shard
// -----
/**
 * get shard key in datastore.
 */
func orderShardDatastoreKey(orderId uint64, cid cid.Cid) datastore.Key {
	return datastore.NewKey(fmt.Sprintf(SHARD_KEY, orderId, cid))
}

/**
 * save order shard state.
 */
func SaveShard(ctx context.Context, ds datastore.Batching, shard types.ShardInfo) error {
	key := orderShardDatastoreKey(shard.OrderId, shard.Cid)

	exists, err := ds.Has(ctx, key)
	if err != nil {
		return err
	}

	buf := new(bytes.Buffer)
	err = shard.MarshalCBOR(buf)
	if err != nil {
		return err
	}
	err = ds.Put(ctx, key, buf.Bytes())
	if err != nil {
		return err
	}
	if !exists {
		err = UpdateShardIndex(ctx, ds, shard.OrderId, shard.Cid)
		if err != nil {
			return err
		}
	}
	return nil
}

/**
 * Get shard state from datastore.
 */
func GetShard(ctx context.Context, ds datastore.Batching, orderId uint64, cid cid.Cid) (types.ShardInfo, error) {
	key := orderShardDatastoreKey(orderId, cid)
	exists, err := ds.Has(ctx, key)
	if err != nil {
		return types.ShardInfo{}, err
	}
	if !exists {
		return types.ShardInfo{}, nil
	}

	bs, err := ds.Get(ctx, key)
	if err != nil {
		return types.ShardInfo{}, err
	}

	var shardInfo types.ShardInfo
	err = shardInfo.UnmarshalCBOR(bytes.NewReader(bs))
	if err != nil {
		return types.ShardInfo{}, err
	}
	return shardInfo, nil
}

/**
 * update shard index
 */
func UpdateShardIndex(
	ctx context.Context,
	ds datastore.Batching,
	orderId uint64,
	cid cid.Cid,
) error {
	key := datastore.NewKey(SHARD_INDEX_KEY)
	exists, err := ds.Has(ctx, key)
	if err != nil {
		return err
	}

	var index types.ShardIndex
	if exists {
		data, err := ds.Get(ctx, key)
		if err != nil {
			return err
		}
		err = index.UnmarshalCBOR(bytes.NewReader(data))
		if err != nil {
			return err
		}
	}
	index.All = append(index.All, types.ShardKey{
		OrderId: orderId,
		Cid:     cid,
	})

	buf := new(bytes.Buffer)
	err = index.MarshalCBOR(buf)
	if err != nil {
		return err
	}
	err = ds.Put(ctx, key, buf.Bytes())
	if err != nil {
		return err
	}
	return nil
}

/**
 * Get shard index from data store.
 */
func GetShardIndex(ctx context.Context, ds datastore.Batching) (types.ShardIndex, error) {
	key := datastore.NewKey(SHARD_INDEX_KEY)
	exists, err := ds.Has(ctx, key)
	if err != nil {
		return types.ShardIndex{}, err
	}
	if !exists {
		return types.ShardIndex{}, nil
	}

	data, err := ds.Get(ctx, key)
	if err != nil {
		return types.ShardIndex{}, err
	}

	var index types.ShardIndex
	err = index.UnmarshalCBOR(bytes.NewReader(data))
	return index, err
}
