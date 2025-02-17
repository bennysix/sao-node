package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sao-node/chain"
	cliutil "sao-node/cmd"
	"sao-node/types"
	"sao-node/utils"
	"strconv"
	"strings"
	"time"

	did "github.com/SaoNetwork/sao-did"
	saotypes "github.com/SaoNetwork/sao/x/sao/types"
	"github.com/fatih/color"
	"github.com/ipfs/go-cid"
	"github.com/urfave/cli/v2"
)

var modelCmd = &cli.Command{
	Name:      "model",
	Usage:     "data model management",
	UsageText: "model related commands including create, update, update permission, etc.",
	Subcommands: []*cli.Command{
		createCmd,
		patchGenCmd,
		updateCmd,
		updatePermissionCmd,
		loadCmd,
		deleteCmd,
		commitsCmd,
		listCmd,
		renewCmd,
		statusCmd,
		metaCmd,
		orderCmd,
	},
}

var createCmd = &cli.Command{
	Name:  "create",
	Usage: "create a new data model",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "content",
			Required: false,
			Usage:    "data model content to create. you must either specify --content or --cid",
		},
		&cli.StringFlag{
			Name:     "cid",
			Usage:    "data content cid, make sure gateway has this cid file before using this flag. you must either specify --content or --cid. ",
			Value:    "",
			Required: false,
		},
		&cli.IntFlag{
			Name:     "duration",
			Usage:    "how many days do you want to store the data",
			Value:    DEFAULT_DURATION,
			Required: false,
		},
		&cli.IntFlag{
			Name:     "delay",
			Usage:    "how many epochs to wait for the content to be completed storing",
			Value:    1 * 60,
			Required: false,
		},
		&cli.BoolFlag{
			Name:     "client-publish",
			Usage:    "true if client sends MsgStore message on chain, or leave it to gateway to send",
			Value:    false,
			Required: false,
		},
		&cli.StringFlag{
			Name:     "name",
			Usage:    "alias name for this data model, this alias name can be used to update, load, etc.",
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
		&cli.IntFlag{
			Name:     "replica",
			Usage:    "how many copies to store",
			Value:    DEFAULT_REPLICA,
			Required: false,
		},
		&cli.StringFlag{
			Name:     "extend-info",
			Usage:    "extend information for the model",
			Value:    "",
			Required: false,
		},
		&cli.BoolFlag{
			Name:     "public",
			Value:    false,
			Required: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		// ---- check parameters ----
		if !cctx.IsSet("content") || cctx.String("content") == "" {
			return types.Wrapf(types.ErrInvalidParameters, "must provide non-empty --content.")
		}
		content := []byte(cctx.String("content"))

		clientPublish := cctx.Bool("client-publish")

		// TODO: check valid range
		duration := cctx.Int("duration")
		replicas := cctx.Int("replica")
		delay := cctx.Int("delay")
		isPublic := cctx.Bool("public")

		extendInfo := cctx.String("extend-info")
		if len(extendInfo) > 1024 {
			return types.Wrapf(types.ErrInvalidParameters, "extend-info should no longer than 1024 characters")
		}

		client, closer, err := getSaoClient(cctx)
		if err != nil {
			return err
		}
		defer closer()

		if client == nil {
			return types.Wrap(types.ErrCreateClientFailed, nil)
		}

		groupId := cctx.String("platform")
		if groupId == "" {
			groupId = client.Cfg.GroupId
		}

		contentCid, err := utils.CalculateCid(content)
		if err != nil {
			return err
		}

		didManager, signer, err := cliutil.GetDidManager(cctx, client.Cfg.KeyName)
		if err != nil {
			return err
		}

		gatewayAddress, err := client.GetNodeAddress(ctx)
		if err != nil {
			return err
		}

		dataId := utils.GenerateDataId(didManager.Id + groupId)
		proposal := saotypes.Proposal{
			DataId:   dataId,
			Owner:    didManager.Id,
			Provider: gatewayAddress,
			GroupId:  groupId,
			Duration: uint64(time.Duration(60*60*24*duration) * time.Second / chain.Blocktime),
			Replica:  int32(replicas),
			Timeout:  int32(delay),
			Alias:    cctx.String("name"),
			Tags:     cctx.StringSlice("tags"),
			Cid:      contentCid.String(),
			CommitId: dataId,
			Rule:     cctx.String("rule"),
			// OrderId:    0,
			Size_:      uint64(len(content)),
			Operation:  1,
			ExtendInfo: extendInfo,
		}
		if proposal.Alias == "" {
			proposal.Alias = proposal.Cid
		}

		queryProposal := saotypes.QueryProposal{
			Owner:   didManager.Id,
			Keyword: dataId,
		}

		if isPublic {
			queryProposal.Owner = "all"
			proposal.Owner = "all"
		}

		clientProposal, err := buildClientProposal(ctx, didManager, proposal, client)
		if err != nil {
			return err
		}

		var orderId uint64 = 0
		if clientPublish {
			resp, _, _, err := client.StoreOrder(ctx, signer, clientProposal)
			if err != nil {
				return err
			}
			orderId = resp.OrderId
		}

		request, err := buildQueryRequest(ctx, didManager, queryProposal, client, gatewayAddress)
		if err != nil {
			return err
		}

		resp, err := client.ModelCreate(ctx, request, clientProposal, orderId, content)
		if err != nil {
			return err
		}
		fmt.Printf("alias: %s, data id: %s\r\n", resp.Alias, resp.DataId)
		return nil
	},
}

