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

import (
	"errors"
	"math/rand"
	"sort"

	pb "github.com/pingcap-incubator/tinykv/proto/pkg/eraftpb"
)

// None is a placeholder node ID used when there is no leader.
const None uint64 = 0

// StateType represents the role of a node in a cluster.
type StateType uint64

const (
	StateFollower StateType = iota
	StateCandidate
	StateLeader
)

var stmap = [...]string{
	"StateFollower",
	"StateCandidate",
	"StateLeader",
}

func (st StateType) String() string {
	return stmap[uint64(st)]
}

// ErrProposalDropped is returned when the proposal is ignored by some cases,
// so that the proposer can be notified and fail fast.
var ErrProposalDropped = errors.New("raft proposal dropped")

// Config contains the parameters to start a raft.
type Config struct {
	// ID is the identity of the local raft. ID cannot be 0.
	ID uint64

	// peers contains the IDs of all nodes (including self) in the raft cluster. It
	// should only be set when starting a new raft cluster. Restarting raft from
	// previous configuration will panic if peers is set. peer is private and only
	// used for testing right now.
	peers []uint64

	// ElectionTick is the number of Node.Tick invocations that must pass between
	// elections. That is, if a follower does not receive any message from the
	// leader of current term before ElectionTick has elapsed, it will become
	// candidate and start an election. ElectionTick must be greater than
	// HeartbeatTick. We suggest ElectionTick = 10 * HeartbeatTick to avoid
	// unnecessary leader switching.
	ElectionTick int
	// HeartbeatTick is the number of Node.Tick invocations that must pass between
	// heartbeats. That is, a leader sends heartbeat messages to maintain its
	// leadership every HeartbeatTick ticks.
	HeartbeatTick int

	// Storage is the storage for raft. raft generates entries and states to be
	// stored in storage. raft reads the persisted entries and states out of
	// Storage when it needs. raft reads out the previous state and configuration
	// out of storage when restarting.
	Storage Storage
	// Applied is the last applied index. It should only be set when restarting
	// raft. raft will not return entries to the application smaller or equal to
	// Applied. If Applied is unset when restarting, raft might return previous
	// applied entries. This is a very application dependent configuration.
	Applied uint64
}

func (c *Config) validate() error {
	if c.ID == None {
		return errors.New("cannot use none as id")
	}

	if c.HeartbeatTick <= 0 {
		return errors.New("heartbeat tick must be greater than 0")
	}

	if c.ElectionTick <= c.HeartbeatTick {
		return errors.New("election tick must be greater than heartbeat tick")
	}

	if c.Storage == nil {
		return errors.New("storage cannot be nil")
	}

	return nil
}

// Progress represents a follower’s progress in the view of the leader. Leader maintains
// progresses of all followers, and sends entries to the follower based on its progress.
type Progress struct {
	Match, Next uint64
}

type Raft struct {
	id uint64

	Term uint64
	Vote uint64

	// the log
	RaftLog *RaftLog

	// log replication progress of each peers
	Prs map[uint64]*Progress

	// this peer's role
	State StateType

	// votes records
	votes map[uint64]bool

	// msgs need to send
	msgs []pb.Message

	// the leader id
	Lead uint64

	// heartbeat interval, should send
	heartbeatTimeout int
	// baseline of election interval
	electionTimeout       int
	electionRandomTimeout int
	// number of ticks since it reached last heartbeatTimeout.
	// only leader keeps heartbeatElapsed.
	heartbeatElapsed int
	// Ticks since it reached last electionTimeout when it is leader or candidate.
	// Number of ticks since it reached last electionTimeout or received a
	// valid message from current leader when it is a follower.
	electionElapsed int

	// leadTransferee is id of the leader transfer target when its value is not zero.
	// Follow the procedure defined in section 3.10 of Raft phd thesis.
	// (https://web.stanford.edu/~ouster/cgi-bin/papers/OngaroPhD.pdf)
	// (Used in 3A leader transfer)
	leadTransferee uint64

	// Only one conf change may be pending (in the log, but not yet
	// applied) at a time. This is enforced via PendingConfIndex, which
	// is set to a value >= the log index of the latest pending
	// configuration change (if any). Config changes are only allowed to
	// be proposed if the leader's applied index is greater than this
	// value.
	// (Used in 3A conf change)
	PendingConfIndex uint64
}

