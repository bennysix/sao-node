package chain

import (
	"context"
	"fmt"
	"sao-node/types"

	saodid "github.com/SaoNetwork/sao-did"
	"github.com/SaoNetwork/sao-did/parser"
	"github.com/SaoNetwork/sao-did/sid"
	saodidtypes "github.com/SaoNetwork/sao-did/types"

	sidtypes "github.com/SaoNetwork/sao/x/did/types"
)

func (c *ChainSvc) GetSidDocument(ctx context.Context, versionId string) (*sid.SidDocument, error) {
	resp, err := c.didClient.SidDocument(ctx, &sidtypes.QueryGetSidDocumentRequest{VersionId: versionId})
	if err != nil {
		return nil, types.Wrap(types.ErrGetSidDocumentFailed, err)
	}
	if resp.SidDocument.VersionId == "" {
		return nil, nil
	}
	var keys = make([]*sid.PubKey, 0)
	for _, pk := range resp.SidDocument.Keys {
		keys = append(keys, &sid.PubKey{
			Name:  pk.Name,
			Value: pk.Value,
		})
	}

	return &sid.SidDocument{
		VersionId: resp.SidDocument.VersionId,
		Keys:      keys,
	}, nil
}

func (c *ChainSvc) UpdateDidBinding(ctx context.Context, creator string, did string, accountId string) (string, error) {
	signerAcc, err := c.cosmos.Account(creator)
	if err != nil {
		return "", types.Wrap(types.ErrAccountNotFound, err)
	}

	msg := &sidtypes.MsgUpdatePaymentAddress{
		Creator:   creator,
		Did:       did,
		AccountId: accountId,
	}
	txResp, err := c.cosmos.BroadcastTx(ctx, signerAcc, msg)
	if err != nil {
		return "", types.Wrap(types.ErrTxProcessFailed, err)
	}
	if txResp.TxResponse.Code != 0 {
		return "", types.Wrapf(types.ErrTxProcessFailed, "MsgUpdatePaymentAddress tx hash=%s, code=%d", txResp.TxResponse.TxHash, txResp.TxResponse.Code)
	}
	return txResp.TxResponse.TxHash, nil
}

func (c *ChainSvc) QueryPaymentAddress(ctx context.Context, did string) (string, error) {
	msg := &sidtypes.QueryGetPaymentAddressRequest{
		Did: did,
	}
	paymentAddrResp, err := c.didClient.PaymentAddress(ctx, msg)
	if err != nil {
		return "", err
	}
	return paymentAddrResp.PaymentAddress.Address, nil
}

