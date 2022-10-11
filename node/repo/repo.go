package repo

import (
	"crypto/rand"
	"errors"
	"io/ioutil"
	"os"
	"path/filepath"
	"sao-storage-node/node/config"

	logging "github.com/ipfs/go-log/v2"
	"github.com/libp2p/go-libp2p/core/crypto"
	"github.com/mitchellh/go-homedir"
	"golang.org/x/xerrors"
)

var log = logging.Logger("repo")

var ErrRepoExists = xerrors.New("repo exists")

const (
	fsConfig    = "config.toml"
	fsKeystore  = "keystore"
	fsLibp2pKey = "libp2p.key"
	fsAPI       = "api"
)

var (
	ErrNoAPIEndpoint = errors.New("API not running (no endpoint)")
)

type Repo struct {
	path       string
	configPath string
}

func NewRepo(path string) (*Repo, error) {
	path, err := homedir.Expand(path)
	if err != nil {
		return nil, err
	}

	return &Repo{
		path:       path,
		configPath: filepath.Join(path, fsConfig),
	}, nil
}

func (r *Repo) Exists() (bool, error) {
	// TODO:
	_, err := os.Stat(filepath.Join(r.path, fsKeystore))
	notexist := os.IsNotExist(err)
	if notexist {
		err = nil
	}
	return !notexist, err
}

func (r *Repo) Init() error {
	exist, err := r.Exists()
	if err != nil {
		return err
	}
	if exist {
		return nil
	}

	log.Infof("Initializing repo at '%s'", r.path)
	err = os.MkdirAll(r.path, 0755) //nolint: gosec
	if err != nil && !os.IsExist(err) {
		return err
	}

	if err := r.initConfig(); err != nil {
		return xerrors.Errorf("init config: %w", err)
	}
	return r.initKeystore()
}

func (r *Repo) GeneratePeerId() (crypto.PrivKey, error) {
	pk, _, err := crypto.GenerateEd25519Key(rand.Reader)
	if err != nil {
		return nil, err
	}

	kbytes, err := crypto.MarshalPrivateKey(pk)
	if err != nil {
		return nil, err
	}

	err = r.setPeerId(kbytes)
	if err != nil {
		return nil, err
	}

	return pk, nil
}

func (r *Repo) PeerId() (crypto.PrivKey, error) {
	libp2pPath := filepath.Join(r.path, fsKeystore, fsLibp2pKey)
	key, err := ioutil.ReadFile(libp2pPath)
	if err != nil {
		return nil, err
	}
	return crypto.UnmarshalPrivateKey(key)
}

func (r *Repo) setPeerId(data []byte) error {
	libp2pPath := filepath.Join(r.path, fsKeystore, fsLibp2pKey)
	err := ioutil.WriteFile(libp2pPath, data, 0600)
	if err != nil {
		return err
	}
	return nil
}

func (r *Repo) Config() (interface{}, error) {
	return config.FromFile(r.configPath, r.defaultConfig())
}

func (r *Repo) initConfig() error {
	_, err := os.Stat(r.configPath)
	if err == nil {
		// exists
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}

	c, err := os.Create(r.configPath)
	if err != nil {
		return err
	}

	comm, err := config.NodeBytes(r.defaultConfig())
	if err != nil {
		return xerrors.Errorf("load default: %w", err)
	}
	_, err = c.Write(comm)
	if err != nil {
		return xerrors.Errorf("write config: %w", err)
	}

	if err := c.Close(); err != nil {
		return xerrors.Errorf("close config: %w", err)
	}
	return nil
}

func (r *Repo) defaultConfig() interface{} {
	return config.DefaultNode()
}

func (r *Repo) initKeystore() error {
	kstorePath := filepath.Join(r.path, fsKeystore)
	if _, err := os.Stat(kstorePath); err == nil {
		return ErrRepoExists
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Mkdir(kstorePath, 0700)
}
