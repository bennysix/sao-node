package order

import (
	"context"
	"sao-storage-node/node/transport"
	"sao-storage-node/types"
	"sync"
	"time"

	"github.com/ipfs/go-cid"
	"github.com/libp2p/go-libp2p/core/host"
	"github.com/libp2p/go-libp2p/core/network"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/multiformats/go-multiaddr"
)

type ShardStreamHandler struct {
	ctx         context.Context
	host        host.Host
	stagingPath string
}

var (
	handler *ShardStreamHandler
	once    sync.Once
)

func NewShardStreamHandler(ctx context.Context, host host.Host, path string) *ShardStreamHandler {
	once.Do(func() {
		handler = &ShardStreamHandler{
			ctx:         ctx,
			host:        host,
			stagingPath: path,
		}

		host.SetStreamHandler(types.ShardStoreProtocol, handler.HandleShardStream)
	})

	return handler
}

func (ssh *ShardStreamHandler) HandleShardStream(s network.Stream) {
	defer s.Close()

	// Set a deadline on reading from the stream so it doesn't hang
	_ = s.SetReadDeadline(time.Now().Add(10 * time.Second))
	defer s.SetReadDeadline(time.Time{}) // nolint

	var req types.ShardStoreReq
	err := req.Unmarshal(s, "json")
	if err != nil {
		log.Error(err)
		// TODO: respond error
		return
	}
	log.Debugf("receive ShardStoreReq: orderId=%d cid=%v", req.OrderId, req.Cid)

	contentBytes, err := GetStagedShard(ssh.stagingPath, req.Owner, req.Cid)
	if err != nil {
		log.Error(err)
		// TODO: respond error
		return
	}
	var resp = &types.ShardStoreResp{
		OrderId: req.OrderId,
		Cid:     req.Cid,
		Content: contentBytes,
	}
	log.Debugf("send ShardStoreResp: Content=%v", string(contentBytes))

	err = resp.Marshal(s, "json")
	if err != nil {
		log.Error(err.Error())
		return
	}

	if err := s.CloseWrite(); err != nil {
		log.Error(err.Error())
		return
	}
}

func (ssh *ShardStreamHandler) Fetch(addr string, shardCid cid.Cid) (*FetchResult, error) {
	a, err := multiaddr.NewMultiaddr(addr)
	if err != nil {
		return nil, err
	}
	pi, err := peer.AddrInfoFromP2pAddr(a)
	if err != nil {
		return nil, err
	}
	err = ssh.host.Connect(ssh.ctx, *pi)
	if err != nil {
		return nil, err
	}
	stream, err := ssh.host.NewStream(ssh.ctx, pi.ID, types.ShardStoreProtocol)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	log.Infof("open stream(%s) to storage node %s", types.ShardStoreProtocol, addr)

	req := types.ShardStoreReq{
		Cid: shardCid,
	}
	log.Infof("send ShardStoreReq with cid:%v, to the storage node %s", req.Cid, addr)

	var resp types.ShardStoreResp
	if err = transport.DoTransport(ssh.ctx, stream, &req, &resp, "json"); err != nil {
		return nil, err
	}

	log.Debugf("receive ShardStoreResp with content length:%d, from the storage node %s", len(resp.Content), addr)

	return &FetchResult{
		Cid:     resp.Cid.String(),
		Content: resp.Content,
	}, nil
}

func (ssh *ShardStreamHandler) Stop(ctx context.Context) error {
	log.Info("stopping shard stream handler...")
	ssh.host.RemoveStreamHandler(types.ShardStoreProtocol)
	return nil
}
