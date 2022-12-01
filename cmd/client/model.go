package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	apitypes "sao-storage-node/api/types"
	"sao-storage-node/chain"
	saoclient "sao-storage-node/client"
	cliutil "sao-storage-node/cmd"
	"sao-storage-node/types"
	"sao-storage-node/utils"
	"strconv"
	"strings"
	"time"

	saotypes "github.com/SaoNetwork/sao/x/sao/types"
	"github.com/fatih/color"
	"github.com/ipfs/go-cid"
	"github.com/urfave/cli/v2"

	"golang.org/x/xerrors"
)

var modelCmd = &cli.Command{
	Name:  "model",
	Usage: "data model management",
	Subcommands: []*cli.Command{
		createCmd,
		patchGenCmd,
		updateCmd,
		loadCmd,
		deleteCmd,
		commitsCmd,
		renewCmd,
		statusCmd,
	},
}

var createCmd = &cli.Command{
	Name: "create",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "owner",
			Usage:    "data model owner",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "content",
			Required: false,
		},
		&cli.IntFlag{
			Name:     "duration",
			Usage:    "how long do you want to store the data.",
			Value:    DEFAULT_DURATION,
			Required: false,
		},
		&cli.IntFlag{
			Name:     "delay",
			Usage:    "how long to wait for the data ready",
			Value:    24 * 60 * 60,
			Required: false,
		},
		&cli.BoolFlag{
			Name:     "client-publish",
			Value:    false,
			Required: false,
		},
		&cli.StringFlag{
			Name:     "name",
			Value:    "",
			Required: false,
		},
		&cli.StringSliceFlag{
			Name:     "tags",
			Required: false,
		},
		&cli.StringFlag{
			Name:     "rule",
			Value:    "",
			Required: false,
		},
		&cli.StringFlag{
			Name:     "cid",
			Value:    "",
			Required: false,
		},
		&cli.IntFlag{
			Name:     "replica",
			Usage:    "how many copies to store.",
			Value:    DEFAULT_REPLICA,
			Required: false,
		},
		&cli.StringFlag{
			Name:     "extend-info",
			Usage:    "extend information for the model",
			Value:    "",
			Required: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		// ---- check parameters ----
		if !cctx.IsSet("content") || cctx.String("content") == "" {
			return xerrors.Errorf("must provide non-empty --content.")
		}
		content := []byte(cctx.String("content"))

		if !cctx.IsSet("owner") {
			return xerrors.Errorf("must provide --owner")
		}
		owner := cctx.String("owner")
		clientPublish := cctx.Bool("client-publish")

		// TODO: check valid range
		duration := cctx.Int("duration")
		replicas := cctx.Int("replica")
		delay := cctx.Int("delay")
		gateway := cctx.String("gateway")

		extendInfo := cctx.String("extend-info")
		if len(extendInfo) > 1024 {
			return xerrors.Errorf("extend-info should no longer than 1024 characters")
		}

		client := saoclient.NewSaoClient(ctx, gateway)
		if client == nil {
			return xerrors.Errorf("failed to create client")
		}

		chainAddress := cctx.String("chain-address")
		if chainAddress == "" {
			chainAddress = client.Cfg.ChainAddress
		}

		groupId := cctx.String("platform")
		if groupId == "" {
			groupId = client.Cfg.GroupId
		}

		contentCid, err := utils.CalculateCid(content)
		if err != nil {
			return err
		}

		didManager, err := cliutil.GetDidManager(cctx, client.Cfg.Seed, client.Cfg.Alg)
		if err != nil {
			return err
		}

		gatewayAddress, err := client.NodeAddress(ctx)
		if err != nil {
			return err
		}

		dataId := utils.GenerateDataId()
		proposal := saotypes.Proposal{
			DataId:   dataId,
			Owner:    didManager.Id,
			Provider: gatewayAddress,
			GroupId:  groupId,
			Duration: int32(duration),
			Replica:  int32(replicas),
			Timeout:  int32(delay),
			Alias:    cctx.String("name"),
			Tags:     cctx.StringSlice("tags"),
			Cid:      contentCid.String(),
			CommitId: dataId,
			Rule:     cctx.String("rule"),
			// OrderId:    0,
			Operation:  1,
			ExtendInfo: extendInfo,
		}

		proposalJsonBytesOrg, err := json.Marshal(proposal)
		if err != nil {
			return err
		}

		var obj interface{}
		err = json.Unmarshal(proposalJsonBytesOrg, &obj)
		if err != nil {
			return err
		}

		proposalJsonBytes, err := json.Marshal(obj)
		if err != nil {
			return err
		}

		jws, err := didManager.CreateJWS(proposalJsonBytes)
		if err != nil {
			return err
		}
		clientProposal := types.OrderStoreProposal{
			Proposal: proposal,
			JwsSignature: saotypes.JwsSignature{
				Protected: jws.Signatures[0].Protected,
				Signature: jws.Signatures[0].Signature,
			},
		}

		var orderId uint64 = 0
		if clientPublish {
			chain, err := chain.NewChainSvc(ctx, "cosmos", chainAddress, "/websocket")
			if err != nil {
				return xerrors.Errorf("new cosmos chain: %w", err)
			}

			orderId, _, err = chain.StoreOrder(ctx, owner, clientProposal)
			if err != nil {
				return err
			}
		}

		resp, err := client.Create(ctx, clientProposal, orderId, content)
		if err != nil {
			return err
		}
		fmt.Printf("alias: %s, data id: %s\r\n", resp.Alias, resp.DataId)
		return nil
	},
}

