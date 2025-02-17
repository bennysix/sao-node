package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"sao-node/chain"
	"sao-node/store"
	"sao-node/types"
	"sao-node/utils"
	"strings"
	"time"

	sdktypes "github.com/cosmos/cosmos-sdk/types"

	ordertypes "github.com/SaoNetwork/sao/x/order/types"
	"golang.org/x/xerrors"

	saotypes "github.com/SaoNetwork/sao/x/sao/types"
	"github.com/cosmos/cosmos-sdk/types/tx"

	"github.com/dvsekhvalnov/jose2go/base64url"
	"github.com/ipfs/go-cid"
	"github.com/ipfs/go-datastore"

	"github.com/SaoNetwork/sao-did/sid"
	logging "github.com/ipfs/go-log/v2"

	saodid "github.com/SaoNetwork/sao-did"
	saodidtypes "github.com/SaoNetwork/sao-did/types"

	"github.com/libp2p/go-libp2p/core/host"
)

var log = logging.Logger("storage")

const (
	MAX_RETRIES = 3
)

type MigrateRequest struct {
	FromProvider  string
	OrderId       uint64
	DataId        string
	Cid           string
	ToProvider    string
	MigrateTxHash string
	MigrateHeight int64
}

type StoreSvc struct {
	nodeAddress        string
	chainSvc           *chain.ChainSvc
	taskChan           chan types.ShardInfo
	migrateChan        chan MigrateRequest
	host               host.Host
	stagingPath        string
	storeManager       *store.StoreManager
	ctx                context.Context
	orderDs            datastore.Batching
	storageProtocolMap map[string]StorageProtocol
}

func NewStoreService(
	ctx context.Context,
	nodeAddress string,
	chainSvc *chain.ChainSvc,
	host host.Host,
	stagingPath string,
	storeManager *store.StoreManager,
	notifyChan map[string]chan interface{},
	orderDs datastore.Batching,
) (*StoreSvc, error) {
	ss := &StoreSvc{
		nodeAddress:  nodeAddress,
		chainSvc:     chainSvc,
		taskChan:     make(chan types.ShardInfo),
		migrateChan:  make(chan MigrateRequest),
		host:         host,
		stagingPath:  stagingPath,
		storeManager: storeManager,
		ctx:          ctx,
		orderDs:      orderDs,
	}

	ss.storageProtocolMap = make(map[string]StorageProtocol)
	ss.storageProtocolMap["local"] = NewLocalStorageProtocol(
		ctx,
		notifyChan,
		stagingPath,
		ss,
	)
	ss.storageProtocolMap["stream"] = NewStreamStorageProtocol(host, ss)

	// wsevent way to receive shard assign
	//if err := ss.chainSvc.SubscribeShardTask(ctx, ss.nodeAddress, ss.taskChan); err != nil {
	//	return nil, err
	//}

	go ss.processIncompleteShards(ctx)
	go ss.processMigrateLoop(ctx)

	return ss, nil
}

func (ss *StoreSvc) processMigrateLoop(ctx context.Context) {
	for {
		select {
		case migrateReq := <-ss.migrateChan:
			err := ss.processMigrate(ctx, migrateReq)
			if err != nil {
				log.Error(err)
			}
		case <-ctx.Done():
			return
		}
	}
}

