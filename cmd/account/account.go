package account

import (
	"bufio"
	"fmt"
	"os"
	"sao-node/chain"
	cliutil "sao-node/cmd"
	"strings"
	"syscall"

	"github.com/urfave/cli/v2"
	"golang.org/x/term"
)

var AccountCmd = &cli.Command{
	Name:  "account",
	Usage: "account management",
	Subcommands: []*cli.Command{
		listCmd,
		createCmd,
		importCmd,
		exportCmd,
	},
}

var listCmd = &cli.Command{
	Name:  "list",
	Usage: "list all sao chain account in local keystore",
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		err := chain.List(ctx, cctx.String("repo"))
		if err != nil {
			return err
		}

		return nil
	},
}

var createCmd = &cli.Command{
	Name:  "create",
	Usage: "create a new local account with the given name",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     cliutil.FlagKeyName,
			Usage:    "account name",
			Required: true,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		name := cctx.String(cliutil.FlagKeyName)
		if !cctx.IsSet(cliutil.FlagKeyName) {
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Enter account name:")
			indata, err := reader.ReadBytes('\n')
			if err != nil {
				return err
			}
			name = strings.Replace(string(indata), "\n", "", -1)
		}

		accountName, address, mnemonic, err := chain.Create(ctx, cctx.String("repo"), name)
		if err != nil {
			return err
		}
		fmt.Println("Account: ", accountName)
		fmt.Println("Address: ", address)
		fmt.Println("Mnemonic: ", mnemonic)
		fmt.Println()

		return nil
	},
}

var exportCmd = &cli.Command{
	Name:  "export",
	Usage: "Export the given local account's encrypted private key",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     cliutil.FlagKeyName,
			Usage:    "account name to export",
			Required: true,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		name := cctx.String(cliutil.FlagKeyName)
		if !cctx.IsSet(cliutil.FlagKeyName) {
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Enter account name:")
			indata, err := reader.ReadBytes('\n')
			if err != nil {
				return err
			}
			name = strings.Replace(string(indata), "\n", "", -1)
		}

		fmt.Print("Enter passphrase:")
		passphrase, err := term.ReadPassword(syscall.Stdin)
		if err != nil {
			return err
		}

		err = chain.Export(ctx, cctx.String("repo"), name, string(passphrase))
		if err != nil {
			return err
		}

		return nil
	},
}

var importCmd = &cli.Command{
	Name: "import",
	Flags: []cli.Flag{
		&cli.StringFlag{
			Name:     cliutil.FlagKeyName,
			Usage:    "account name to import",
			Required: true,
		},
	},
	Action: func(cctx *cli.Context) error {
		ctx := cctx.Context

		name := cctx.String(cliutil.FlagKeyName)
		if !cctx.IsSet(cliutil.FlagKeyName) {
			reader := bufio.NewReader(os.Stdin)
			fmt.Print("Enter account name:")
			indata, err := reader.ReadBytes('\n')
			if err != nil {
				return err
			}
			name = strings.Replace(string(indata), "\n", "", -1)
		}

		fmt.Println("Enter secret:")
		var secret string
		reader := bufio.NewReader(os.Stdin)
		for {
			// read line from stdin using newline as separator
			line, err := reader.ReadString('\n')
			if err != nil {
				return err
			}

			secret = secret + line

			if strings.Contains(line, "-----END TENDERMINT PRIVATE KEY-----") {
				break
			}
		}

		fmt.Print("Enter passphrase:")
		passphrase, err := term.ReadPassword(syscall.Stdin)
		if err != nil {
			return err
		}

		err = chain.Import(ctx, cctx.String("repo"), name, secret, string(passphrase))
		if err != nil {
			return err
		}

		return nil
	},
}