var loadCmd = &cli.Command{
	Name:      "load",
	Usage:     "load data model",
	UsageText: "only owner and dids with r/rw permission can load data model.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "keyword",
			Usage:    "data model's alias, dataId or tag",
			Required: false,
		},
		&cli.StringFlag{
			Name:     "version",
			Usage:    "data model's version. you can find out version in commits cmd",
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
			Usage:    "dump data model content to ./<dataid>.json",
			Required: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		if !cctx.IsSet("keyword") {
			return types.Wrapf(types.ErrInvalidParameters, "must provide --keyword")
		}
		keyword := cctx.String("keyword")

		version := cctx.String("version")
		commitId := cctx.String("commit-id")
		if cctx.IsSet("version") && cctx.IsSet("commit-id") {
			fmt.Println("--version is to be ignored once --commit-id is specified")
			version = ""
		}

		client, closer, err := getSaoClient(cctx)
		if err != nil {
			return err
		}
		defer closer()

		groupId := cctx.String("platform")
		if groupId == "" {
			groupId = client.Cfg.GroupId
		}

		didManager, _, err := cliutil.GetDidManager(cctx, client.Cfg.KeyName)
		if err != nil {
			return err
		}

		proposal := saotypes.QueryProposal{
			Owner:    didManager.Id,
			Keyword:  keyword,
			GroupId:  groupId,
			CommitId: commitId,
			Version:  version,
		}

		if !utils.IsDataId(keyword) {
			proposal.KeywordType = 2
		}

		gatewayAddress, err := client.GetNodeAddress(ctx)
		if err != nil {
			return err
		}

		request, err := buildQueryRequest(ctx, didManager, proposal, client, gatewayAddress)
		if err != nil {
			return err
		}

		resp, err := client.ModelLoad(ctx, request)
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
			return types.Wrap(types.ErrInvalidAlias, err)
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
				return types.Wrap(types.ErrCreateDirFailed, err)
			}

			_, err = file.Write([]byte(resp.Content))
			if err != nil {
				return types.Wrap(types.ErrWriteFileFailed, err)
			}
			fmt.Printf("data model dumped to %s.\r\n", path)
		}

		return nil
	},
}