// newRaft return a raft peer with the given config
func newRaft(c *Config) *Raft {
	if err := c.validate(); err != nil {
		panic(err.Error())
	}
	// Your Code Here (2A).
	// 获取持久化存储的信息
	hardState, confState, _ := c.Storage.InitialState()
	raft := &Raft{
		id:                    c.ID,
		Term:                  hardState.GetTerm(),
		Vote:                  hardState.GetVote(),
		RaftLog:               newLog(c.Storage),
		Prs:                   make(map[uint64]*Progress),
		State:                 StateFollower,
		votes:                 nil,
		msgs:                  make([]pb.Message, 0),
		Lead:                  0,
		heartbeatTimeout:      c.HeartbeatTick,
		electionTimeout:       c.ElectionTick,
		electionRandomTimeout: 0,
		heartbeatElapsed:      0,
		electionElapsed:       0,
		leadTransferee:        0,
		PendingConfIndex:      0,
	}
	raft.electionRandomTimeout = raft.createElectionRandomTimeout()
	if raft.Term == 0 {
		raft.Term, _ = raft.RaftLog.Term(raft.RaftLog.LastIndex())
	}
	nodes := c.peers
	if nodes == nil {
		nodes = confState.Nodes
	}
	for _, node := range nodes {
		if node == raft.id {
			raft.Prs[node] = &Progress{
				Match: raft.RaftLog.LastIndex(),
				Next:  raft.RaftLog.LastIndex() + 1,
			}
		} else {
			raft.Prs[node] = &Progress{
				Match: 0,
				Next:  1,
			}
		}
	}
	if c.Applied > 0 {
		raft.RaftLog.applied = c.Applied
	}
	return raft
}

func (r *Raft) createElectionRandomTimeout() int {
	return rand.Intn(r.electionTimeout) + r.electionTimeout
}

// tick advances the internal logical clock by a single tick.
func (r *Raft) tick() {
	// Your Code Here (2A).
	switch r.State {
	case StateFollower:
		r.followerTrick()
	case StateCandidate:
		r.candidateTrick()
	case StateLeader:
		r.leaderTrick()
	default:
		print("raft trick, unknown state: %s", r.State)
	}
}

func (r *Raft) followerTrick() {
	r.electionElapsed++
	// 选举结束还未决出leader，参加选举
	if r.electionElapsed >= r.electionRandomTimeout {
		r.electionElapsed = 0
		// 发送Local Msg
		_ = r.Step(pb.Message{
			MsgType: pb.MessageType_MsgHup,
		})
	}
}

func (r *Raft) candidateTrick() {
	r.electionElapsed++
	// 选举结束还未决出leader，参加选举
	if r.electionElapsed >= r.electionRandomTimeout {
		r.electionElapsed = 0
		// 发送Local Msg
		_ = r.Step(pb.Message{
			MsgType: pb.MessageType_MsgHup,
		})
	}
}

func (r *Raft) leaderTrick() {
	r.heartbeatElapsed++
	// 该向其他所有节点发送心跳包了
	if r.heartbeatElapsed >= r.heartbeatTimeout {
		r.heartbeatElapsed = 0
		// 发送Local Msg
		_ = r.Step(pb.Message{
			MsgType: pb.MessageType_MsgBeat,
		})
	}
}

// becomeFollower transform this peer's state to Follower
func (r *Raft) becomeFollower(term uint64, lead uint64) {
	// Your Code Here (2A).
	r.State = StateFollower
	r.Term = term
	r.Lead = lead
	r.Vote = lead
	r.votes = map[uint64]bool{}
	r.electionRandomTimeout = r.createElectionRandomTimeout()
	r.electionElapsed = 0
}