func (c *ChainSvc) ShowDidInfo(ctx context.Context, did string) {
	_, err := c.didClient.ValidateDid(ctx, &sidtypes.QueryValidateDidRequest{
		Did: did,
	})
	if err != nil {
		log.Error(err.Error())
		return
	}
	fmt.Println("Did: ", did)

	paymentAddressResp, err := c.didClient.PaymentAddress(ctx, &sidtypes.QueryGetPaymentAddressRequest{
		Did: did,
	})
	if err != nil {
		log.Error(err.Error())
		return
	}
	fmt.Println("PaymentAddress:", paymentAddressResp.PaymentAddress.Address)

	getSidDocFunc := func(versionId string) (*sid.SidDocument, error) {
		return c.GetSidDocument(ctx, versionId)
	}

	getDidResolutionFunc := func(did string) (saodidtypes.DidResolutionResult, error) {
		didManager, err := saodid.NewDidManagerWithDid(did, getSidDocFunc)
		if err != nil {
			return saodidtypes.DidResolutionResult{}, err
		}
		result := didManager.Resolver.Resolve(did, saodidtypes.DidResolutionOptions{})
		return result, nil
	}

	pd, err := parser.Parse(did)
	if err != nil {
		log.Error(err.Error())
		return
	}

	if pd.Method == "sid" {

		accountAuthsResp, err := c.didClient.GetAllAccountAuths(ctx, &sidtypes.QueryGetAllAccountAuthsRequest{
			Did: did,
		})
		if err != nil {
			log.Error(err.Error())
			return
		}
		fmt.Println("Accounts:")
		for index, accAuth := range accountAuthsResp.AccountAuths {
			accountIdResp, err := c.didClient.AccountId(ctx, &sidtypes.QueryGetAccountIdRequest{
				AccountDid: accAuth.AccountDid,
			})
			if err != nil {
				log.Error(err.Error())
				return
			}

			fmt.Println("  Account", index, " id: ", accountIdResp.AccountId.AccountId)
			fmt.Println("    AccountDid: ", accAuth.AccountDid)
			fmt.Println("    AccountEncryptedSeed: ", accAuth.AccountEncryptedSeed)
			fmt.Println("    SidEncryptedAccount:  ", accAuth.SidEncryptedAccount)
		}
		fmt.Println()

		pastSeedsResp, err := c.didClient.PastSeeds(ctx, &sidtypes.QueryGetPastSeedsRequest{
			Did: did,
		})
		if err == nil {
			printStringArray(pastSeedsResp.PastSeeds.Seeds, "PastSeeds", "")
			fmt.Println()
		}

		versionsResp, err := c.didClient.SidDocumentVersion(ctx, &sidtypes.QueryGetSidDocumentVersionRequest{
			DocId: pd.ID,
		})
		if err != nil {
			log.Error(err.Error())
			return
		}

		fmt.Println("DidDocument:")
		for index, version := range versionsResp.SidDocumentVersion.VersionList {
			fmt.Println("  DocId", index, ": ", version)
			didResolution, err := getDidResolutionFunc("did:sid:" + pd.ID + "?versionId=" + version)
			if err != nil {
				log.Error(err.Error())
				return
			}
			if didResolution.DidResolutionMetadata.Error != "" {
				log.Error(didResolution.DidResolutionMetadata.Error)
				return
			}

			printDidDocument(didResolution, "    ")

		}

	} else if pd.Method == "key" {
		fmt.Println("DidDocument:")
		didResolution, err := getDidResolutionFunc(did)
		if err != nil {
			log.Error(err.Error())
			return
		}
		if didResolution.DidResolutionMetadata.Error != "" {
			log.Error(didResolution.DidResolutionMetadata.Error)
			return
		}

		printDidDocument(didResolution, "  ")
	}
	fmt.Println()

}

func printDidDocument(didResolution saodidtypes.DidResolutionResult, prefix string) {
	printVm := func(vm saodidtypes.VerificationMethod) {
		fmt.Println(prefix+"  Id: ", vm.Id)
		fmt.Println(prefix+"    Type:            ", vm.Type)
		fmt.Println(prefix+"    Controller:      ", vm.Controller)
		fmt.Println(prefix+"    PublicKeyBase58: ", vm.PublicKeyBase58)
	}

	// context
	printStringArray(didResolution.DidDocument.Context, "Context", prefix)

	// id
	fmt.Println(prefix+"Id: ", didResolution.DidDocument.Id)

	// also known as
	printStringArray(didResolution.DidDocument.AlsoKnownAs, "AlsoKnownAs", prefix)

	// controller
	printStringArray(didResolution.DidDocument.Controller, "Controller", prefix)

	// verification method
	if len(didResolution.DidDocument.VerificationMethod) > 0 {
		fmt.Println(prefix + "VerificationMethods: ")
		for _, vm := range didResolution.DidDocument.VerificationMethod {
			printVm(vm)
		}
	}

	// authentication
	if len(didResolution.DidDocument.Authentication) > 0 {
		fmt.Println(prefix + "Authentication: ")
		for _, vmany := range didResolution.DidDocument.Authentication {
			switch t := vmany.(type) {
			case string:
				fmt.Println(prefix + "- " + t)
			case saodidtypes.VerificationMethod:
				printVm(t)
			}
		}
	}

	// key agreement
	if len(didResolution.DidDocument.KeyAgreement) > 0 {
		fmt.Println(prefix + "KeyAgreement: ")
		for _, vm := range didResolution.DidDocument.KeyAgreement {
			printVm(vm)
		}
	}

}

func printStringArray(array []string, name, prefix string) {
	if len(array) > 0 {
		fmt.Println(prefix + name + ": [")
		for _, controller := range array {
			fmt.Println(prefix + "  " + controller)
		}
		fmt.Println(prefix + "]")
	}
}
