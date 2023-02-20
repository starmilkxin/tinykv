package standalone_storage

import (
	"path"

	"github.com/pingcap-incubator/tinykv/kv/config"
	"github.com/pingcap-incubator/tinykv/kv/storage"
	"github.com/pingcap-incubator/tinykv/kv/util/engine_util"
	"github.com/pingcap-incubator/tinykv/proto/pkg/kvrpcpb"

	"github.com/Connor1996/badger"
)

// StandAloneStorage is an implementation of `Storage` for a single-node TinyKV instance. It does not
// communicate with other nodes and all data is stored locally.
type StandAloneStorage struct {
	// Your Data Here (1).
	engines *engine_util.Engines
	config  *config.Config
}

func NewStandAloneStorage(conf *config.Config) *StandAloneStorage {
	// Your Code Here (1).
	dBPath := conf.DBPath
	kvPath := path.Join(dBPath, "kv")
	raftPath := path.Join(dBPath, "raft")
	kvEngine := engine_util.CreateDB(kvPath, false)
	raftEngine := engine_util.CreateDB(raftPath, true)
	return &StandAloneStorage{
		engines: engine_util.NewEngines(kvEngine, raftEngine, kvPath, raftPath),
		config:  conf,
	}
}

func (s *StandAloneStorage) Start() error {
	// Your Code Here (1).
	return nil
}

func (s *StandAloneStorage) Stop() error {
	// Your Code Here (1).
	return nil
}

func (s *StandAloneStorage) Reader(ctx *kvrpcpb.Context) (storage.StorageReader, error) {
	// Your Code Here (1).
	txn := s.engines.Kv.NewTransaction(false)
	return NewStandAloneStorageReader(txn), nil
}

func (s *StandAloneStorage) Write(ctx *kvrpcpb.Context, batch []storage.Modify) error {
	// Your Code Here (1).
	var err error
	for _, modify := range batch {
		switch modify.Data.(type) {
		case storage.Put:
			err = engine_util.PutCF(s.engines.Kv, modify.Cf(), modify.Key(), modify.Value())
		case storage.Delete:
			err = engine_util.DeleteCF(s.engines.Kv, modify.Cf(), modify.Key())
		default:
			println("StandAloneStorage Write unknown type: %s", modify.Data)
		}
		if err != nil {
			break
		}
	}
	return err
}

type StandAloneStorageReader struct {
	txn *badger.Txn
}

func (s *StandAloneStorageReader) GetCF(cf string, key []byte) ([]byte, error) {
	val, err := engine_util.GetCFFromTxn(s.txn, cf, key)
	if err == badger.ErrKeyNotFound {
		err = nil
	}
	return val, err
}

func (s *StandAloneStorageReader) IterCF(cf string) engine_util.DBIterator {
	return engine_util.NewCFIterator(cf, s.txn)
}

func (s *StandAloneStorageReader) Close() {
	s.txn.Discard()
}

func NewStandAloneStorageReader(txn *badger.Txn) storage.StorageReader {
	return &StandAloneStorageReader{txn: txn}
}