// becomeCandidate transform this peer's state to candidate
func (r *Raft) becomeCandidate() {
	// Your Code Here (2A).
	r.State = StateCandidate
	// 任期增加
	r.Term++
	r.Lead = 0
	r.Vote = 0
	r.votes = map[uint64]bool{}
	r.electionRandomTimeout = r.createElectionRandomTimeout()
	r.electionElapsed = 0
}

// becomeLeader transform this peer's state to leader
func (r *Raft) becomeLeader() {
	// Your Code Here (2A).
	// NOTE: Leader should propose a noop entry on its term
	r.State = StateLeader
	r.heartbeatElapsed = 0
	r.Lead = r.id
	for k := range r.Prs {
		r.Prs[k] = &Progress{
			Match: 0,
			Next:  r.RaftLog.LastIndex() + 1, // raft 论文
		}
	}
	// NOTE: Leader should propose a noop entry on its term
	r.RaftLog.entries = append(r.RaftLog.entries, pb.Entry{
		EntryType: pb.EntryType_EntryNormal,
		Term:      r.Term,
		Index:     r.RaftLog.LastIndex() + 1,
		Data:      nil,
	})
	// 只有自己的特殊情况
	if 1 > len(r.Prs)/2 {
		r.RaftLog.committed = r.RaftLog.LastIndex()
	}
	r.Prs[r.id] = &Progress{
		Match: r.RaftLog.LastIndex(),
		Next:  r.RaftLog.LastIndex() + 1,
	}
	// 成为领导人立刻开始发送附加日志 RPC
	for k := range r.Prs {
		if k != r.id {
			r.sendAppend(k)
		}
	}
}

// Step the entrance of handle message, see `MessageType`
// on `eraftpb.proto` for what msgs should be handled
// follower可以寄接收到的消息:
// MsgAppend(leader发送的日志追加条目)、MsgRequestVote(candidate发送的请求投票)、MsgHeartbeat(leader发送的心跳包)
// candidate可以寄接收到的消息:
// MsgHup(自己竞选leader)、MsgAppend(leader发送的日志追加条目)、MsgRequestVoteResponse(请求投票的反馈)、
// MsgHeartbeat(leader发送的心跳包)
// leader可以寄接收到的消息:
// MsgBeat(给所有其他节点发送心跳包)、MsgPropose(给其他节点发送追加日志rpc)、MsgAppendResponse(日志追加反馈)、
// MsgRequestVote(candidate发送的请求投票)、MsgSnapshot(给其他节点发送快照)、MsgHeartbeatResponse(心跳包反馈)、
// MsgTransferLeader(???)、MsgTimeoutNow(???)
func (r *Raft) Step(m pb.Message) error {
	// Your Code Here (2A).
	_, ok := r.Prs[r.id]
	if !ok && m.GetMsgType() == pb.MessageType_MsgTimeoutNow {
		return nil // 自己给 remove 掉了
	}
	if m.GetTerm() > r.Term {
		r.becomeFollower(m.GetTerm(), None)
	}
	var err error
	switch m.GetMsgType() {
	case pb.MessageType_MsgHup:
		switch r.State {
		case StateFollower:
			r.handleHup()
		case StateCandidate:
			r.handleHup()
		case StateLeader:
		}
	case pb.MessageType_MsgBeat:
		switch r.State {
		case StateFollower:
		case StateCandidate:
		case StateLeader:
			r.handleBeat()
		}
	case pb.MessageType_MsgPropose:
		switch r.State {
		case StateFollower:
			err = ErrProposalDropped
		case StateCandidate:
			err = ErrProposalDropped
		case StateLeader:
			err = r.handlePropose(m)
		}
	case pb.MessageType_MsgAppend:
		switch r.State {
		case StateFollower:
			r.handleAppendEntries(m)
		case StateCandidate:
			r.handleAppendEntries(m)
		case StateLeader:
		}
	case pb.MessageType_MsgAppendResponse:
		switch r.State {
		case StateFollower:
		case StateCandidate:
		case StateLeader:
			r.handleAppendResponse(m)
		}
	case pb.MessageType_MsgRequestVote:
		switch r.State {
		case StateFollower:
			r.handleRequestVote(m)
		case StateCandidate:
			r.handleRequestVote(m)
		case StateLeader:
			r.handleRequestVote(m)
		}
	case pb.MessageType_MsgRequestVoteResponse:
		switch r.State {
		case StateFollower:
		case StateCandidate:
			r.handleRequestVoteResponse(m)
		case StateLeader:
		}
	case pb.MessageType_MsgSnapshot:
		switch r.State {
		case StateFollower:

		case StateCandidate:

		case StateLeader:

		}
	case pb.MessageType_MsgHeartbeat:
		switch r.State {
		case StateFollower:
			r.handleHeartbeat(m)
		case StateCandidate:
			r.handleHeartbeat(m)
		case StateLeader:
			r.handleHeartbeat(m)
		}
	case pb.MessageType_MsgHeartbeatResponse:
		switch r.State {
		case StateFollower:
		case StateCandidate:
		case StateLeader:
			r.handleHeartbeatResponse(m)
		}
	case pb.MessageType_MsgTransferLeader:
		switch r.State {
		case StateFollower:

		case StateCandidate:

		case StateLeader:

		}
	case pb.MessageType_MsgTimeoutNow:
		switch r.State {
		case StateFollower:

		case StateCandidate:

		case StateLeader:

		}
	}
	return err
}

