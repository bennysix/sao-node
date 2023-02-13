package gateway

import (
	"context"
	"sao-node/types"
)

type GatewayProtocol interface {
	RequestShardAssign(ctx context.Context, req types.ShardAssignReq, peer string) types.ShardAssignResp
	RequestShardLoad(ctx context.Context, req types.ShardLoadReq, peer string) types.ShardLoadResp
	Stop(ctx context.Context) error
}

type GatewayProtocolHandler interface {
	/**
	 * Resp:
	 * ErrorCodeInvalidTx - storage node should resubmit the right tx hash.
	 */
	HandleShardComplete(types.ShardCompleteReq) types.ShardCompleteResp

	HandleShardStore(types.ShardLoadReq) types.ShardLoadResp
}
