package storage

import (
	"fmt"
	"github.com/ipfs/go-cid"
	"github.com/mitchellh/go-homedir"
	"golang.org/x/xerrors"
	"os"
	"path/filepath"
)

type ShardStaging struct {
	basedir string
}

func NewShardStaging(basedir string) ShardStaging {
	return ShardStaging{
		basedir: basedir,
	}
}

func (ss *ShardStaging) StageShard( /*orderId uint64, */ creator string, cid cid.Cid, content []byte) error {
	// TODO: check enough space
	// TODO: check existence
	path, err := homedir.Expand(ss.basedir)
	if err != nil {
		return err
	}

	err = os.MkdirAll(filepath.Join(path, creator), 0755)
	if err != nil && !os.IsExist(err) {
		return err
	}

	//filename := fmt.Sprintf("%d-%v", orderId, cid)
	filename := fmt.Sprintf("%v", cid)
	log.Info("path: ", path)
	log.Info("staging filename: ", filename)
	file, err := os.Create(filepath.Join(path, creator, filename))
	if err != nil {
		return err
	}
	defer file.Close()

	_, err = file.Write(content)
	if err != nil {
		return err
	}
	return nil
}

func (ss *ShardStaging) GetStagedShard(creator string, cid cid.Cid) ([]byte, error) {
	//var retry = 0
	//for retry < 1 {
	path, err := homedir.Expand(ss.basedir)
	if err != nil {
		return nil, err
	}

	//filename := fmt.Sprintf("%d-%v", orderId, cid)
	filename := fmt.Sprintf("%v", cid)
	bytes, err := os.ReadFile(filepath.Join(path, creator, filename))
	if err != nil {
		//if os.IsNotExist(err) {
		//	time.Sleep(time.Second * 2)
		//	retry++
		//} else {
		//	log.Error(err.Error())
		return nil, err
		//}
	} else {
		return bytes, nil
	}
	//}

	return nil, xerrors.Errorf("not able to get the shard for order: %d", cid)
}