// sendHeartbeat sends a heartbeat RPC to the given peer.
func (r *Raft) sendHeartbeat(to uint64) {
	// Your Code Here (2A).
	msg := pb.Message{
		MsgType: pb.MessageType_MsgHeartbeat,
		To:      to,
		From:    r.id,
		Term:    r.Term,
	}
	r.msgs = append(r.msgs, msg)
}

// sendAppend sends an append RPC with new entries (if any) and the
// current commit index to the given peer. Returns true if a message was sent.
func (r *Raft) sendAppend(to uint64) bool {
	// Your Code Here (2A).
	prevIndex := r.Prs[to].Next - 1
	prevTerm, prevTermErr := r.RaftLog.Term(prevIndex)
	// 如果目标节点需要的最早日志不在当前节点内存中，则要发送的entry已被压缩，发送当前快照
	if prevTermErr != nil {
		r.sendSnapshot(to)
		return false
	}
	// 根据目标节点的next发送entry
	entries := make([]*pb.Entry, 0)
	if len(r.RaftLog.entries) > 0 {
		offset := r.RaftLog.entries[0].Index
		for idx := prevIndex + 1 - offset; int(idx) < len(r.RaftLog.entries); idx++ {
			entry := r.RaftLog.entries[idx]
			entries = append(entries, &pb.Entry{
				EntryType: entry.GetEntryType(),
				Term:      entry.GetTerm(),
				Index:     entry.GetIndex(),
				Data:      entry.GetData(),
			})
		}
	}
	r.msgs = append(r.msgs, pb.Message{
		MsgType: pb.MessageType_MsgAppend,
		To:      to,
		From:    r.id,
		Term:    r.Term,
		LogTerm: prevTerm,  // 要发送的entries前一个term
		Index:   prevIndex, // 要发送的entries前一个index
		Entries: entries,
		Commit:  r.RaftLog.committed,
	})
	return true
}

func (r *Raft) sendSnapshot(to uint64) {
	snapshot, err := r.RaftLog.storage.Snapshot()
	if err != nil {
		return
	}
	msg := pb.Message{
		MsgType:  pb.MessageType_MsgSnapshot,
		From:     r.id,
		To:       to,
		Term:     r.Term,
		Snapshot: &snapshot,
	}
	r.msgs = append(r.msgs, msg)
	r.Prs[to].Next = snapshot.Metadata.Index + 1
}

