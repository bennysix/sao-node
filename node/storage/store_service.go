package storage

import (
	"context"
	"sao-storage-node/node/chain"

	"github.com/ipfs/go-cid"

	logging "github.com/ipfs/go-log/v2"

	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/peer"
	ma "github.com/multiformats/go-multiaddr"
)

var log = logging.Logger("storage")

type StoreSvc struct {
	nodeAddress  string
	chainSvc     *chain.ChainSvc
	taskChan     chan *chain.ShardTask
	host         host.Host
	shardStaging *ShardStaging
}

func NewStoreService(ctx context.Context, nodeAddress string, chainSvc *chain.ChainSvc, host host.Host, shardStaging *ShardStaging) (*StoreSvc, error) {
	ss := StoreSvc{
		nodeAddress:  nodeAddress,
		chainSvc:     chainSvc,
		taskChan:     make(chan *chain.ShardTask),
		host:         host,
		shardStaging: shardStaging,
	}
	if err := ss.chainSvc.SubscribeShardTask(ctx, ss.nodeAddress, ss.taskChan); err != nil {
		return nil, err
	}

	return &ss, nil
}

func (ss *StoreSvc) Start(ctx context.Context) error {
	for {
		select {
		case t, ok := <-ss.taskChan:
			if !ok {
				return nil
			}
			err := ss.process(ctx, t)
			if err != nil {
				log.Error(err)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (ss *StoreSvc) process(ctx context.Context, task *chain.ShardTask) error {
	log.Infof("processing task: order id=%d gateway=%s shard_cid=%v", task.OrderId, task.Gateway, task.Cid)

	var shard []byte
	var err error

	log.Info("task.Gateway: ", task.Gateway)
	log.Info("ss.nodeAddress: ", ss.nodeAddress)

	// check if gateway is node itself
	if task.Gateway == ss.nodeAddress {
		shard, err = ss.getShardFromLocal(task.OrderId, task.Cid)
		if err != nil {
			log.Error("known err: ", err.Error())
			// return err
		}
	} else {
		shard, err = ss.getShardFromGateway(ctx, task.Gateway, task.OrderId, task.Cid)
		if err != nil {
			return err
		}
	}
	// TODO: store resp.Content to ipfs

	txHash, err := ss.chainSvc.CompleteOrder(ss.nodeAddress, task.OrderId, task.Cid, int32(len(shard)))
	if err != nil {
		return err
	}
	log.Infof("Complete order succeed: txHash:%s, OrderId: ", txHash, task.OrderId)
	return nil
}

func (ss *StoreSvc) getShardFromLocal(orderId uint64, cid cid.Cid) ([]byte, error) {
	return ss.shardStaging.GetStagedShard(orderId, cid)
}

func (ss *StoreSvc) getShardFromGateway(ctx context.Context, gateway string, orderId uint64, cid cid.Cid) ([]byte, error) {
	conn, err := ss.chainSvc.GetNodePeer(ctx, gateway)
	if err != nil {
		return nil, err
	}

	a, err := ma.NewMultiaddr(conn)
	if err != nil {
		return nil, err
	}
	pi, err := peer.AddrInfoFromP2pAddr(a)
	if err != nil {
		return nil, err
	}
	err = ss.host.Connect(ctx, *pi)
	if err != nil {
		return nil, err
	}
	stream, err := ss.host.NewStream(ctx, pi.ID, ShardStoreProtocol)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	log.Infof("open stream(%s) to gateway %s", ShardStoreProtocol, conn)

	req := ShardStoreReq{
		OrderId: orderId,
		Cid:     cid,
	}
	log.Debugf("send ShardStoreReq: orderId=%d cid=%v", req.OrderId, req.Cid)
	var resp ShardStoreResp
	if err = DoRpc(ctx, stream, &req, &resp, "json"); err != nil {
		// TODO: handle error
		return nil, err
	}
	log.Debugf("receive ShardStoreResp: content=%s", string(resp.Content))
	return resp.Content, nil
}

func (ss *StoreSvc) Stop(ctx context.Context) error {
	close(ss.taskChan)
	if err := ss.chainSvc.UnsubscribeShardTask(ctx, ss.nodeAddress); err != nil {
		return err
	}
	return nil
}