var loadCmd = &cli.Command{
	Name:  "load",
	Usage: "load data model",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "keyword",
			Usage:    "data model's alias, dataId or tag",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "version",
			Usage:    "data model's version",
			Required: false,
		},
		&cli.StringFlag{
			Name:     "commit-id",
			Usage:    "data model's commitId",
			Required: false,
		},
		&cli.BoolFlag{
			Name:     "dump",
			Value:    false,
			Usage:    "dump data model content to current path",
			Required: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		gateway := cctx.String("gateway")

		if !cctx.IsSet("keyword") {
			return xerrors.Errorf("must provide --keyword")
		}
		keyword := cctx.String("keyword")

		version := cctx.String("version")
		commitId := cctx.String("commit-id")
		if cctx.IsSet("version") && cctx.IsSet("commit-id") {
			fmt.Println("--version is to be ignored once --commit-id is specified")
			version = ""
		}

		client := saoclient.NewSaoClient(ctx, gateway)
		groupId := cctx.String("platform")
		if groupId == "" {
			groupId = client.Cfg.GroupId
		}

		didManager, err := cliutil.GetDidManager(cctx, client.Cfg.Seed, client.Cfg.Alg)
		if err != nil {
			return err
		}

		req := apitypes.LoadReq{
			KeyWord:   keyword,
			PublicKey: didManager.Id,
			GroupId:   groupId,
			CommitId:  commitId,
			Version:   version,
		}

		resp, err := client.Load(ctx, req)
		if err != nil {
			return err
		}

		console := color.New(color.FgMagenta, color.Bold)

		fmt.Print("  DataId    : ")
		console.Println(resp.DataId)

		fmt.Print("  Alias     : ")
		console.Println(resp.Alias)

		fmt.Print("  CommitId  : ")
		console.Println(resp.CommitId)

		fmt.Print("  Version   : ")
		console.Println(resp.Version)

		fmt.Print("  Cid       : ")
		console.Println(resp.Cid)

		match, err := regexp.Match("^"+types.Type_Prefix_File, []byte(resp.Alias))
		if err != nil {
			return err
		}

		if len(resp.Content) == 0 || match {
			fmt.Print("  SAO Link  : ")
			console.Println("sao://" + resp.DataId)

			httpUrl, err := client.GetHttpUrl(ctx, resp.DataId)
			if err != nil {
				return err
			}
			fmt.Print("  HTTP Link : ")
			console.Println(httpUrl.Url)

			ipfsUrl, err := client.GetIpfsUrl(ctx, resp.Cid)
			if err != nil {
				return err
			}
			fmt.Print("  IPFS Link : ")
			console.Println(ipfsUrl.Url)
		} else {
			fmt.Print("  Content   : ")
			console.Println(resp.Content)
		}

		dumpFlag := cctx.Bool("dump")
		if dumpFlag {
			path := filepath.Join("./", resp.DataId+".json")
			file, err := os.Create(path)
			if err != nil {
				return err
			}

			_, err = file.Write([]byte(resp.Content))
			if err != nil {
				return err
			}
			fmt.Printf("data model dumped to %s.\r\n", path)
		}

		return nil
	},
}