var listCmd = &cli.Command{
	Name:  "list",
	Usage: "check models' status",
	Flags: []cli.Flag{
		&cli.StringSliceFlag{
			Name:     "date",
			Usage:    "updated date of data model's to be list",
			Required: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		fmt.Printf("TODO...")
		return nil
	},
}

var renewCmd = &cli.Command{
	Name:  "renew",
	Usage: "renew data model",
	Flags: []cli.Flag{
		&cli.StringSliceFlag{
			Name:     "data-ids",
			Usage:    "data model's dataId list",
			Required: true,
		},
		&cli.IntFlag{
			Name:     "duration",
			Usage:    "how many days do you want to renew the data.",
			Value:    DEFAULT_DURATION,
			Required: false,
		},
		&cli.IntFlag{
			Name:     "delay",
			Usage:    "how long to wait for the file ready",
			Value:    1 * 60,
			Required: false,
		},
		&cli.BoolFlag{
			Name:     "client-publish",
			Usage:    "true if client sends MsgStore message on chain, or leave it to gateway to send",
			Value:    false,
			Required: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		if !cctx.IsSet("data-ids") {
			return types.Wrapf(types.ErrInvalidParameters, "must provide --data-ids")
		}
		dataIds := cctx.StringSlice("data-ids")
		duration := cctx.Int("duration")
		delay := cctx.Int("delay")
		clientPublish := cctx.Bool("client-publish")

		client, closer, err := getSaoClient(cctx)
		if err != nil {
			return err
		}
		defer closer()

		didManager, signer, err := cliutil.GetDidManager(cctx, client.Cfg.KeyName)
		if err != nil {
			return err
		}

		proposal := saotypes.RenewProposal{
			Owner:    didManager.Id,
			Duration: uint64(time.Duration(60*60*24*duration) * time.Second / chain.Blocktime),
			Timeout:  int32(delay),
			Data:     dataIds,
		}

		proposalBytes, err := proposal.Marshal()
		if err != nil {
			return types.Wrap(types.ErrMarshalFailed, err)
		}

		jws, err := didManager.CreateJWS(proposalBytes)
		if err != nil {
			return types.Wrap(types.ErrCreateJwsFailed, err)
		}
		clientProposal := types.OrderRenewProposal{
			Proposal:     proposal,
			JwsSignature: saotypes.JwsSignature(jws.Signatures[0]),
		}

		var results map[string]string
		if clientPublish {
			_, results, err = client.RenewOrder(ctx, signer, clientProposal)
			if err != nil {
				return err
			}
		} else {
			res, err := client.ModelRenewOrder(ctx, &clientProposal, !clientPublish)
			if err != nil {
				return err
			}
			results = res.Results
		}

		var renewModels = make(map[string]uint64, len(results))
		var renewedOrders = make(map[string]string, 0)
		var failedOrders = make(map[string]string, 0)
		for dataId, result := range results {
			if strings.Contains(result, "SUCCESS") {
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

		for dataId, orderId := range renewModels {
			fmt.Printf("successfully renewed model[%s] with orderId[%d].\n", dataId, orderId)
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

		if !cctx.IsSet("data-ids") {
			return types.Wrapf(types.ErrInvalidParameters, "must provide --data-ids")
		}
		dataIds := cctx.StringSlice("data-ids")

		client, closer, err := getSaoClient(cctx)
		if err != nil {
			return err
		}
		defer closer()

		didManager, _, err := cliutil.GetDidManager(cctx, client.Cfg.KeyName)
		if err != nil {
			return err
		}

		gatewayAddress, err := client.GetNodeAddress(ctx)
		if err != nil {
			return err
		}

		states := ""
		for _, dataId := range dataIds {
			proposal := saotypes.QueryProposal{
				Owner:   didManager.Id,
				Keyword: dataId,
			}

			request, err := buildQueryRequest(ctx, didManager, proposal, client, gatewayAddress)
			if err != nil {
				return err
			}

			res, err := client.QueryMetadata(ctx, request, 0)
			if err != nil {
				if len(states) > 0 {
					states = fmt.Sprintf("%s\n[%s]: %s", states, dataId, err.Error())
				} else {
					states = fmt.Sprintf("[%s]: %s", dataId, err.Error())
				}
			} else {
				duration := res.Metadata.Duration
				currentHeight, err := client.GetLastHeight(ctx)
				if err != nil {
					return err
				}
				stored := uint64(currentHeight) - res.Metadata.CreatedAt
				if len(states) > 0 {
					states = states + "\n"
				}
				consoleOK := color.New(color.FgGreen, color.Bold)
				consoleWarn := color.New(color.FgHiRed, color.Bold)

				var leftHeight uint64
				if duration >= stored {
					leftHeight = duration - stored
					states = fmt.Sprintf("%s[%s]: expired in %s heights", states, dataId, consoleOK.Sprintf("%d", leftHeight))
				} else {
					leftHeight = stored - duration
					states = fmt.Sprintf("%s[%s]: expired %s heights ago", states, dataId, consoleWarn.Sprintf("%d", leftHeight))
				}
			}
		}

		fmt.Println(states)

		return nil
	},
}

var metaCmd = &cli.Command{
	Name:  "meta",
	Usage: "check models' meta information",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:  "data-id",
			Usage: "data model's dataId",
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		if !cctx.IsSet("data-id") {
			return types.Wrapf(types.ErrInvalidParameters, "must provide --data-id")
		}
		dataId := cctx.String("data-id")

		client, closer, err := getSaoClient(cctx)
		if err != nil {
			return err
		}
		defer closer()

		didManager, _, err := cliutil.GetDidManager(cctx, client.Cfg.KeyName)
		if err != nil {
			return err
		}

		gatewayAddress, err := client.GetNodeAddress(ctx)
		if err != nil {
			return err
		}

		proposal := saotypes.QueryProposal{
			Owner:   didManager.Id,
			Keyword: dataId,
		}

		request, err := buildQueryRequest(ctx, didManager, proposal, client, gatewayAddress)
		if err != nil {
			return err
		}

		res, err := client.QueryMetadata(ctx, request, 0)
		if err != nil {
			return types.Wrap(types.ErrQueryMetadataFailed, err)
		} else {
			fmt.Printf("DataId: %s\n", res.Metadata.DataId)
			fmt.Printf("Owner: %s\n", res.Metadata.Owner)
			fmt.Printf("Alias: %s\n", res.Metadata.Alias)
			fmt.Printf("GroupId: %s\n", res.Metadata.GroupId)
			fmt.Printf("OrderId: %d\n", res.Metadata.OrderId)
			fmt.Println("Tags: ")
			for index, tag := range res.Metadata.Tags {
				fmt.Printf("%s", tag)
				if index < len(res.Metadata.Tags)-1 {
					fmt.Print(", ")
				} else {
					fmt.Println()
				}
			}
			fmt.Printf("Cid: %s\n", res.Metadata.Cid)
			fmt.Println("Commits: ")
			for index, commit := range res.Metadata.Commits {
				fmt.Printf("%s", commit)
				if index < len(res.Metadata.Commits)-1 {
					fmt.Print(", ")
				} else {
					fmt.Println()
				}
			}
			fmt.Printf("ExtendInfo: %s\n", res.Metadata.ExtendInfo)
			fmt.Printf("IsUpdate: %v\n", res.Metadata.Update)
			fmt.Printf("Commit: %s\n", res.Metadata.Commit)
			fmt.Printf("Rule: %s\n", res.Metadata.Rule)
			fmt.Printf("Duration: %d\n", res.Metadata.Duration)
			fmt.Printf("CreatedAt: %d\n", res.Metadata.CreatedAt)
			fmt.Printf("Provider: %s\n", res.Metadata.Provider)
			fmt.Printf("Expire: %d\n", res.Metadata.Expire)
			fmt.Printf("Status: %d\n", res.Metadata.Status)
			fmt.Printf("Replica: %d\n", res.Metadata.Replica)
			fmt.Printf("Amount: %v\n", res.Metadata.Amount)
			fmt.Printf("Size: %d\n", res.Metadata.Size_)
			fmt.Printf("Operation: %d\n", res.Metadata.Operation)

			fmt.Println("Shards: ")
			for _, shard := range res.Shards {
				fmt.Printf("ShardId: %d\n", shard.ShardId)
				fmt.Printf("Cid: %s\n", shard.Cid)
				fmt.Printf("Peer: %s\n", shard.Peer)
				fmt.Printf("Provider: %s\n", shard.Provider)
			}

		}

		return nil
	},
}

var orderCmd = &cli.Command{
	Name:  "order",
	Usage: "check models' order information",
	Flags: []cli.Flag{
		&cli.UintFlag{
			Name:  "order-id",
			Usage: "data model's orderId",
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		if !cctx.IsSet("order-id") {
			return types.Wrapf(types.ErrInvalidParameters, "must provide --order-id")
		}
		orderId := cctx.Uint("order-id")

		client, closer, err := getSaoClient(cctx)
		if err != nil {
			return err
		}
		defer closer()

		res, err := client.GetOrder(ctx, uint64(orderId))
		if err != nil {
			return types.Wrap(types.ErrQueryMetadataFailed, err)
		} else {
			fmt.Printf("Id: %d\n", res.Id)
			fmt.Printf("Owner: %s\n", res.Owner)
			fmt.Printf("Creator: %s\n", res.Creator)
			fmt.Printf("Gateway: %s\n", res.Provider)
			fmt.Printf("Cid: %s\n", res.Cid)
			fmt.Printf("Duration: %d\n", res.Duration)
			fmt.Printf("CreatedAt: %d\n", res.CreatedAt)
			fmt.Printf("Expire: %d\n", res.Expire)
			fmt.Printf("Status: %d\n", res.Status)
			fmt.Printf("Replica: %d\n", res.Replica)
			fmt.Printf("Amount: %v\n", res.Amount)
			fmt.Printf("Size: %d\n", res.Size_)
			fmt.Printf("Operation: %d\n", res.Operation)

			fmt.Println("Shards: ")
			for key, shard := range res.Shards {
				fmt.Printf("Id: %d\n", shard.Id)
				fmt.Printf("Provider: %s\n", key)
				fmt.Printf("OrderId: %d\n", shard.OrderId)
				fmt.Printf("Status: %d\n", shard.Status)
				fmt.Printf("Size: %d\n", shard.Size_)
				fmt.Printf("Cid: %s\n", shard.Cid)
				fmt.Printf("Pledge: %v\n", shard.Pledge)
				if shard.From != "" {
					fmt.Printf("Previous Provider: %s\n", shard.From)
				}
			}

		}

		return nil
	},
}

var deleteCmd = &cli.Command{
	Name:  "delete",
	Usage: "delete data model",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "data-id",
			Usage:    "data model's dataId",
			Required: true,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		if !cctx.IsSet("data-id") {
			return types.Wrapf(types.ErrInvalidParameters, "must provide --data-id")
		}
		dataId := cctx.String("data-id")
		clientPublish := cctx.Bool("client-publish")

		client, closer, err := getSaoClient(cctx)
		if err != nil {
			return err
		}
		defer closer()

		didManager, signer, err := cliutil.GetDidManager(cctx, client.Cfg.KeyName)
		if err != nil {
			return err
		}

		proposal := saotypes.TerminateProposal{
			Owner:  didManager.Id,
			DataId: dataId,
		}

		proposalBytes, err := proposal.Marshal()
		if err != nil {
			return types.Wrap(types.ErrMarshalFailed, err)
		}

		jws, err := didManager.CreateJWS(proposalBytes)
		if err != nil {
			return types.Wrap(types.ErrCreateJwsFailed, err)
		}
		request := types.OrderTerminateProposal{
			Proposal:     proposal,
			JwsSignature: saotypes.JwsSignature(jws.Signatures[0]),
		}

		if clientPublish {
			_, err = client.TerminateOrder(ctx, signer, request)
			if err != nil {
				return err
			}
		}

		result, err := client.ModelDelete(ctx, &request, !clientPublish)
		if err != nil {
			return err
		}

		fmt.Printf("data model %s deleted.\r\n", result.DataId)

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

		if !cctx.IsSet("keyword") {
			return types.Wrapf(types.ErrInvalidParameters, "must provide --keyword")
		}
		keyword := cctx.String("keyword")

		client, closer, err := getSaoClient(cctx)
		if err != nil {
			return err
		}
		defer closer()

		didManager, _, err := cliutil.GetDidManager(cctx, client.Cfg.KeyName)
		if err != nil {
			return err
		}

		groupId := cctx.String("platform")
		if groupId == "" {
			groupId = client.Cfg.GroupId
		}

		proposal := saotypes.QueryProposal{
			Owner:   didManager.Id,
			Keyword: keyword,
			GroupId: groupId,
		}

		if !utils.IsDataId(keyword) {
			proposal.KeywordType = 2
		}

		gatewayAddress, err := client.GetNodeAddress(ctx)
		if err != nil {
			return err
		}

		request, err := buildQueryRequest(ctx, didManager, proposal, client, gatewayAddress)
		if err != nil {
			return err
		}

		resp, err := client.ModelShowCommits(ctx, request)
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
			commitInfo, err := types.ParseMetaCommit(commit)
			if err != nil {
				return types.Wrapf(types.ErrInvalidCommitInfo, "invalid commit information: %s", commit)
			}

			console.Printf("  v%d\t  |%s|%d\r\n", i, commitInfo.CommitId, commitInfo.Height)
		}
		fmt.Println("  -----------------------------------------------------------")

		return nil
	},
}

var updateCmd = &cli.Command{
	Name:      "update",
	Usage:     "update an existing data model",
	UsageText: "use patch cmd to generate --patch flag and --cid first. permission error will be reported if you don't have model write perm",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "patch",
			Usage:    "patch to apply for the data model",
			Required: true,
		},
		&cli.IntFlag{
			Name:     "duration",
			Usage:    "how many days do you want to store the data.",
			Value:    DEFAULT_DURATION,
			Required: false,
		},
		&cli.IntFlag{
			Name:     "delay",
			Usage:    "how many epochs to wait for data update complete",
			Value:    1 * 60,
			Required: false,
		},
		&cli.BoolFlag{
			Name:     "client-publish",
			Usage:    "true if client sends MsgStore message on chain, or leave it to gateway to send",
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
			Usage:    "data model's alias name, dataId or tag",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "commit-id",
			Usage:    "data model's last commit id",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "cid",
			Usage:    "target content cid",
			Required: true,
		},
		&cli.IntFlag{
			Name:     "size",
			Usage:    "target content size",
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
		if !cctx.IsSet("keyword") {
			return types.Wrapf(types.ErrInvalidParameters, "must provide --keyword")
		}
		keyword := cctx.String("keyword")

		size := cctx.Int("size")
		if size <= 0 {
			return types.Wrapf(types.ErrInvalidParameters, "invalid size")
		}

		patch := []byte(cctx.String("patch"))
		contentCid := cctx.String("cid")
		newCid, err := cid.Decode(contentCid)
		if err != nil {
			return types.Wrapf(types.ErrInvalidCid, "cid=%s", contentCid)
		}

		extendInfo := cctx.String("extend-info")
		if len(extendInfo) > 1024 {
			return types.Wrapf(types.ErrInvalidParameters, "extend-info should no longer than 1024 characters")
		}

		clientPublish := cctx.Bool("client-publish")

		// TODO: check valid range
		duration := cctx.Int("duration")
		replicas := cctx.Int("replica")
		delay := cctx.Int("delay")
		client, closer, err := getSaoClient(cctx)
		if err != nil {
			return err
		}
		defer closer()

		groupId := cctx.String("platform")
		if groupId == "" {
			groupId = client.Cfg.GroupId
		}
		commitId := cctx.String("commit-id")

		didManager, signer, err := cliutil.GetDidManager(cctx, client.Cfg.KeyName)
		if err != nil {
			return err
		}

		gatewayAddress, err := client.GetNodeAddress(ctx)
		if err != nil {
			return err
		}

		queryProposal := saotypes.QueryProposal{
			Owner:   didManager.Id,
			Keyword: keyword,
			GroupId: groupId,
		}

		if !utils.IsDataId(keyword) {
			queryProposal.KeywordType = 2
		}

		request, err := buildQueryRequest(ctx, didManager, queryProposal, client, gatewayAddress)
		if err != nil {
			return err
		}

		res, err := client.QueryMetadata(ctx, request, 0)
		if err != nil {
			return err
		}

		force := cctx.Bool("force")

		operation := uint32(1)

		if force {
			operation = 2
		}

		proposal := saotypes.Proposal{
			Owner:      didManager.Id,
			Provider:   gatewayAddress,
			GroupId:    groupId,
			Duration:   uint64(time.Duration(60*60*24*duration) * time.Second / chain.Blocktime),
			Replica:    int32(replicas),
			Timeout:    int32(delay),
			DataId:     res.Metadata.DataId,
			Alias:      res.Metadata.Alias,
			Tags:       cctx.StringSlice("tags"),
			Cid:        newCid.String(),
			CommitId:   commitId + "|" + utils.GenerateCommitId(didManager.Id+groupId),
			Rule:       cctx.String("rule"),
			Operation:  operation,
			Size_:      uint64(size),
			ExtendInfo: extendInfo,
		}

		clientProposal, err := buildClientProposal(ctx, didManager, proposal, client)
		if err != nil {
			return err
		}

		var orderId uint64 = 0
		if clientPublish {
			resp, _, _, err := client.StoreOrder(ctx, signer, clientProposal)
			if err != nil {
				return err
			}
			orderId = resp.OrderId
		}

		resp, err := client.ModelUpdate(ctx, request, clientProposal, orderId, patch)
		if err != nil {
			return err
		}
		fmt.Printf("alias: %s, data id: %s, commit id: %s.\r\n", resp.Alias, resp.DataId, resp.CommitId)
		return nil
	},
}

var updatePermissionCmd = &cli.Command{
	Name:      "update-permission",
	Usage:     "update data model's permission",
	UsageText: "only data model owner can update permission",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "data-id",
			Usage:    "data model's dataId",
			Required: true,
		},
		&cli.StringSliceFlag{
			Name:     "readonly-dids",
			Usage:    "DIDs with read access to the data model",
			Required: false,
		},
		&cli.StringSliceFlag{
			Name:     "readwrite-dids",
			Usage:    "DIDs with read and write access to the data model",
			Required: false,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		if !cctx.IsSet("data-id") {
			return types.Wrapf(types.ErrInvalidParameters, "must provide --data-id")
		}
		dataId := cctx.String("data-id")
		clientPublish := cctx.Bool("client-publish")

		client, closer, err := getSaoClient(cctx)
		if err != nil {
			return err
		}
		defer closer()

		didManager, signer, err := cliutil.GetDidManager(cctx, client.Cfg.KeyName)
		if err != nil {
			return err
		}

		proposal := saotypes.PermissionProposal{
			Owner:         didManager.Id,
			DataId:        dataId,
			ReadonlyDids:  cctx.StringSlice("readonly-dids"),
			ReadwriteDids: cctx.StringSlice("readwrite-dids"),
		}

		proposalBytes, err := proposal.Marshal()
		if err != nil {
			return types.Wrap(types.ErrMarshalFailed, err)
		}

		jws, err := didManager.CreateJWS(proposalBytes)
		if err != nil {
			return types.Wrap(types.ErrCreateJwsFailed, err)
		}

		request := &types.PermissionProposal{
			Proposal: proposal,
			JwsSignature: saotypes.JwsSignature{
				Protected: jws.Signatures[0].Protected,
				Signature: jws.Signatures[0].Signature,
			},
		}

		if clientPublish {
			_, err = client.UpdatePermission(ctx, signer, request)
			if err != nil {
				return err
			}
		} else {
			_, err := client.ModelUpdatePermission(ctx, request, !clientPublish)
			if err != nil {
				return err
			}
		}

		fmt.Printf("Data model[%s]'s permission updated.\r\n", dataId)
		return nil
	},
}

var patchGenCmd = &cli.Command{
	Name:      "patch-gen",
	Usage:     "generate data model patch",
	UsageText: "used to before update cmd. you will get patch diff and target cid.",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     "origin",
			Usage:    "the original data model content",
			Required: true,
		},
		&cli.StringFlag{
			Name:     "target",
			Usage:    "the target data model content",
			Required: true,
		},
	},
	Action: func(cctx *cli.Context) error {
		if !cctx.IsSet("origin") || !cctx.IsSet("target") {
			return types.Wrapf(types.ErrInvalidParameters, "please provide both --origin and --target")
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
			return types.Wrap(types.ErrUnMarshalFailed, err)
		}

		var targetModel interface{}
		err = json.Unmarshal([]byte(target), &targetModel)
		if err != nil {
			return types.Wrap(types.ErrUnMarshalFailed, err)
		}

		valueStrNew, err := json.Marshal(newModel)
		if err != nil {
			return types.Wrap(types.ErrMarshalFailed, err)
		}

		valueStrTarget, err := json.Marshal(targetModel)
		if err != nil {
			return types.Wrap(types.ErrMarshalFailed, err)
		}

		if string(valueStrNew) != string(valueStrTarget) {
			return types.Wrapf(types.ErrCreatePatchFailed, "failed to generate the patch")
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

		fmt.Print("  Target Size : ")
		console.Println(len(content))

		return nil
	},
}