func (r *Raft) handleHup() {
	// 判断自身是否存在
	if r.Prs[r.id] == nil {
		return
	}
	// follower升级为candidate
	r.becomeCandidate()
	// 投票给自身
	r.Vote = r.id
	r.votes[r.id] = true
	// 如果region中只有自己一个节点，则直接成为leader
	if len(r.Prs) == 1 {
		r.becomeLeader()
		return
	}
	// 向其他所有节点发送 MsgRequestVote
	for node, _ := range r.Prs {
		if node == r.id {
			continue
		}
		logTerm, _ := r.RaftLog.Term(r.RaftLog.LastIndex())
		msg := pb.Message{
			MsgType: pb.MessageType_MsgRequestVote,
			To:      node,
			From:    r.id,
			Term:    r.Term,
			LogTerm: logTerm,
			Index:   r.RaftLog.LastIndex(),
		}
		r.msgs = append(r.msgs, msg)
	}
}

func (r *Raft) handleBeat() {
	if r.State != StateLeader {
		return
	}
	// 向其他所有节点发送心跳
	for node, _ := range r.Prs {
		if node == r.id {
			continue
		}
		r.sendHeartbeat(node)
	}
}

func (r *Raft) handlePropose(m pb.Message) error {
	// 判断 r.leadTransferee 是否等于 None，如果不是，返回 ErrProposalDropped，因为此时集群正在转移 Leader
	if r.leadTransferee != None {
		return ErrProposalDropped
	}
	// 把 m.Entries追加到自己的 Entries 中
	for _, entry := range m.Entries {
		r.RaftLog.entries = append(r.RaftLog.entries, pb.Entry{
			EntryType: entry.EntryType,
			Term:      r.Term,
			Index:     r.RaftLog.LastIndex() + 1,
			Data:      entry.Data,
		})
	}
	r.Prs[r.id].Match = r.RaftLog.LastIndex()
	r.Prs[r.id].Next = r.RaftLog.LastIndex() + 1
	// 向其他所有节点发送追加日志 RPC，即 MsgAppend，用于集群同步
	for node, _ := range r.Prs {
		if node == r.id {
			continue
		}
		_ = r.sendAppend(node)
	}
	// 如果集群中只有自己一个节点，则直接更新自己的 committedIndex
	if r.Prs[r.id] != nil && len(r.Prs) == 1 {
		r.RaftLog.committed = r.RaftLog.LastIndex()
	}
	return nil
}

