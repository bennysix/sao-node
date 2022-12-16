package model

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"sao-node/node/cache"
	"sao-node/node/config"
	"sao-node/node/gateway"
	"sao-node/node/model/schema/validator"
	"sao-node/types"
	"sao-node/utils"
	"strconv"
	"strings"
	"sync"

	saotypes "github.com/SaoNetwork/sao/x/sao/types"

	logging "github.com/ipfs/go-log/v2"
	jsoniter "github.com/json-iterator/go"
	"golang.org/x/xerrors"
)

const PROPERTY_CONTEXT = "@context"
const PROPERTY_TYPE = "@type"
const MODEL_TYPE_FILE = "File"

var log = logging.Logger("model")

type ModelManager struct {
	CacheCfg *config.Cache
	CacheSvc cache.CacheSvcApi
	// used by gateway module
	GatewaySvc gateway.GatewaySvcApi
}

var (
	modelManager *ModelManager
	once         sync.Once
)

func NewModelManager(cacheCfg *config.Cache, gatewaySvc gateway.GatewaySvcApi) *ModelManager {
	once.Do(func() {
		var cacheSvc cache.CacheSvcApi
		if cacheCfg.RedisConn == "" && cacheCfg.MemcachedConn == "" {
			cacheSvc = cache.NewLruCacheSvc()
		} else if cacheCfg.RedisConn != "" {
			cacheSvc = cache.NewRedisCacheSvc(cacheCfg.RedisConn, cacheCfg.RedisPassword, cacheCfg.RedisPoolSize)
		} else if cacheCfg.MemcachedConn != "" {
			cacheSvc = cache.NewMemcachedCacheSvc(cacheCfg.MemcachedConn)
		}

		modelManager = &ModelManager{
			CacheCfg:   cacheCfg,
			CacheSvc:   cacheSvc,
			GatewaySvc: gatewaySvc,
		}
	})

	return modelManager
}

func (mm *ModelManager) Stop(ctx context.Context) error {
	log.Info("stopping model manager...")

	mm.GatewaySvc.Stop(ctx)

	return nil
}

func (mm *ModelManager) Load(ctx context.Context, req *types.MetadataProposal) (*types.Model, error) {
	log.Info("KeyWord:", req.Proposal.Keyword)
	meta, err := mm.GatewaySvc.QueryMeta(ctx, req, 0)
	if err != nil {
		return nil, xerrors.Errorf(err.Error())
	}

	version := req.Proposal.Version
	if req.Proposal.Version != "" {
		match, err := regexp.Match(`^v\d+$`, []byte(req.Proposal.Version))
		if err != nil || !match {
			return nil, xerrors.Errorf("invalid Version: %s", req.Proposal.Version)
		}

		index, err := strconv.Atoi(strings.ReplaceAll(req.Proposal.Version, "v", ""))
		if err != nil {
			return nil, xerrors.Errorf(err.Error())
		}

		if len(meta.Commits) > index {
			commit := meta.Commits[index]
			commitInfo := strings.Split(meta.Commits[index], "\032")
			if len(commitInfo) != 2 || len(commitInfo[1]) == 0 {
				return nil, xerrors.Errorf("invalid commit information: %s", commit)
			}
			height, err := strconv.ParseInt(commitInfo[1], 10, 64)
			if err != nil {
				return nil, xerrors.Errorf(err.Error())
			}
			meta, err = mm.GatewaySvc.QueryMeta(ctx, req, height)
			if err != nil {
				return nil, xerrors.Errorf(err.Error())
			}
		} else {
			return nil, xerrors.Errorf("invalid Version: %s", req.Proposal.Version)
		}
	} else {
		version = fmt.Sprintf("v%d", len(meta.Commits)-1)
	}

	if req.Proposal.CommitId != "" {
		isFound := false
		for i, commit := range meta.Commits {
			commitInfo := strings.Split(commit, "\032")
			if len(commitInfo) != 2 || len(commitInfo[1]) == 0 {
				return nil, xerrors.Errorf("invalid commit information: %s", commit)
			}

			if commitInfo[0] == req.Proposal.CommitId {
				height, err := strconv.ParseInt(commitInfo[1], 10, 64)
				if err != nil {
					return nil, xerrors.Errorf(err.Error())
				}
				meta, err = mm.GatewaySvc.QueryMeta(ctx, req, height)
				if err != nil {
					return nil, xerrors.Errorf(err.Error())
				}

				version = fmt.Sprintf("v%d", i)
				isFound = true
				break
			}
		}

		if !isFound {
			return nil, xerrors.Errorf("invalid CommitId: %s", req.Proposal.CommitId)
		}
	}

	model := mm.loadModel(req.Proposal.Owner, meta.DataId)
	if model != nil {
		if model.CommitId == meta.CommitId && len(model.Content) > 0 {
			model.Version = req.Proposal.Version

			return model, nil
		}
	}
	if model == nil {
		model = &types.Model{
			DataId:   meta.DataId,
			Alias:    meta.Alias,
			GroupId:  meta.GroupId,
			OrderId:  meta.OrderId,
			Owner:    meta.Owner,
			Tags:     meta.Tags,
			Cid:      meta.Cid,
			Shards:   meta.Shards,
			CommitId: meta.CommitId,
			Commits:  meta.Commits,
			// Content: N/a,
			ExtendInfo: meta.ExtendInfo,
		}
	} else {
		model.OrderId = meta.OrderId
		model.Cid = meta.Cid
		model.Shards = meta.Shards
		model.CommitId = meta.CommitId
		model.Commits = meta.Commits
		model.ExtendInfo = meta.ExtendInfo
	}

	if len(meta.Shards) > 1 {
		log.Warnf("large size content should go through P2P channel")
	} else {
		result, err := mm.GatewaySvc.FetchContent(ctx, req, meta)
		if err != nil {
			return nil, xerrors.Errorf(err.Error())
		}
		model.Cid = result.Cid
		model.Content = result.Content
		model.Version = version
	}

	mm.cacheModel(req.Proposal.Owner, model)

	return model, nil
}