func (ss *StoreSvc) processMigrate(ctx context.Context, req MigrateRequest) error {
	cid, err := cid.Decode(req.Cid)
	if err != nil {
		return err
	}
	reader, err := ss.storeManager.Get(ss.ctx, cid)
	if err != nil {
		return err
	}
	shardContent, err := io.ReadAll(reader)
	if err != nil {
		return err
	}

	peer, err := ss.chainSvc.GetNodePeer(ctx, req.ToProvider)
	if err != nil {
		return err
	}
	p := ss.storageProtocolMap["stream"]
	resp := p.RequestShardMigrate(ctx, types.ShardMigrateReq{
		MigrateFrom: req.FromProvider,
		OrderId:     req.OrderId,
		DataId:      req.DataId,
		TxHash:      req.MigrateTxHash,
		Cid:         req.Cid,
		Content:     shardContent,
	}, peer)
	if resp.Code != 0 {
		return xerrors.Errorf(resp.Message)
	}

	// validate transaction
	resultTx, err := ss.chainSvc.GetTx(ctx, resp.CompleteHash, resp.CompleteHeight)
	if err != nil {
		return err
	}

	if resultTx.TxResult.Code != 0 {
		return xerrors.Errorf("complete tx %s failed: code=%d", resultTx.Hash, resultTx.TxResult.Code)
	}

	// validate order information after migration
	order, err := ss.chainSvc.GetOrder(ctx, req.OrderId)
	if err != nil {
		return err
	}
	shard, exists := order.Shards[req.ToProvider]
	if !exists {
		return xerrors.Errorf("no shard assigned to new provider %s", req.ToProvider)
	}

	if shard.From != req.FromProvider {
		return xerrors.Errorf("shard is migrated from old provider %s", req.FromProvider)
	}

	if shard.Status != ordertypes.ShardCompleted {
		return xerrors.Errorf("shard status should be ShardCompleted, but is %d", shard.Status)
	}
	log.Info("migrate response validate pass.")

	migrateInfo, err := utils.GetMigrate(ss.ctx, ss.orderDs, req.DataId, req.FromProvider)
	if err != nil {
		log.Error("get migrate error: ", err)
	} else {
		migrateInfo.State = types.MigrateStateComplete
		migrateInfo.CompleteTxHash = resp.CompleteHash
		migrateInfo.CompleteTxHeight = resp.CompleteHeight
		err = utils.SaveMigrate(ss.ctx, ss.orderDs, migrateInfo)
		if err != nil {
			log.Error("save migrate error: ", err)
		}
	}

	return nil
}

func (ss *StoreSvc) processIncompleteShards(ctx context.Context) {
	log.Info("processing pending shards...")
	pendings, err := ss.getPendingShardList(ctx)
	if err != nil {
		log.Errorf("process pending shards error: %v", err)
	}
	for _, p := range pendings {
		ss.taskChan <- p
	}
}

func (ss *StoreSvc) HandleShardMigrate(req types.ShardMigrateReq) types.ShardMigrateResp {
	logAndRespond := func(code uint64, errMsg string) types.ShardMigrateResp {
		log.Error(errMsg)
		return types.ShardMigrateResp{
			Code:    code,
			Message: errMsg,
		}
	}

	resultTx, err := ss.chainSvc.GetTx(ss.ctx, req.TxHash, req.TxHeight)
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("Get tx %s error: ", req.TxHash),
		)
	}

	if resultTx.TxResult.Code != 0 {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("Tx %s failed with code: %d", req.TxHash, resultTx.TxResult.Code),
		)
	}

	var txMsgData sdktypes.TxMsgData
	err = txMsgData.Unmarshal(resultTx.TxResult.Data)
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("unmarshal tx error: %v", err),
		)
	}

	mr := saotypes.MsgMigrateResponse{}
	err = mr.Unmarshal(txMsgData.MsgResponses[0].Value)
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("unmarshal tx error: %v", err),
		)
	}
	m, exists := mr.Result[req.DataId]
	if !exists {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("invalid data id: given dataId %s not in tx %s", req.DataId, req.TxHash),
		)
	}
	if !strings.HasPrefix(m, "SUCCESS") {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("dataId migrate fails: %s", m),
		)
	}
	order, err := ss.chainSvc.GetOrder(ss.ctx, req.OrderId)
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("get order %d error: %d", order.Id, err),
		)
	}
	shard, exists := order.Shards[ss.nodeAddress]
	if !exists {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("no shard to current provider %s", ss.nodeAddress),
		)
	}
	if shard.From != req.MigrateFrom {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("unmatched migrate from: expected %s, actual %s", req.MigrateFrom, shard.From),
		)
	}
	if shard.Cid != req.Cid {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("unmatched cid: expected %s, actual %s", req.Cid, shard.Cid),
		)
	}
	if shard.Status != ordertypes.ShardWaiting {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("shard status is not invalid, expected ShardWaiting, actual %d", shard.Status),
		)
	}

	cid, err := cid.Decode(shard.Cid)
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("invalid cid %s error: %v", shard.Cid, err),
		)
	}
	// TODO: size check
	_, err = ss.storeManager.Store(ss.ctx, cid, bytes.NewReader(req.Content))
	if err != nil {
		return logAndRespond(types.ErrorCodeInternalErr, fmt.Sprintf("store cid %s error: %v", cid, err))
	}
	// send tx
	txHash, height, err := ss.chainSvc.CompleteOrder(ss.ctx, ss.nodeAddress, order.Id, cid, uint64(len(req.Content)))
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInvalidTx,
			fmt.Sprintf("complete order tx failed: %v", err),
		)
	}

	return types.ShardMigrateResp{
		Code:           0,
		CompleteHash:   txHash,
		CompleteHeight: height,
	}
}