var renewCmd = &cli.Command{
	Name:  "renew",
	Usage: "renew data",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "owner",
			Usage:    "file owner",
			Required: true,
		},
		&cli.StringSliceFlag{
			Name:     "data-ids",
			Usage:    "data model's dataId list",
			Required: true,
		},
		&cli.IntFlag{
			Name:     "duration",
			Usage:    "how long do you want to store the data.",
			Value:    DEFAULT_DURATION,
			Required: false,
		},
		&cli.IntFlag{
			Name:     "delay",
			Usage:    "how long to wait for the file ready",
			Value:    24 * 60 * 60,
			Required: false,
		},
		&cli.BoolFlag{
			Name:     "client-publish",
			Value:    false,
			Required: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		gateway := cctx.String("gateway")

		if !cctx.IsSet("data-ids") {
			return xerrors.Errorf("must provide --data-ids")
		}
		dataIds := cctx.StringSlice("data-ids")
		duration := cctx.Int("duration")
		delay := cctx.Int("delay")
		owner := cctx.String("owner")

		client := saoclient.NewSaoClient(ctx, gateway)

		chainAddress := cctx.String("chain-address")
		if chainAddress == "" {
			chainAddress = client.Cfg.ChainAddress
		}

		chain, err := chain.NewChainSvc(ctx, "cosmos", chainAddress, "/websocket")
		if err != nil {
			return xerrors.Errorf("new cosmos chain: %w", err)
		}

		didManager, err := cliutil.GetDidManager(cctx, client.Cfg.Seed, client.Cfg.Alg)
		if err != nil {
			return err
		}

		proposal := saotypes.RenewProposal{
			Owner:    didManager.Id,
			Duration: int32(duration),
			Timeout:  int32(delay),
			Data:     dataIds,
		}

		proposalJsonBytes, err := proposal.Marshal()
		if err != nil {
			return err
		}
		jws, err := didManager.CreateJWS(proposalJsonBytes)
		if err != nil {
			return err
		}
		clientProposal := types.OrderRenewProposal{
			Proposal:     proposal,
			JwsSignature: saotypes.JwsSignature(jws.Signatures[0]),
		}

		_, results, err := chain.RenewOrder(ctx, owner, clientProposal)
		if err != nil {
			return err
		}

		var renewModels = make(map[string]uint64, len(results))
		var renewedOrders = make(map[string]string, 0)
		var failedOrders = make(map[string]string, 0)
		for dataId, result := range results {
			if strings.Contains(result, "New order=") {
				orderId, err := strconv.ParseUint(strings.Split(result, "=")[1], 10, 64)
				if err != nil {
					failedOrders[dataId] = result + ", " + err.Error()
				} else {
					renewModels[dataId] = orderId
				}
			} else {
				renewedOrders[dataId] = result
			}
		}

		for dataId, info := range renewedOrders {
			fmt.Printf("successfully renewed model[%s]: %s.\n", dataId, info)
		}

		for dataId, err := range failedOrders {
			fmt.Printf("failed to renew model[%s]: %s.\n", dataId, err)
		}

		return nil
	},
}

var statusCmd = &cli.Command{
	Name:  "status",
	Usage: "check models' status",
	Flags: []cli.Flag{
		&cli.StringSliceFlag{
			Name:     "data-ids",
			Usage:    "data model's dataId list",
			Required: true,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		gateway := cctx.String("gateway")

		if !cctx.IsSet("data-ids") {
			return xerrors.Errorf("must provide --data-ids")
		}
		dataIds := cctx.StringSlice("data-ids")

		client := saoclient.NewSaoClient(ctx, gateway)

		chainAddress := cctx.String("chain-address")
		if chainAddress == "" {
			chainAddress = client.Cfg.ChainAddress
		}

		chain, err := chain.NewChainSvc(ctx, "cosmos", chainAddress, "/websocket")
		if err != nil {
			return xerrors.Errorf("new cosmos chain: %w", err)
		}

		states := ""
		for _, dataId := range dataIds {
			res, err := chain.QueryMeta(ctx, dataId, 0)
			if err != nil {
				if len(states) > 0 {
					states = fmt.Sprintf("%s\n[%s]: %s", states, dataId, err.Error())
				} else {
					states = fmt.Sprintf("[%s]: %s", dataId, err.Error())
				}
			} else {
				duration := res.Metadata.Duration
				used := uint64(time.Now().Second()) - res.Metadata.CreatedAt
				if len(states) > 0 {
					states = states + "\n"
				}
				consoleOK := color.New(color.FgGreen, color.Bold)
				consoleWarn := color.New(color.FgHiRed, color.Bold)

				var leftDays uint64
				if duration >= used {
					leftDays = duration - used

				} else {
					leftDays = used - duration
				}
				if leftDays > 5 {
					states = fmt.Sprintf("%s[%s]: expired in %s days", states, dataId, consoleOK.Sprintf("%d", leftDays/(60*60*24*365)))
				} else {
					states = fmt.Sprintf("%s[%s]: expired %s days ago", states, dataId, consoleWarn.Sprintf("%d", leftDays/(60*60*24*365)))
				}
			}
		}

		fmt.Println(states)

		return nil
	},
}

var deleteCmd = &cli.Command{
	Name:  "delete",
	Usage: "delete data model",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "keyword",
			Usage:    "data model's alias, dataId or tag",
			Required: true,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		gateway := cctx.String("gateway")

		if !cctx.IsSet("keyword") {
			return xerrors.Errorf("must provide --keyword")
		}
		keyword := cctx.String("keyword")

		client := saoclient.NewSaoClient(ctx, gateway)
		didManager, err := cliutil.GetDidManager(cctx, client.Cfg.Seed, client.Cfg.Alg)
		if err != nil {
			return err
		}

		groupId := cctx.String("platform")
		if groupId == "" {
			groupId = client.Cfg.GroupId
		}

		resp, err := client.Delete(ctx, didManager.Id, keyword, groupId)
		if err != nil {
			return err
		}
		fmt.Printf("data model %s deleted.\r\n", resp.Alias)

		return nil
	},
}