func (mm *ModelManager) Create(ctx context.Context, req *types.MetadataProposal, clientProposal *types.OrderStoreProposal, orderId uint64, content []byte) (*types.Model, error) {
	orderProposal := clientProposal.Proposal
	if orderProposal.Alias == "" {
		orderProposal.Alias = orderProposal.Cid
	}

	oldModel := mm.loadModel(orderProposal.Owner, orderProposal.DataId)
	if oldModel != nil {
		return nil, xerrors.Errorf("the model is exsiting already, alias: %s, dataId: %s", oldModel.Alias, oldModel.DataId)
	}

	oldModel = mm.loadModel(orderProposal.Owner, orderProposal.Alias)
	if oldModel != nil {
		return nil, xerrors.Errorf("the model is exsiting already, alias: %s, dataId: %s", oldModel.Alias, oldModel.DataId)
	}

	meta, err := mm.GatewaySvc.QueryMeta(ctx, req, 0)
	if err == nil && meta != nil {
		return nil, xerrors.Errorf("the model is exsiting already, alias: %s, dataId: %s", meta.Alias, meta.DataId)
	}

	err = mm.validateModel(ctx, orderProposal.Owner, orderProposal.Alias, content, orderProposal.Rule)
	if err != nil {
		log.Error(err.Error())
		return nil, xerrors.Errorf(err.Error())
	}

	// Commit
	result, err := mm.GatewaySvc.CommitModel(ctx, clientProposal, orderId, content)
	if err != nil {
		return nil, xerrors.Errorf(err.Error())
	}

	model := &types.Model{
		DataId:     result.DataId,
		Alias:      orderProposal.Alias,
		GroupId:    orderProposal.GroupId,
		OrderId:    result.OrderId,
		Owner:      orderProposal.Owner,
		Tags:       orderProposal.Tags,
		Cid:        result.Cid,
		Shards:     result.Shards,
		CommitId:   result.Commit,
		Commits:    result.Commits,
		Version:    "v0",
		Content:    content,
		ExtendInfo: orderProposal.ExtendInfo,
	}

	// mm.cacheModel(orderProposal.Owner, model)

	return model, nil
}

