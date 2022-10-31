package storage

import (
	"context"
	"fmt"
	"sao-storage-node/node/chain"
	"sao-storage-node/types"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/pkg/errors"
)

type CommitResult struct {
	OrderId  uint64
	DataId   string
	CommitId string
}

type PullResult struct {
	OrderId  uint64
	DataId   string
	Alias    string
	Tags     string
	CommitId string
	Content  []byte
	Cid      cid.Cid
	Type     types.ModelType
}

type CommitSvcApi interface {
	Commit(ctx context.Context, creator string, orderMeta types.OrderMeta, content []byte) (*CommitResult, error)
	Pull(ctx context.Context, key string) (*PullResult, error)
	Stop(ctx context.Context) error
}

type CommitSvc struct {
	ctx          context.Context
	chainSvc     *chain.ChainSvc
	nodeAddress  string
	db           datastore.Batching
	host         host.Host
	shardStaging *ShardStaging
}

const (
	ShardStoreProtocol = "/sao/store/shard/1.0"
)

func NewCommitSvc(ctx context.Context, nodeAddress string, chainSvc *chain.ChainSvc, db datastore.Batching, host host.Host, shardSharding *ShardStaging) *CommitSvc {
	cs := &CommitSvc{
		ctx:          ctx,
		chainSvc:     chainSvc,
		nodeAddress:  nodeAddress,
		db:           db,
		host:         host,
		shardStaging: shardSharding,
	}
	cs.host.SetStreamHandler(ShardStoreProtocol, cs.handleShardStore)
	return cs
}

func (cs *CommitSvc) Stop(ctx context.Context) error {
	log.Info("stop commit service")
	cs.host.RemoveStreamHandler(ShardStoreProtocol)
	return nil
}

func (cs *CommitSvc) handleShardStore(s network.Stream) {
	defer s.Close()

	// Set a deadline on reading from the stream so it doesn't hang
	_ = s.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer s.SetReadDeadline(time.Time{}) // nolint

	var req ShardStoreReq
	err := req.Unmarshal(s, "json")
	if err != nil {
		// TODO: respond error
	}
	log.Debugf("receive ShardStoreReq: orderId=%d cid=%v", req.OrderId, req.Cid)

	contentBytes, err := cs.shardStaging.GetStagedShard(req.Owner, req.Cid)
	if err != nil {
		log.Error(err)
		// TODO: respond error
	}
	var resp = &ShardStoreResp{
		OrderId: req.OrderId,
		Cid:     req.Cid,
		Content: contentBytes,
	}
	log.Debugf("send ShardStoreResp: Content=%v", string(contentBytes))
	err = resp.Marshal(s, "json")
	if err != nil {
		// TODO: respond error
	}
}

func (cs *CommitSvc) Commit(ctx context.Context, creator string, orderMeta types.OrderMeta, content []byte) (*CommitResult, error) {
	// TODO: consider store node may ask earlier than file split
	// TODO: if big data, consider store to staging dir.
	// TODO: support split file.
	// TODO: support marshal any content
	log.Infof("stage shard /%s/%v", creator, orderMeta.Cid)
	err := cs.shardStaging.StageShard(creator, orderMeta.Cid, content)
	if err != nil {
		return nil, err
	}

	if !orderMeta.TxSent {
		orderId, txId, err := cs.chainSvc.StoreOrder(cs.nodeAddress, creator, cs.nodeAddress, orderMeta.Cid, orderMeta.Duration, orderMeta.Replica)
		if err != nil {
			return nil, err
		}
		log.Infof("StoreOrder tx succeed. orderId=%d tx=%s", orderId, txId)
		orderMeta.OrderId = orderId
		orderMeta.TxId = txId
		orderMeta.TxSent = true
	} else {
		txId, err := cs.chainSvc.OrderReady(cs.nodeAddress, orderMeta.OrderId)
		if err != nil {
			return nil, err
		}
		orderMeta.TxId = txId
		orderMeta.TxSent = true
	}

	log.Infof("start SubscribeOrderComplete")
	doneChan := make(chan chain.OrderCompleteResult)
	err = cs.chainSvc.SubscribeOrderComplete(ctx, orderMeta.OrderId, doneChan)
	if err != nil {
		return nil, err
	}

	dataId := ""
	timeout := false
	select {
	case result := <-doneChan:
		dataId = result.DataId
	case <-time.After(chain.Blocktime * time.Duration(orderMeta.CompleteTimeoutBlocks)):
		timeout = true
	case <-ctx.Done():
		timeout = true
	}
	close(doneChan)

	err = cs.chainSvc.UnsubscribeOrderComplete(ctx, orderMeta.OrderId)
	if err != nil {
		log.Error(err)
	} else {
		log.Info("UnsubscribeOrderComplete")
	}

	log.Infof("unstage shard /%s/%v", creator, orderMeta.Cid)
	err = cs.shardStaging.UnstageShard(creator, orderMeta.Cid)
	if err != nil {
		return nil, err
	}

	if timeout {
		// TODO: timeout handling
		return nil, errors.Errorf("process order %d timeout.", orderMeta.OrderId)
	} else {
		log.Infof("order %d complete: dataId=%s", orderMeta.OrderId, dataId)
		return &CommitResult{
			OrderId: orderMeta.OrderId,
			DataId:  dataId,
		}, nil
	}
}
func (cs *CommitSvc) Pull(ctx context.Context, key string) (*PullResult, error) {
	return &PullResult{
		OrderId: 100,
		DataId:  "6666666",
		Content: []byte("sdafasdf"),
	}, nil
}

func orderShardDsKey(orderId uint64, cid cid.Cid) datastore.Key {
	return datastore.NewKey(fmt.Sprintf("order-%d-%v", orderId, cid))
}