var commitsCmd = &cli.Command{
	Name:  "commits",
	Usage: "list data model historical commits",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "keyword",
			Usage:    "data model's alias, dataId or tag",
			Required: true,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		gateway := cctx.String("gateway")

		if !cctx.IsSet("keyword") {
			return xerrors.Errorf("must provide --keyword")
		}
		keyword := cctx.String("keyword")

		client := saoclient.NewSaoClient(ctx, gateway)
		didManager, err := cliutil.GetDidManager(cctx, client.Cfg.Seed, client.Cfg.Alg)
		if err != nil {
			return err
		}

		groupId := cctx.String("platform")
		if groupId == "" {
			groupId = client.Cfg.GroupId
		}

		resp, err := client.ShowCommits(ctx, didManager.Id, keyword, groupId)
		if err != nil {
			return err
		}

		console := color.New(color.FgMagenta, color.Bold)

		fmt.Print("  Model DataId : ")
		console.Println(resp.DataId)

		fmt.Print("  Model Alias  : ")
		console.Println(resp.Alias)

		fmt.Println("  -----------------------------------------------------------")
		fmt.Println("  Version |Commit                              |Height")
		fmt.Println("  -----------------------------------------------------------")
		for i, commit := range resp.Commits {
			commitInfo := strings.Split(commit, "\032")
			if len(commitInfo) != 2 || len(commitInfo[1]) == 0 {
				return xerrors.Errorf("invalid commit information: %s", commit)
			}

			console.Printf("  v%d\t  |%s|%s\r\n", i, commitInfo[0], commitInfo[1])
		}
		fmt.Println("  -----------------------------------------------------------")

		return nil
	},
}