func (mm *ModelManager) Update(ctx context.Context, req *types.MetadataProposal, clientProposal *types.OrderStoreProposal, orderId uint64, patch []byte) (*types.Model, error) {
	meta, err := mm.GatewaySvc.QueryMeta(ctx, req, 0)
	if err != nil {
		return nil, xerrors.Errorf(err.Error())
	}

	var isFetch = true
	orgModel := mm.loadModel(clientProposal.Proposal.Owner, meta.DataId)
	if orgModel != nil {
		if orgModel.CommitId == meta.CommitId && len(orgModel.Content) > 0 {
			// found latest data model in local cache
			log.Debugf("load the model[%s]-%s from cache", meta.DataId, meta.Alias)
			log.Debug("model: ", string(orgModel.Content))
			isFetch = false
		}
	} else {
		orgModel = &types.Model{
			DataId:   meta.DataId,
			Alias:    meta.Alias,
			GroupId:  meta.GroupId,
			OrderId:  meta.OrderId,
			Owner:    meta.Owner,
			Tags:     meta.Tags,
			Cid:      meta.Cid,
			Shards:   meta.Shards,
			CommitId: meta.CommitId,
			Commits:  meta.Commits,
			// Content: N/a,
			ExtendInfo: meta.ExtendInfo,
		}
	}

	if isFetch {
		result, err := mm.GatewaySvc.FetchContent(ctx, req, meta)
		if err != nil {
			return nil, xerrors.Errorf(err.Error())
		}
		log.Info("result: ", result)
		log.Info("orgModel: ", orgModel)
		orgModel.Content = result.Content
	}

	log.Debug("orgModel: ", string(orgModel.Content))
	log.Debug("patch: ", string(patch))
	newContent, err := utils.ApplyPatch(orgModel.Content, []byte(patch))
	if err != nil {
		return nil, xerrors.Errorf(err.Error())
	}
	log.Debug("newContent: ", string(newContent))
	if bytes.Compare(orgModel.Content, newContent) == 0 {
		return nil, xerrors.Errorf("no content updated.")
	}

	if len(newContent) != int(clientProposal.Proposal.Size_) {
		return nil, xerrors.Errorf("given size(%d) doesn't match target content size(%d)", int(clientProposal.Proposal.Size_), len(newContent))
	}

	newContentCid, err := utils.CalculateCid(newContent)
	if err != nil {
		return nil, err
	}
	if newContentCid.String() != clientProposal.Proposal.Cid {
		return nil, xerrors.Errorf("cid mismatch, expected %s, but got %s", clientProposal.Proposal.Cid, newContentCid)
	}

	err = mm.validateModel(ctx, clientProposal.Proposal.Owner, clientProposal.Proposal.Alias, newContent, clientProposal.Proposal.Rule)
	if err != nil {
		log.Error(err.Error())
		return nil, xerrors.Errorf(err.Error())
	}

	// Commit
	result, err := mm.GatewaySvc.CommitModel(ctx, clientProposal, orderId, newContent)
	if err != nil {
		return nil, xerrors.Errorf(err.Error())
	}

	model := &types.Model{
		DataId:     meta.DataId,
		Alias:      meta.Alias,
		GroupId:    clientProposal.Proposal.GroupId,
		OrderId:    result.OrderId,
		Owner:      clientProposal.Proposal.Owner,
		Tags:       clientProposal.Proposal.Tags,
		Cid:        result.Cid,
		Shards:     result.Shards,
		CommitId:   result.Commit,
		Commits:    result.Commits,
		Version:    fmt.Sprintf("v%d", len(result.Commits)-1),
		Content:    newContent,
		ExtendInfo: clientProposal.Proposal.ExtendInfo,
	}

	// mm.cacheModel(clientProposal.Proposal.Owner, model)

	return model, nil
}

func (mm *ModelManager) Delete(ctx context.Context, req *types.OrderTerminateProposal) (*types.Model, error) {
	model, _ := mm.CacheSvc.Get(req.Proposal.Owner, req.Proposal.DataId)
	if model != nil {
		m, ok := model.(*types.Model)
		if ok {
			mm.CacheSvc.Evict(req.Proposal.Owner, m.DataId)
			mm.CacheSvc.Evict(req.Proposal.Owner, m.Alias+m.GroupId)

			return &types.Model{
				DataId: m.DataId,
				Alias:  m.Alias,
			}, nil
		}
	}

	return nil, nil
}

func (mm *ModelManager) ShowCommits(ctx context.Context, req *types.MetadataProposal) (*types.Model, error) {
	meta, err := mm.GatewaySvc.QueryMeta(ctx, req, 0)
	if err != nil {
		return nil, xerrors.Errorf(err.Error())
	}

	return &types.Model{
		DataId:  meta.DataId,
		Alias:   meta.Alias,
		Commits: meta.Commits,
	}, nil
}