// handleAppendEntries handle AppendEntries RPC request
func (r *Raft) handleAppendEntries(m pb.Message) {
	// Your Code Here (2A).
	if m.GetTerm() == r.Term {
		r.becomeFollower(m.GetTerm(), m.GetFrom())
	}
	msgResp := pb.Message{
		MsgType: pb.MessageType_MsgAppendResponse,
		To:      m.GetFrom(),
		From:    r.id,
		Term:    r.Term,
		Index:   r.RaftLog.LastIndex(),
		Reject:  true,
	}
	// 如果发送者 的 Term 小于 接收者的 Term，拒绝
	if m.GetTerm() < r.Term {
		r.msgs = append(r.msgs, msgResp)
		return
	}
	// 如果 prevLogIndex > r.RaftLog.LastIndex()，则表示entry断连，拒绝
	if m.GetIndex() > r.RaftLog.LastIndex() {
		r.msgs = append(r.msgs, msgResp)
		return
	}
	// 如果接收者日志中没有包含这样一个条目：即该条目的任期在 prevLogIndex 上无法和 prevLogTerm 匹配上，拒绝
	if len(r.RaftLog.entries) > 0 {
		offset := r.RaftLog.entries[0].GetIndex()
		if m.GetIndex() >= offset && m.GetLogTerm() != r.RaftLog.entries[m.GetIndex()-offset].GetTerm() {
			r.msgs = append(r.msgs, msgResp)
			return
		}
	}
	// 追加新条目，同时删除冲突条目
	for _, entry := range m.GetEntries() {
		// 发生冲突，可能删除冲突条目
		if len(r.RaftLog.entries) > 0 && entry.GetIndex() <= r.RaftLog.LastIndex() {
			targetTerm, _ := r.RaftLog.Term(entry.GetIndex())
			// 删除冲突条目并添加新条目
			if entry.GetTerm() != targetTerm {
				r.RaftLog.DeleteAndAppendEntry(pb.Entry{
					EntryType: entry.GetEntryType(),
					Term:      entry.GetTerm(),
					Index:     entry.GetIndex(),
					Data:      entry.GetData(),
				})
			}
		} else {
			r.RaftLog.entries = append(r.RaftLog.entries, pb.Entry{
				EntryType: entry.GetEntryType(),
				Term:      entry.GetTerm(),
				Index:     entry.GetIndex(),
				Data:      entry.GetData(),
			})
		}
	}
	// 接受者的commit = 发送者的commit与发送者传来的最后一个entry的index的较小值
	if m.GetCommit() > r.RaftLog.committed {
		r.RaftLog.committed = min(m.GetCommit(), m.GetIndex()+uint64(len(m.GetEntries())))
	}
	// 回复接受
	msgResp.Index = r.RaftLog.LastIndex()
	msgResp.Reject = false
	r.msgs = append(r.msgs, msgResp)
}

func (r *Raft) handleAppendResponse(m pb.Message) {
	// reject，则重置 next 为 next-1 与 m.Index + 1的较小值，然后重新进行日志 / 快照追加
	if m.Reject {
		r.Prs[m.GetFrom()].Next = min(r.Prs[m.GetFrom()].Next-1, m.GetIndex()+1)
		_ = r.sendAppend(m.GetFrom())
	}
	// 接受，则更新 match 为 m.Index，next 为 m.Index + 1
	if !m.Reject {
		r.Prs[m.GetFrom()].Match = m.GetIndex()
		r.Prs[m.GetFrom()].Next = m.GetIndex() + 1
	}
	// 更新leader的commit，将超过一半节点的 match所大于的最大值作为commit，同时其与现在的term相等
	r.checkRaftCommit()
}

func (r *Raft) handleRequestVote(m pb.Message) {
	msgResp := pb.Message{
		MsgType: pb.MessageType_MsgRequestVoteResponse,
		To:      m.GetFrom(),
		From:    r.id,
		Term:    r.Term,
		Reject:  true,
	}
	// 发送节点的term小于自己则拒绝
	if m.Term < r.Term {
		r.msgs = append(r.msgs, msgResp)
		return
	}
	// 如果自己投票过了则拒绝
	if r.Vote != 0 && r.Vote != m.GetFrom() {
		r.msgs = append(r.msgs, msgResp)
		return
	}
	// Candidate 的日志至少和自己一样新，那么就给其投票，否者拒绝。新旧判断逻辑如下：
	// 如果两份日志最后的条目的任期号不同，那么任期号大的日志更加新
	// 如果两份日志最后的条目任期号相同，那么日志比较长的那个就更加新
	rLastTerm, _ := r.RaftLog.Term(r.RaftLog.LastIndex())
	if (m.GetLogTerm() > rLastTerm) || (m.GetLogTerm() == rLastTerm && m.GetIndex() >= r.RaftLog.LastIndex()) {
		msgResp.Reject = false
		r.Vote = m.GetFrom()
	}
	r.msgs = append(r.msgs, msgResp)
}

