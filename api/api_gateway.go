package api

import (
	"context"
	apitypes "sao-storage-node/api/types"
	"sao-storage-node/types"
)

type GatewayApi interface {
	Test(ctx context.Context, msg string) (string, error)
	Create(ctx context.Context, orderMeta types.OrderMeta, content []byte) (apitypes.CreateResp, error)
	CreateFile(ctx context.Context, orderMeta types.OrderMeta) (apitypes.CreateResp, error)
	Load(ctx context.Context, onwer string, key string, group string) (apitypes.LoadResp, error)
	Delete(ctx context.Context, onwer string, key string, group string) (apitypes.DeleteResp, error)
	Update(ctx context.Context, orderMeta types.OrderMeta, patch []byte) (apitypes.UpdateResp, error)
	GetPeerInfo(ctx context.Context) (apitypes.GetPeerInfoResp, error)
	NodeAddress(ctx context.Context) (string, error)
}