func buildClientProposal(_ context.Context, didManager *did.DidManager, proposal saotypes.Proposal, _ chain.ChainSvcApi) (*types.OrderStoreProposal, error) {
	if proposal.Owner == "all" {
		return &types.OrderStoreProposal{
			Proposal: proposal,
		}, nil
	}

	proposalBytes, err := proposal.Marshal()
	if err != nil {
		return nil, types.Wrap(types.ErrMarshalFailed, err)
	}

	jws, err := didManager.CreateJWS(proposalBytes)
	if err != nil {
		return nil, types.Wrap(types.ErrCreateJwsFailed, err)
	}
	return &types.OrderStoreProposal{
		Proposal: proposal,
		JwsSignature: saotypes.JwsSignature{
			Protected: jws.Signatures[0].Protected,
			Signature: jws.Signatures[0].Signature,
		},
	}, nil
}

func buildQueryRequest(ctx context.Context, didManager *did.DidManager, proposal saotypes.QueryProposal, chain chain.ChainSvcApi, gatewayAddress string) (*types.MetadataProposal, error) {
	lastHeight, err := chain.GetLastHeight(ctx)
	if err != nil {
		return nil, types.Wrap(types.ErrQueryHeightFailed, err)
	}

	peerInfo, err := chain.GetNodePeer(ctx, gatewayAddress)
	if err != nil {
		return nil, err
	}

	proposal.LastValidHeight = uint64(lastHeight + 200)
	proposal.Gateway = peerInfo

	if proposal.Owner == "all" {
		return &types.MetadataProposal{
			Proposal: proposal,
		}, nil
	}

	proposalBytes, err := proposal.Marshal()
	if err != nil {
		return nil, types.Wrap(types.ErrMarshalFailed, err)
	}

	jws, err := didManager.CreateJWS(proposalBytes)
	if err != nil {
		return nil, types.Wrap(types.ErrCreateJwsFailed, err)
	}

	return &types.MetadataProposal{
		Proposal: proposal,
		JwsSignature: saotypes.JwsSignature{
			Protected: jws.Signatures[0].Protected,
			Signature: jws.Signatures[0].Signature,
		},
	}, nil
}