func (ss *StoreSvc) HandleShardLoad(req types.ShardLoadReq, remotePeerId string) types.ShardLoadResp {
	logAndRespond := func(code uint64, errMsg string) types.ShardLoadResp {
		log.Error(errMsg)
		return types.ShardLoadResp{
			Code:       code,
			Message:    errMsg,
			OrderId:    req.OrderId,
			Cid:        req.Cid,
			RequestId:  req.RequestId,
			ResponseId: time.Now().UnixMilli(),
		}
	}

	didManager, err := saodid.NewDidManagerWithDid(req.Proposal.Proposal.Owner, ss.getSidDocFunc())
	if err != nil {
		return logAndRespond(types.ErrorCodeInternalErr, fmt.Sprintf("invalid did: %v", err))
	}

	p := saotypes.QueryProposal{
		Owner:           req.Proposal.Proposal.Owner,
		Keyword:         req.Proposal.Proposal.Keyword,
		GroupId:         req.Proposal.Proposal.GroupId,
		KeywordType:     uint32(req.Proposal.Proposal.KeywordType),
		LastValidHeight: req.Proposal.Proposal.LastValidHeight,
		Gateway:         req.Proposal.Proposal.Gateway,
		CommitId:        req.Proposal.Proposal.CommitId,
		Version:         req.Proposal.Proposal.Version,
	}

	proposalBytes, err := p.Marshal()
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("marshal error: %v", err),
		)
	}

	_, err = didManager.VerifyJWS(saodidtypes.GeneralJWS{
		Payload: base64url.Encode(proposalBytes),
		Signatures: []saodidtypes.JwsSignature{
			saodidtypes.JwsSignature(req.Proposal.JwsSignature),
		},
	})

	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("verify client order proposal signature failed: %v", err),
		)
	}

	log.Debugf("check peer: %s<->%s", req.Proposal.Proposal.Gateway, remotePeerId)
	if !strings.Contains(req.Proposal.Proposal.Gateway, remotePeerId) {
		if len(req.RelayProposal.Signature) > 0 && strings.Contains(req.RelayProposal.Proposal.RelayPeerIds, remotePeerId) {
			account, err := ss.chainSvc.GetAccount(ss.ctx, req.RelayProposal.Proposal.NodeAddress)
			if err != nil {
				return logAndRespond(
					types.ErrorCodeInternalErr,
					fmt.Sprintf("failed to get gateway account info: %v", err),
				)
			}
			buf := new(bytes.Buffer)
			err = req.RelayProposal.Proposal.MarshalCBOR(buf)
			if err != nil {
				return logAndRespond(
					types.ErrorCodeInternalErr,
					fmt.Sprintf("failed marshal relay proposal: %v", err),
				)
			}
			account.GetPubKey().VerifySignature(buf.Bytes(), req.RelayProposal.Signature)
		} else {
			return logAndRespond(
				types.ErrorCodeInternalErr,
				fmt.Sprintf("invalid query, unexpect gateway:%s, should be %s", remotePeerId, req.Proposal.Proposal.Gateway),
			)
		}
	}

	lastHeight, err := ss.chainSvc.GetLastHeight(ss.ctx)
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("get chain height error: %v", err),
		)
	}

	if req.Proposal.Proposal.LastValidHeight < uint64(lastHeight) {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("invalid query, LastValidHeight:%d > now:%d", req.Proposal.Proposal.LastValidHeight, lastHeight),
		)
	}

	log.Debugf("Get %v", req.Cid)
	reader, err := ss.storeManager.Get(ss.ctx, req.Cid)
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("get %v from store error: %v", req.Cid, err),
		)
	}
	shardContent, err := io.ReadAll(reader)
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("get %v from store error: %v", req.Cid, err),
		)
	}

	return types.ShardLoadResp{
		OrderId:    req.OrderId,
		Cid:        req.Cid,
		Content:    shardContent,
		RequestId:  req.RequestId,
		ResponseId: time.Now().UnixMilli(),
	}
}

