package server

import (
	"context"

	"github.com/pingcap-incubator/tinykv/kv/storage"
	"github.com/pingcap-incubator/tinykv/proto/pkg/kvrpcpb"
)

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