var updateCmd = &cli.Command{
	Name: "update",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "owner",
			Usage:    "file owner",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "patch",
			Usage:    "patch to apply for the data model",
			Required: true,
		},
		&cli.IntFlag{
			Name:     "duration",
			Usage:    "how long do you want to store the data.",
			Value:    DEFAULT_DURATION,
			Required: false,
		},
		&cli.IntFlag{
			Name:     "delay",
			Usage:    "how long to wait for the file ready",
			Value:    24 * 60 * 60,
			Required: false,
		},
		&cli.BoolFlag{
			Name:     "client-publish",
			Value:    false,
			Required: false,
		},
		&cli.BoolFlag{
			Name:     "force",
			Usage:    "overwrite the latest commit",
			Value:    false,
			Required: false,
		},
		&cli.StringSliceFlag{
			Name:     "tags",
			Required: false,
		},
		&cli.StringFlag{
			Name:     "rule",
			Value:    "",
			Required: false,
		},
		&cli.StringFlag{
			Name:     "keyword",
			Usage:    "data model's alias, dataId or tag",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "cid",
			Required: true,
		},
		&cli.IntFlag{
			Name:     "replica",
			Usage:    "how many copies to store.",
			Value:    DEFAULT_REPLICA,
			Required: false,
		},
		&cli.StringFlag{
			Name:     "extend-info",
			Usage:    "extend information for the model",
			Required: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		// ---- check parameters ----
		if !cctx.IsSet("owner") {
			return xerrors.Errorf("must provide --owner")
		}
		owner := cctx.String("owner")

		if !cctx.IsSet("keyword") {
			return xerrors.Errorf("must provide --keyword")
		}
		keyword := cctx.String("keyword")

		patch := []byte(cctx.String("patch"))
		contentCid := cctx.String("cid")
		newCid, err := cid.Decode(contentCid)
		if err != nil {
			return err
		}

		extendInfo := cctx.String("extend-info")
		if len(extendInfo) > 1024 {
			return xerrors.Errorf("extend-info should no longer than 1024 characters")
		}

		clientPublish := cctx.Bool("client-publish")

		// TODO: check valid range
		duration := cctx.Int("duration")
		replicas := cctx.Int("replica")
		delay := cctx.Int("delay")
		gateway := cctx.String("gateway")
		client := saoclient.NewSaoClient(ctx, gateway)

		chainAddress := cctx.String("chain-address")
		if chainAddress == "" {
			chainAddress = client.Cfg.ChainAddress
		}

		groupId := cctx.String("platform")
		if groupId == "" {
			groupId = client.Cfg.GroupId
		}

		didManager, err := cliutil.GetDidManager(cctx, client.Cfg.Seed, client.Cfg.Alg)
		if err != nil {
			return err
		}

		gatewayAddress, err := client.NodeAddress(ctx)
		if err != nil {
			return err
		}

		chain, err := chain.NewChainSvc(ctx, "cosmos", chainAddress, "/websocket")
		if err != nil {
			return xerrors.Errorf("new cosmos chain: %w", err)
		}

		var dataId string
		if !utils.IsDataId(keyword) {
			dataId, err = chain.QueryDataId(ctx, fmt.Sprintf("%s-%s-%s", didManager.Id, keyword, groupId))
			if err != nil {
				return err
			}
		} else {
			dataId = keyword
		}
		res, err := chain.QueryMeta(ctx, dataId, 0)
		if err != nil {
			return err
		}

		force := cctx.Bool("force")

		operation := uint32(0)

		if force {
			operation = 1
		}

		proposal := saotypes.Proposal{
			Owner:      didManager.Id,
			Provider:   gatewayAddress,
			GroupId:    groupId,
			Duration:   int32(duration),
			Replica:    int32(replicas),
			Timeout:    int32(delay),
			DataId:     res.Metadata.DataId,
			Alias:      res.Metadata.Alias,
			Tags:       cctx.StringSlice("tags"),
			Cid:        newCid.String(),
			CommitId:   utils.GenerateCommitId(),
			Rule:       cctx.String("rule"),
			Operation:  operation,
			ExtendInfo: extendInfo,
		}

		proposalJsonBytes, err := proposal.Marshal()
		if err != nil {
			return err
		}
		jws, err := didManager.CreateJWS(proposalJsonBytes)
		if err != nil {
			return err
		}
		clientProposal := types.OrderStoreProposal{
			Proposal:     proposal,
			JwsSignature: saotypes.JwsSignature(jws.Signatures[0]),
		}

		var orderId uint64 = 0
		if clientPublish {

			orderId, _, err = chain.StoreOrder(ctx, owner, clientProposal)
			if err != nil {
				return err
			}
		}

		resp, err := client.Update(ctx, clientProposal, orderId, patch)
		if err != nil {
			return err
		}
		fmt.Printf("alias: %s, data id: %s.\r\n", resp.Alias, resp.DataId)
		return nil
	},
}

var patchGenCmd = &cli.Command{
	Name:  "patch-gen",
	Usage: "generate data model patch",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "origin",
			Usage:    "the original data model\r\n",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "target",
			Usage:    "the target data model\r\n",
			Required: true,
		},
	},
	Action: func(cctx *cli.Context) error {
		if !cctx.IsSet("origin") || !cctx.IsSet("target") {
			return xerrors.Errorf("please provide both --origin and --target")
		}

		origin := cctx.String("origin")
		target := cctx.String("target")
		patch, err := utils.GeneratePatch(origin, target)
		if err != nil {
			return err
		}

		content, err := utils.ApplyPatch([]byte(origin), []byte(patch))
		if err != nil {
			return err
		}

		var newModel interface{}
		err = json.Unmarshal(content, &newModel)
		if err != nil {
			return err
		}

		var targetModel interface{}
		err = json.Unmarshal([]byte(target), &targetModel)
		if err != nil {
			return err
		}

		valueStrNew, err := json.Marshal(newModel)
		if err != nil {
			return err
		}

		valueStrTarget, err := json.Marshal(targetModel)
		if err != nil {
			return err
		}

		if string(valueStrNew) != string(valueStrTarget) {
			return xerrors.Errorf("failed to generate the patch")
		}

		targetCid, err := utils.CalculateCid(content)
		if err != nil {
			return err
		}

		console := color.New(color.FgMagenta, color.Bold)

		fmt.Print("  Patch      : ")
		console.Println(patch)

		fmt.Print("  Target Cid : ")
		console.Println(targetCid)

		return nil
	},
}