func (r *Raft) handleRequestVoteResponse(m pb.Message) {
	// 同意的票数过半，成为 Leader，否则成为 Follower
	r.votes[m.GetFrom()] = !m.GetReject()
	agreeN := 0
	disagreeN := 0
	halfN := len(r.Prs) / 2
	for _, vote := range r.votes {
		if vote {
			agreeN++
		} else {
			disagreeN++
		}
		if agreeN > halfN {
			r.becomeLeader()
			return
		}
		if disagreeN > halfN {
			r.becomeFollower(r.Term, 0)
			return
		}
	}
}

// handleSnapshot handle Snapshot RPC request
func (r *Raft) handleSnapshot(m pb.Message) {
	// Your Code Here (2C).
	msg := pb.Message{
		MsgType: pb.MessageType_MsgAppendResponse,
		From:    r.id,
		To:      m.GetFrom(),
		Term:    r.Term,
		Reject:  false,
		Index:   r.RaftLog.committed,
	}
	meta := m.Snapshot.Metadata
	// 快照最新的日志的 index <= 目标节点最新 committed 日志的 index
	if meta.Index <= r.RaftLog.committed {
		r.msgs = append(r.msgs, msg)
		return
	}
	r.becomeFollower(max(r.Term, m.Term), m.From)
	if len(r.RaftLog.entries) > 0 {
		r.RaftLog.entries = nil
	}
	r.RaftLog.applied = meta.Index
	r.RaftLog.committed = meta.Index
	r.RaftLog.stabled = meta.Index
	r.Prs = make(map[uint64]*Progress)
	for _, peer := range meta.ConfState.Nodes {
		if peer == r.id {
			r.Prs[peer] = &Progress{
				Match: meta.Index,
				Next:  meta.Index + 1,
			}
		} else {
			r.Prs[peer] = &Progress{}
		}
	}
	// 赋值待定快照
	r.RaftLog.pendingSnapshot = m.Snapshot
	// 更新 MsgAppendResponse
	msg.Term = r.Term
	msg.Index = r.RaftLog.committed
	r.msgs = append(r.msgs, msg)
}

// handleHeartbeat handle Heartbeat RPC request
func (r *Raft) handleHeartbeat(m pb.Message) {
	// Your Code Here (2A).
	// 当对方任期大于等于自身时，则将对方视为leader
	if m.GetTerm() == r.Term {
		r.becomeFollower(m.GetTerm(), m.GetFrom())
	}
	msg := pb.Message{
		MsgType: pb.MessageType_MsgHeartbeatResponse,
		To:      m.GetFrom(),
		From:    r.id,
		Term:    r.Term,
		Commit:  r.RaftLog.committed,
	}
	// todo 小心并发
	r.msgs = append(r.msgs, msg)
	return
}

func (r *Raft) handleHeartbeatResponse(m pb.Message) {
	if m.Commit < r.RaftLog.committed {
		r.sendAppend(m.From)
	}
}

// addNode add a new node to raft group
func (r *Raft) addNode(id uint64) {
	// Your Code Here (3A).
}

// removeNode remove a node from raft group
func (r *Raft) removeNode(id uint64) {
	// Your Code Here (3A).
}

// 更新leader的commit，超过一半节点的match所大于的最大值作为commit，同时其与现在的term相等
// 如果commit更新，则立即广播给其他节点用以同步commit
func (r *Raft) checkRaftCommit() {
	matchList := make([]uint64, 0)
	for node, progress := range r.Prs {
		if node == r.id {
			continue
		}
		matchList = append(matchList, progress.Match)
	}
	sort.Slice(matchList, func(i, j int) bool {
		if matchList[i] < matchList[j] {
			return true
		}
		return false
	})
	maxN := matchList[(len(r.Prs)-1)/2]
	for ; maxN > r.RaftLog.committed; maxN-- {
		if term, _ := r.RaftLog.Term(maxN); term == r.Term {
			r.RaftLog.committed = maxN
			// 立即广播给其他节点用以同步commit
			for k := range r.Prs {
				if k == r.id {
					continue
				}
				r.sendAppend(k)
			}
			break
		}
	}
}