func (mm *ModelManager) validateModel(ctx context.Context, account string, alias string, contentBytes []byte, rule string) error {
	schemaStr := jsoniter.Get(contentBytes, PROPERTY_CONTEXT).ToString()
	if schemaStr == "" {
		return nil
	}

	match, err := regexp.Match(`^\[.*\]$`, []byte(schemaStr))
	if err != nil {
		return xerrors.Errorf(err.Error())
	}

	if match {
		schemas := []interface{}{}
		iter := jsoniter.ParseString(jsoniter.ConfigDefault, schemaStr)
		iter.ReadArrayCB(func(iter *jsoniter.Iterator) bool {
			var elem interface{}
			iter.ReadVal(&elem)
			schemas = append(schemas, elem)
			return true
		})

		for _, schema := range schemas {
			sch, ok := schema.(string)
			if ok && sch != "" {
				if utils.IsDataId(sch) {
					model, err := mm.CacheSvc.Get(account, sch)
					if err != nil {
						return xerrors.Errorf(err.Error())
					}

					if model == nil {
						req := &types.MetadataProposal{
							Proposal: saotypes.QueryProposal{
								Owner:       "all",
								Keyword:     sch,
								KeywordType: 0,
							},
						}

						model, err = mm.Load(ctx, req)
						if err != nil {
							return xerrors.Errorf(err.Error())
						}
					}
					m, ok := model.(*types.Model)
					if ok {
						sch = string(m.Content)
					} else {
						return xerrors.Errorf("invalid schema: %v", m)
					}
				}

				validator, err := validator.NewDataModelValidator(alias, sch, rule)
				if err != nil {
					return xerrors.Errorf(err.Error())
				}
				err = validator.Validate(jsoniter.Get(contentBytes))
				if err != nil {
					return xerrors.Errorf(err.Error())
				}
			} else {
				return xerrors.Errorf("invalid schema: %v", schema)
			}
		}
	} else {
		iter := jsoniter.ParseString(jsoniter.ConfigDefault, schemaStr)
		dataId := iter.ReadString()
		var schema string
		if utils.IsDataId(dataId) {
			model, err := mm.CacheSvc.Get(account, dataId)
			if err != nil {
				return xerrors.Errorf(err.Error())
			}

			if model == nil {
				req := &types.MetadataProposal{
					Proposal: saotypes.QueryProposal{
						Owner:       "all",
						Keyword:     dataId,
						KeywordType: 0,
					},
				}

				model, err = mm.Load(ctx, req)
				if err != nil {
					return xerrors.Errorf(err.Error())
				}
			}

			m, ok := model.(*types.Model)
			if ok {
				schema = string(m.Content)
			} else {
				return xerrors.Errorf("invalid schema: %v", m)
			}
		} else {
			schema = iter.ReadObject()
		}

		validator, err := validator.NewDataModelValidator(alias, schema, rule)
		if err != nil {
			return xerrors.Errorf(err.Error())
		}
		err = validator.Validate(jsoniter.Get(contentBytes))
		if err != nil {
			return xerrors.Errorf(err.Error())
		}
	}

	return nil
}

func (mm *ModelManager) loadModel(account string, key string) *types.Model {
	if !mm.CacheCfg.EnableCache {
		return nil
	}

	value, err := mm.CacheSvc.Get(account, key)
	if err != nil {
		if strings.Contains(err.Error(), fmt.Sprintf("the cache [%s] not found", account)) {
			err = mm.CacheSvc.CreateCache(account, mm.CacheCfg.CacheCapacity)
			if err != nil {
				log.Error(err.Error())
				return nil
			}
		} else {
			log.Error(err.Error())
			return nil
		}
	}

	if value != nil {
		dataId, ok := value.(string)
		if ok {
			value, err = mm.CacheSvc.Get(account, dataId)
			if err != nil {
				log.Warn(err.Error())
			}

			if value == nil {
				return nil
			}
		}

		model, ok := value.(*types.Model)
		if ok {
			if len(model.Content) == 0 && len(model.Shards) > 0 {
				log.Warnf("large size content should go through P2P channel")
			}
			return model
		}
	}

	return nil
}

func (mm *ModelManager) cacheModel(account string, model *types.Model) {
	if !mm.CacheCfg.EnableCache {
		return
	}

	if len(model.Content) > mm.CacheCfg.ContentLimit {
		// large size content should go through P2P channel
		model.Content = make([]byte, 0)
	}
	mm.CacheSvc.Put(account, model.DataId, model)

	// mm.CacheSvc.Put(account, model.Alias+model.GroupId, model.DataId)
	// Reserved for open data model search feature...
	// for _, k := range model.Tags {
	// 	mm.CacheSvc.Put(account, k, model.DataId)
	// }
}
