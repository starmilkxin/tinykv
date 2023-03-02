# Project1 StandaloneKV

项目1的目标是构建一个单机的基于badger的键值存储grpc服务。其中还用到了列族。列族（Column family，以下简称CF）是一个类似于键命名空间的术语，即同一个键在不同列族中的值是不一样的。可以简单地将多个列族视为单独的迷你数据库。TinyKV的CF用于支持project4中的事务模型。

总共两个步骤：

1. 实现一个独立的存储引擎。
2. 实现一个原始的键/值服务处理程序。

## 实现一个独立的存储引擎

首先先自定义StandAloneStorage类及其实例化函数:

```go
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
```

独立存储引擎中包含有多个引擎，kv和raft引擎:

```go
// Engines keeps references to and data for the engines used by unistore.
// All engines are badger key/value databases.
// the Path fields are the filesystem path to where the data is stored.
type Engines struct {
    // Data, including data which is committed (i.e., committed across other nodes) and un-committed (i.e., only present
    // locally).
    Kv     *badger.DB
    KvPath string
    // Metadata used by Raft.
    Raft     *badger.DB
    RaftPath string
}
```

接下来编写StandAloneStorage的Write函数，用于kv数据的增、改、删。  
根据批量传入的modify中的Data类型，判断是增/改还是删。增/改需用用到modify的Cf，Key与Value。删只用modify的Cf与Key。
需要注意的是删除操作如果返回的err为ErrKeyNotFound，则需要将err置为nil:

```go
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
		if err == badger.ErrKeyNotFound {
			err = nil
		}
		if err != nil {
			break
		}
	}
	return err
}
```

其中modify中的Data数据类型是接口，传入Put或Delete类，Delete类相比Put类的属性少了个Value:

```go
// Modify is a single modification to TinyKV's underlying storage.
type Modify struct {
	Data interface{}
}

type Put struct {
	Key   []byte
	Value []byte
	Cf    string
}

type Delete struct {
	Key []byte
	Cf  string
}
```

StandAloneStorageReader用于对kv数据的读，可以为单独读或批量读。  
创建事务，根据事务实例化一个StandAloneStorageReader。  
IterCF函数根据CF获取迭代器，GetCF函数根据CF与key获取对应的value。  
需要注意的是与删除操作一样，GetCF中如果出现ErrKeyNotFound错误，也是将err置为nil:

```go
type StandAloneStorageReader struct {
	txn *badger.Txn
}

func NewStandAloneStorageReader(txn *badger.Txn) storage.StorageReader {
	return &StandAloneStorageReader{txn: txn}
}

func (s *StandAloneStorage) Reader(ctx *kvrpcpb.Context) (storage.StorageReader, error) {
	// Your Code Here (1).
	txn := s.engines.Kv.NewTransaction(false)
	return NewStandAloneStorageReader(txn), nil
}

func (s *StandAloneStorageReader) IterCF(cf string) engine_util.DBIterator {
	return engine_util.NewCFIterator(cf, s.txn)
}

func (s *StandAloneStorageReader) GetCF(cf string, key []byte) ([]byte, error) {
	val, err := engine_util.GetCFFromTxn(s.txn, cf, key)
	if err == badger.ErrKeyNotFound {
		err = nil
	}
	return val, err
}
```

## 实现一个原始的键/值服务处理程序

编写Server类的函数，Server中有一个属性为storage是接口，我们的StandAloneStorage实现了该接口，通过对StandAloneStorage的函数进一步封装来完成Server函数。  
首先是增/改、删、查:

```go
// The functions below are Server's Raw API. (implements TinyKvServer).
// Some helper methods can be found in sever.go in the current directory

// RawGet return the corresponding Get response based on RawGetRequest's CF and Key fields
func (server *Server) RawGet(_ context.Context, req *kvrpcpb.RawGetRequest) (*kvrpcpb.RawGetResponse, error) {
	// Your Code Here (1).
	var err error
	var reader storage.StorageReader
	reader, err = server.storage.Reader(req.GetContext())
	if err != nil {
		return nil, err
	}
	var val []byte
	val, err = reader.GetCF(req.GetCf(), req.GetKey())
	if err != nil {
		return nil, err
	}
	notFound := false
	if val == nil {
		notFound = true
	}
	resp := &kvrpcpb.RawGetResponse{
		Value:    val,
		NotFound: notFound,
	}
	return resp, err
}

// RawPut puts the target data into storage and returns the corresponding response
func (server *Server) RawPut(_ context.Context, req *kvrpcpb.RawPutRequest) (*kvrpcpb.RawPutResponse, error) {
	// Your Code Here (1).
	// Hint: Consider using Storage.Modify to store data to be modified
	put := storage.Put{
		Key:   req.GetKey(),
		Value: req.GetValue(),
		Cf:    req.GetCf(),
	}
	batch := storage.Modify{Data: put}
	err := server.storage.Write(req.GetContext(), []storage.Modify{batch})
	if err != nil {
		return nil, err
	}
	return &kvrpcpb.RawPutResponse{}, err
}

// RawDelete delete the target data from storage and returns the corresponding response
func (server *Server) RawDelete(_ context.Context, req *kvrpcpb.RawDeleteRequest) (*kvrpcpb.RawDeleteResponse, error) {
	// Your Code Here (1).
	// Hint: Consider using Storage.Modify to store data to be deleted
	del := storage.Delete{
		Key: req.GetKey(),
		Cf:  req.GetCf(),
	}
	batch := storage.Modify{Data: del}
	err := server.storage.Write(req.GetContext(), []storage.Modify{batch})
	if err != nil {
		return nil, err
	}
	return &kvrpcpb.RawDeleteResponse{}, err
}
```

RawScan批量读，创建reader，根据CF获取该CF的迭代器，根据请求中的StartKey决定迭代的起点，根据请求中的limit来决定迭代次数。

```go
// RawScan scan the data starting from the start key up to limit. and return the corresponding result
func (server *Server) RawScan(_ context.Context, req *kvrpcpb.RawScanRequest) (*kvrpcpb.RawScanResponse, error) {
	// Your Code Here (1).
	// Hint: Consider using reader.IterCF
	var err error
	var reader storage.StorageReader
	reader, err = server.storage.Reader(req.GetContext())
	if err != nil {
		return nil, err
	}
	iterCF := reader.IterCF(req.GetCf())
	defer iterCF.Close()
	var kvs []*kvrpcpb.KvPair
	limit := req.Limit
	for iterCF.Seek(req.GetStartKey()); iterCF.Valid(); iterCF.Next() {
		item := iterCF.Item()
		var key []byte
		var val []byte
		key = item.Key()
		val, err = item.Value()
		if err != nil {
			return nil, err
		}
		kvPair := &kvrpcpb.KvPair{
			Key:   key,
			Value: val,
		}
		kvs = append(kvs, kvPair)
		limit--
		if limit == 0 {
			break
		}
	}
	rawScanResponse := &kvrpcpb.RawScanResponse{
		Kvs: kvs,
	}
	return rawScanResponse, err
}
```