func (ss *StoreSvc) HandleShardAssign(req types.ShardAssignReq) types.ShardAssignResp {
	logAndRespond := func(code uint64, errMsg string) types.ShardAssignResp {
		log.Error(errMsg)
		return types.ShardAssignResp{
			Code:    code,
			Message: errMsg,
		}
	}

	// validate request
	if req.Assignee != ss.nodeAddress {
		return logAndRespond(
			types.ErrorCodeInvalidShardAssignee,
			fmt.Sprintf("shard assignee is %s, but current node is %s", req.Assignee, ss.nodeAddress),
		)
	}

	resultTx, err := ss.chainSvc.GetTx(ss.ctx, req.TxHash, req.Height)
	if err != nil {
		return logAndRespond(
			types.ErrorCodeInternalErr,
			fmt.Sprintf("internal error: %v", err),
		)
	}

	if resultTx.TxResult.Code == 0 {
		txb := tx.Tx{}
		err = txb.Unmarshal(resultTx.Tx)
		if err != nil {
			return logAndRespond(
				types.ErrorCodeInvalidTx,
				fmt.Sprintf("tx %s body is invalid.", resultTx.Tx),
			)
		}

		// validate tx
		if req.AssignTxType == types.AssignTxTypeStore {
			m := saotypes.MsgStore{}
			err = m.Unmarshal(txb.Body.Messages[0].Value)
		} else {
			m := saotypes.MsgReady{}
			err = m.Unmarshal(txb.Body.Messages[0].Value)
		}
		if err != nil {
			return logAndRespond(
				types.ErrorCodeInvalidTx,
				fmt.Sprintf("tx %s body is invalid.", resultTx.Tx),
			)
		}

		order, err := ss.chainSvc.GetOrder(ss.ctx, req.OrderId)
		if err != nil {
			return logAndRespond(
				types.ErrorCodeInternalErr,
				fmt.Sprintf("internal error: %v", err),
			)
		}

		var shardCids []string
		for key, shard := range order.Shards {
			if key == ss.nodeAddress {
				shardCids = append(shardCids, shard.Cid)
			}
		}
		if len(shardCids) <= 0 {
			return logAndRespond(
				types.ErrorCodeInvalidProvider,
				fmt.Sprintf("order %d doesn't have shard provider %s", req.OrderId, ss.nodeAddress),
			)
		}
		for _, shardCid := range shardCids {
			cid, err := cid.Decode(shardCid)
			if err != nil {
				return logAndRespond(
					types.ErrorCodeInvalidShardCid,
					fmt.Sprintf("invalid cid %s", shardCid),
				)
			}

			shardInfo, _ := utils.GetShard(ss.ctx, ss.orderDs, req.OrderId, cid)
			if (types.ShardInfo{} == shardInfo) {
				shardInfo = types.ShardInfo{
					Owner:          order.Owner,
					OrderId:        req.OrderId,
					Gateway:        order.Provider,
					Cid:            cid,
					DataId:         req.DataId,
					OrderOperation: fmt.Sprintf("%d", order.Operation),
					ShardOperation: fmt.Sprintf("%d", order.Operation),
					State:          types.ShardStateValidated,
					ExpireHeight:   uint64(order.Expire),
				}
				err = utils.SaveShard(ss.ctx, ss.orderDs, shardInfo)
				if err != nil {
					// do not throw error, the best case is storage node handle shard again.
					log.Warn("put shard order=%d cid=%v error: %v", shardInfo.OrderId, shardInfo.Cid, err)
				}
			}
			ss.taskChan <- shardInfo
		}
		return types.ShardAssignResp{Code: 0}
	} else {
		return logAndRespond(
			types.ErrorCodeInvalidTx,
			fmt.Sprintf("tx %s body is invalid.", resultTx.Tx),
		)
	}
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
				// TODO: retry mechanism
				log.Error(err)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

func (ss *StoreSvc) process(ctx context.Context, task types.ShardInfo) error {
	log.Infof("start processing: order id=%d gateway=%s shard_cid=%v", task.OrderId, task.Gateway, task.Cid)

	if task.State == types.ShardStateTerminate {
		return nil
	}

	task.Tries++
	if task.Tries >= MAX_RETRIES {
		task.State = types.ShardStateTerminate
		errMsg := fmt.Sprintf("order %d shard %v too many retries %d", task.OrderId, task.DataId, task.Tries)
		ss.updateShardError(task, xerrors.Errorf(errMsg))
		return types.Wrapf(types.ErrRetriesExceed, errMsg)
	}

	if task.ExpireHeight > 0 {
		latestHeight, err := ss.chainSvc.GetLastHeight(ctx)
		if err != nil {
			return err
		}

		if latestHeight > int64(task.ExpireHeight) {
			task.State = types.ShardStateTerminate
			errStr := fmt.Sprintf("order expired: latest=%d expireAt=%d", latestHeight, task.ExpireHeight)
			ss.updateShardError(task, xerrors.Errorf(errStr))
			return types.Wrapf(types.ErrExpiredOrder, errStr)
		}
	}

	sp, peerInfo, err := ss.getStorageProtocolAndPeer(ctx, task.Gateway)
	if err != nil {
		ss.updateShardError(task, err)
		return err
	}

	if task.State < types.ShardStateStored {
		// check if it's a renew order(Operation is 3)
		if task.OrderOperation != "3" || task.ShardOperation != "3" {
			resp := sp.RequestShardStore(ctx, types.ShardLoadReq{
				Owner:   task.Owner,
				OrderId: task.OrderId,
				Cid:     task.Cid,
			}, peerInfo)
			if resp.Code != 0 {
				ss.updateShardError(task, types.Wrapf(types.ErrFailuresResponsed, resp.Message))
				return types.Wrapf(types.ErrFailuresResponsed, resp.Message)
			} else {
				cid, _ := utils.CalculateCid(resp.Content)
				log.Debugf("ipfs cid %v, task cid %v, order id %v", cid, task.Cid, task.OrderId)
				if cid.String() != task.Cid.String() {
					ss.updateShardError(task, err)
					return types.Wrapf(types.ErrInvalidCid, "ipfs cid %v != task cid %v", cid, task.Cid)
				}
			}

			// store to backends
			_, err = ss.storeManager.Store(ctx, task.Cid, bytes.NewReader(resp.Content))
			if err != nil {
				ss.updateShardError(task, err)
				return types.Wrap(types.ErrStoreFailed, err)
			}
			task.Size = uint64(len(resp.Content))
		} else {
			// make sure the data is still there
			isExist := ss.storeManager.IsExist(ctx, task.Cid)
			if !isExist {
				ss.updateShardError(task, err)
				return types.Wrapf(types.ErrDataMissing, "shard with cid %s not found", task.Cid)
			}
		}
		task.State = types.ShardStateStored
		err = utils.SaveShard(ctx, ss.orderDs, task)
		if err != nil {
			log.Warnf("put shard order=%d cid=%v error: %v", task.OrderId, task.Cid, err)
		}
	}

	if task.State < types.ShardStateTxSent {
		txHash, height, err := ss.chainSvc.CompleteOrder(ctx, ss.nodeAddress, task.OrderId, task.Cid, task.Size)
		if err != nil {
			ss.updateShardError(task, err)
			return err
		}
		log.Infof("Complete order succeed: txHash: %s, OrderId: %d, cid: %s", txHash, task.OrderId, task.Cid)

		task.State = types.ShardStateComplete
		task.CompleteHash = txHash
		task.CompleteHeight = height
		err = utils.SaveShard(ss.ctx, ss.orderDs, task)
		if err != nil {
			log.Warnf("put shard order=%d cid=%v error: %v", task.OrderId, task.Cid, err)
		}
	}

	resp := sp.RequestShardComplete(ctx, types.ShardCompleteReq{
		OrderId: task.OrderId,
		DataId:  task.DataId,
		Cids:    []cid.Cid{task.Cid},
		Height:  task.CompleteHeight,
		TxHash:  task.CompleteHash,
	}, peerInfo)
	if resp.Code != 0 {
		ss.updateShardError(task, types.Wrapf(types.ErrFailuresResponsed, resp.Message))
		// return types.Wrapf(types.ErrFailuresResponsed, resp.Message)
	}
	if task.State < types.ShardStateComplete {
		task.State = types.ShardStateComplete
		err = utils.SaveShard(ss.ctx, ss.orderDs, task)
		if err != nil {
			log.Warnf("put shard order=%d cid=%v error: %v", task.OrderId, task.Cid, err)
		}
	}
	return nil
}

func (ss *StoreSvc) Stop(ctx context.Context) error {
	// TODO: wsevent
	//if err := ss.chainSvc.UnsubscribeShardTask(ctx, ss.nodeAddress); err != nil {
	//	return err
	//}
	log.Info("stopping storage service...")
	close(ss.taskChan)

	var err error
	for k, p := range ss.storageProtocolMap {
		err = p.Stop(ctx)
		if err != nil {
			log.Errorf("stopping %s storage protocol failed: %v", k, err)
		} else {
			log.Infof("%s storage protocol stopped.", k)
		}
	}

	return nil
}

func (ss *StoreSvc) getSidDocFunc() func(versionId string) (*sid.SidDocument, error) {
	return func(versionId string) (*sid.SidDocument, error) {
		return ss.chainSvc.GetSidDocument(ss.ctx, versionId)
	}
}

func (ss *StoreSvc) getStorageProtocolAndPeer(
	ctx context.Context,
	targetAddress string,
) (StorageProtocol, string, error) {
	var sp StorageProtocol
	var err error
	peer := ""
	if targetAddress == ss.nodeAddress {
		sp = ss.storageProtocolMap["local"]
	} else {
		sp = ss.storageProtocolMap["stream"]
		peer, err = ss.chainSvc.GetNodePeer(ctx, targetAddress)
	}
	return sp, peer, err
}

func (ss *StoreSvc) updateShardError(shard types.ShardInfo, err error) {
	shard.LastErr = err.Error()
	err = utils.SaveShard(ss.ctx, ss.orderDs, shard)
	if err != nil {
		log.Warnf("put shard order=%d cid=%v error: %v", shard.OrderId, shard.Cid, err)
	}

}

func (ss *StoreSvc) ShardStatus(ctx context.Context, orderId uint64, cid cid.Cid) (types.ShardInfo, error) {
	return utils.GetShard(ctx, ss.orderDs, orderId, cid)
}

func (ss *StoreSvc) getPendingShardList(ctx context.Context) ([]types.ShardInfo, error) {
	shardKeys, err := ss.getShardKeyList(ctx)
	if err != nil {
		return nil, err
	}
	// TODO: optimize add a pending list in OrderShards
	var pending []types.ShardInfo
	for _, shardKey := range shardKeys {
		shard, err := utils.GetShard(ctx, ss.orderDs, shardKey.OrderId, shardKey.Cid)
		if err != nil {
			return nil, err
		}
		if shard.State != types.ShardStateComplete && shard.State != types.ShardStateTerminate {
			pending = append(pending, shard)
		}
	}
	return pending, nil
}

func (ss *StoreSvc) getShardKeyList(ctx context.Context) ([]types.ShardKey, error) {
	index, err := utils.GetShardIndex(ctx, ss.orderDs)
	if err != nil {
		return nil, err
	}
	return index.All, nil
}

func (ss *StoreSvc) ShardList(ctx context.Context) ([]types.ShardInfo, error) {
	shardKeys, err := ss.getShardKeyList(ctx)
	if err != nil {
		return nil, err
	}

	var shardInfos []types.ShardInfo
	for _, shardKey := range shardKeys {
		shard, err := utils.GetShard(ctx, ss.orderDs, shardKey.OrderId, shardKey.Cid)
		if err != nil {
			return nil, err
		}
		shardInfos = append(shardInfos, shard)
	}
	return shardInfos, nil
}

func (ss *StoreSvc) ShardFix(ctx context.Context, orderId uint64, cid cid.Cid) error {
	shardInfo, err := utils.GetShard(ctx, ss.orderDs, orderId, cid)
	if err != nil {
		return nil
	}

	ss.taskChan <- shardInfo
	return nil
}

func (ss *StoreSvc) Migrate(ctx context.Context, dataIds []string) (string, map[string]string, error) {
	hash, results, height, err := ss.chainSvc.MigrateOrder(ctx, ss.nodeAddress, dataIds)

	for k, v := range results {
		if strings.HasPrefix(v, "SUCCESS") {
			// save migrate job
			mi := types.MigrateInfo{
				DataId:          k,
				FromProvider:    ss.nodeAddress,
				MigrateTxHash:   hash,
				MigrateTxHeight: height,
				State:           types.MigrateStateTxSent,
			}
			err := utils.SaveMigrate(ctx, ss.orderDs, mi)
			if err != nil {
				log.Errorf("save migrate error: %v", err)
			}

			resp, err := ss.chainSvc.GetMeta(ctx, k)
			if err != nil {
				log.Error(err)
			}
			order, err := ss.chainSvc.GetOrder(ctx, resp.OrderId)
			if err != nil {
				log.Error(err)
			}

			cid := order.Shards[ss.nodeAddress].Cid
			for node, shard := range order.Shards {
				if shard.Cid == cid &&
					node != ss.nodeAddress &&
					shard.Status == ordertypes.ShardWaiting &&
					shard.From == ss.nodeAddress {

					mi.OrderId = order.Id
					mi.ToProvider = node
					mi.Cid = shard.Cid
					err = utils.SaveMigrate(ctx, ss.orderDs, mi)
					if err != nil {
						log.Error("save migrate error: ", err)
					}

					ss.migrateChan <- MigrateRequest{
						OrderId:       order.Id,
						FromProvider:  ss.nodeAddress,
						DataId:        k,
						Cid:           shard.Cid,
						ToProvider:    node,
						MigrateTxHash: hash,
						MigrateHeight: height,
					}
					break
				}
			}

		}
	}
	return hash, results, err
}

func (ss *StoreSvc) MigrateList(ctx context.Context) ([]types.MigrateInfo, error) {
	migrateKeys, err := ss.getMigrateKeyList(ctx)
	if err != nil {
		return nil, err
	}

	var migrateInfos []types.MigrateInfo
	for _, migrateKey := range migrateKeys {
		migrate, err := utils.GetMigrate(ctx, ss.orderDs, migrateKey.DataId, migrateKey.FromProvider)
		if err != nil {
			return nil, err
		}
		migrateInfos = append(migrateInfos, migrate)
	}
	return migrateInfos, nil
}

func (ss *StoreSvc) getMigrateKeyList(ctx context.Context) ([]types.MigrateKey, error) {
	index, err := utils.GetMigrateIndex(ctx, ss.orderDs)
	if err != nil {
		return nil, err
	}
	return index.All, nil
}
