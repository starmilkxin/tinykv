// Copyright 2015 The etcd Authors
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package raft

import pb "github.com/pingcap-incubator/tinykv/proto/pkg/eraftpb"

// RaftLog manage the log entries, its struct look like:
//
//  snapshot/first.....applied....committed....stabled.....last
//  --------|------------------------------------------------|
//                            log entries
//
// for simplify the RaftLog implement should manage all log entries
// that not truncated
type RaftLog struct {
	// storage contains all stable entries since the last snapshot.
	storage Storage

	// committed is the highest log position that is known to be in
	// stable storage on a quorum of nodes.
	committed uint64

	// applied is the highest log position that the application has
	// been instructed to apply to its state machine.
	// Invariant: applied <= committed
	applied uint64

	// log entries with index <= stabled are persisted to storage.
	// It is used to record the logs that are not persisted by storage yet.
	// Everytime handling `Ready`, the unstabled logs will be included.
	stabled uint64

	// all entries that have not yet compact.
	entries []pb.Entry

	// the incoming unstable snapshot, if any.
	// (Used in 2C)
	pendingSnapshot *pb.Snapshot

	// Your Data Here (2A).
}

// newLog returns log using the given storage. It recovers the log
// to the state that it just commits and applies the latest snapshot.
func newLog(storage Storage) *RaftLog {
	// Your Code Here (2A).
	firstIdx, _ := storage.FirstIndex()
	lastIdx, _ := storage.LastIndex()
	entries, _ := storage.Entries(firstIdx, lastIdx+1)
	hardState, _, _ := storage.InitialState()
	raftLog := &RaftLog{
		storage:         storage,
		committed:       hardState.GetCommit(),
		applied:         firstIdx - 1,
		stabled:         lastIdx,
		entries:         entries,
		pendingSnapshot: nil,
	}
	return raftLog
}

// We need to compact the log entries in some point of time like
// storage compact stabled log entries prevent the log entries
// grow unlimitedly in memory
func (l *RaftLog) maybeCompact() {
	// Your Code Here (2C).
}

// allEntries return all the entries not compacted.
// note, exclude any dummy entries from the return value.
// note, this is one of the test stub functions you need to implement.
func (l *RaftLog) allEntries() []pb.Entry {
	// Your Code Here (2A).
	return l.entries
}

// unstableEntries return all the unstable entries
func (l *RaftLog) unstableEntries() []pb.Entry {
	// Your Code Here (2A).
	var offset uint64
	if len(l.entries) > 0 {
		offset = l.entries[0].GetIndex()
	}
	if len(l.entries) > 0 && l.stabled-offset+1 <= uint64(len(l.entries)) {
		return l.entries[l.stabled-offset+1:]
	}
	return []pb.Entry{}
}

// nextEnts returns all the committed but not applied entries
func (l *RaftLog) nextEnts() (ents []pb.Entry) {
	// Your Code Here (2A).
	if len(l.entries) == 0 {
		return nil
	}
	offset := l.entries[0].GetIndex()
	return l.entries[l.applied+1-offset : l.committed+1-offset]
}

// LastIndex return the last index of the log entries
func (l *RaftLog) LastIndex() uint64 {
	// Your Code Here (2A).
	if len(l.entries) == 0 {
		lastIndex, _ := l.storage.LastIndex()
		return lastIndex
	}
	return l.entries[0].GetIndex() + uint64(len(l.entries)) - 1
}

// Term return the term of the entry in the given index
func (l *RaftLog) Term(i uint64) (uint64, error) {
	// Your Code Here (2A).
	var offset uint64
	if len(l.entries) == 0 {
		return 0, nil
	}
	offset = l.entries[0].GetIndex()
	if i < offset {
		return l.storage.Term(i)
	}
	if int(i-offset+1) > len(l.entries) {
		return 0, ErrUnavailable
	}
	return l.entries[i-offset].GetTerm(), nil
}

func (l *RaftLog) DeleteAndAppendEntry(appendEntry pb.Entry) {
	for idx, entry := range l.entries {
		if entry.GetIndex() != appendEntry.GetIndex() {
			continue
		}
		if idx == 0 {
			l.entries = []pb.Entry{}
			l.stabled = 0
		} else {
			l.entries = l.entries[:idx]
		}
		break
	}
	l.entries = append(l.entries, appendEntry)
	lastIndex := l.LastIndex()
	l.committed = min(l.committed, lastIndex-1)
	l.applied = min(l.applied, lastIndex-1)
	l.stabled = min(l.stabled, lastIndex-1)
}
