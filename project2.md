# Project2 RaftKV

本节需要实现一个基于raft的高可用kv服务器。

总共分为三部分：

1. 实现基本的 Raft 算法 
2. 在 Raft 之上构建一个容错的 KV 服务器 
3. 添加raftlog GC和snapshot的支持

## Project2A

Project2A主要是实现：

1. 领导人选举
2. 日志复制
3. 实现raw node接口

RawNode、Raft、RaftLog关系图：

![rawnode-raft-raftlog](https://cdn.jsdelivr.net/gh/starmilkxin/picturebed/img/rawnode-raft-raftlog.jpg)

### 实现Raft算法

RaftLog中entries的结构图：

![raftlogentry](https://cdn.jsdelivr.net/gh/starmilkxin/picturebed/img/RaftLog.png)

>RaftLog中的entries为非压缩部分，包含了已持久化与未持久化的部分。

Raft的tick方法，会根据raft节点的状态决定调用哪个角色tick:

```go
func (r *Raft) followerTick() {
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

func (r *Raft) candidateTick() {
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

func (r *Raft) leaderTick() {
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
```
> follower和candidate都是增加选举的过期时间，leader是增加心跳的国企时间。

Step方法是驱动器，根据当前raft节点的角色与msg的类型决定不同的handle方法。

Raft流程图：

![raftprocess](https://cdn.jsdelivr.net/gh/starmilkxin/picturebed/img/raft流程.png)

### 实现raw node接口

RawNode作为Raft的封装，其负责接受上层下达的指令，将msg传递给Raft以及从Raft中获取结果。

RawNode的Tick方法调用Raft的tick方法:

```go
// Tick advances the internal logical clock by a single tick.
func (rn *RawNode) Tick() {
	rn.Raft.tick()
}
```

RawNode的Propose方法调用Raft的propose方法:

```go
// Propose proposes data be appended to the raft log.
func (rn *RawNode) Propose(data []byte) error {
	ent := pb.Entry{Data: data}
	return rn.Raft.Step(pb.Message{
		MsgType: pb.MessageType_MsgPropose,
		From:    rn.Raft.id,
		Entries: []*pb.Entry{&ent}})
}
```

RawNode通过HasReady()方法来得知是否有新的同步完成，包括:

- 是否有需要持久化的状态；
- 是否有需要持久化的条目；
- 是否有需要应用的条目；
- 是否有需要应用的快照(pendingSnapshot)；
- 是否有需要发送的 Msg

如果HasReady()返回true，则再通过Ready()方法来获取新的Ready:

```go
// Ready returns the current point-in-time state of this RawNode.
func (rn *RawNode) Ready() Ready {
	// Your Code Here (2A).
	rd := Ready{
		SoftState:        nil,
		HardState:        pb.HardState{},
		Entries:          rn.Raft.RaftLog.unstableEntries(),
		Snapshot:         pb.Snapshot{},
		CommittedEntries: rn.Raft.RaftLog.nextEnts(),
		Messages:         rn.Raft.msgs,
	}
	softState := rn.GetSoftState()
	if !isSoftStateEqual(softState, rn.lastSoftState) {
		rd.SoftState = &softState
	}
	if !IsEmptyHardState(rn.GetHardState()) && !isHardStateEqual(rn.GetHardState(), rn.lastHardState) {
		rd.HardState = rn.GetHardState()
	}
	if !IsEmptySnap(rn.Raft.RaftLog.pendingSnapshot) {
		rd.Snapshot = *rn.Raft.RaftLog.pendingSnapshot
	}
	rn.Raft.msgs = []pb.Message{} // 清空消息
	rn.Raft.RaftLog.pendingSnapshot = nil
	return rd
}
```

上层会根据RawNode提供的Ready进行持久化与应用，之后通过Advance()推进整个状态机，因为Ready中的内容都已经被持久化和应用，所以需要将Ready中的内容更新到RaftLog中:

```go
// Advance notifies the RawNode that the application has applied and saved progress in the
// last Ready results.
func (rn *RawNode) Advance(rd Ready) {
	// Your Code Here (2A).
	if !IsEmptyHardState(rd.HardState) {
		rn.lastHardState = rd.HardState
	}
	if rd.SoftState != nil {
		rn.lastSoftState = *rd.SoftState
	}
	if len(rd.Entries) > 0 {
		rn.Raft.RaftLog.stabled = rd.Entries[len(rd.Entries)-1].GetIndex()
	}
	if len(rd.CommittedEntries) > 0 {
		rn.Raft.RaftLog.applied = rd.CommittedEntries[len(rd.CommittedEntries)-1].GetIndex()
	}
	rn.Raft.RaftLog.maybeCompact()
}
```

> maybeCompact()方法用于根据存储中的日志index情况更新RaftLog中日志的长度

```go
// We need to compact the log entries in some point of time like
// storage compact stabled log entries prevent the log entries
// grow unlimitedly in memory
func (l *RaftLog) maybeCompact() {
	// Your Code Here (2C).
	if len(l.entries) == 0 {
		return
	}
	// 未压缩的第一个日志的index
	storageFirstIdx, _ := l.storage.FirstIndex()
	// 先前的第一个日志的index
	raftLogFirstIdx := l.entries[0].GetIndex()
	// 未压缩的第一个日志的index > 先前的第一个日志的index，则说明先前的日志发生了压缩，需要对日志进行gc
	if storageFirstIdx > raftLogFirstIdx {
		entries := l.entries[storageFirstIdx-raftLogFirstIdx:]
		l.entries = make([]pb.Entry, len(entries))
		copy(l.entries, entries)
	}
}
```

## Project2B

project2b 实现了 rawNode 之上的上层应用，即真正开始多线程集群操作，引入了 peer 和 region 的概念。同时，除了实现上层的调用，project2b 还需要通过调用 project1 中的接口真正实现写操作。

store、region、peer 三者的关系如下:

![store-region-peer](https://cdn.jsdelivr.net/gh/starmilkxin/picturebed/img/store-region-peer.png)

- Store：每一个节点叫做一个 store，也就是一个节点上面只有一个 Store。代码里面叫 RaftStore，后面统一使用 RaftStore 代称。
- Peer：一个 RaftStore 里面会包含多个 peer，一个 RaftStore 里面的所有 peer 公用同一个底层存储，也就是多个 peer 公用同一个 badger 实例。
- Region：一个 Region 叫做一个 Raft group，即同属一个 raft 集群，一个 region 包含多个 peer，这些 peer 散落在不同的 RaftStore 上。

首先看一看peer的数据结构:

```go
type peer struct {
	// The ticker of the peer, used to trigger
	// * raft tick
	// * raft log gc
	// * region heartbeat
	// * split check
	ticker *ticker
	// Instance of the Raft module
	RaftGroup *raft.RawNode
	// The peer storage for the Raft module
	peerStorage *PeerStorage

	// Record the meta information of the peer
	Meta     *metapb.Peer
	regionId uint64
	// Tag which is useful for printing log
	Tag string

	// Record the callback of the proposals
	// (Used in 2B)
	proposals []*proposal

	// Index of last scheduled compacted raft log.
	// (Used in 2C)
	LastCompactedIdx uint64

	// Cache the peers information from other stores
	// when sending raft messages to other peers, it's used to get the store id of target peer
	// (Used in 3B conf change)
	peerCache map[uint64]*metapb.Peer
	// Record the instants of peers being added into the configuration.
	// Remove them after they are not pending any more.
	// (Used in 3B conf change)
	PeersStartPendingTime map[uint64]time.Time
	// Mark the peer as stopped, set when peer is destroyed
	// (Used in 3B conf change)
	stopped bool

	// An inaccurate difference in region size since last reset.
	// split checker is triggered when it exceeds the threshold, it makes split checker not scan the data very often
	// (Used in 3B split)
	SizeDiffHint uint64
	// Approximate size of the region.
	// It's updated everytime the split checker scan the data
	// (Used in 3B split)
	ApproximateSize *uint64
}
```

> 可以看到其包含了RawNode和PeerStorage，RawNode为Raft的封装，PeerStorage用于持久化数据的保存。

Raftstore的 startWorkers(peers []*peer) 方法中会开一个协程来进行raftWorker的 run(closeCh <-chan struct{}, wg *sync.WaitGroup) 方法:

```go
// run runs raft commands.
// On each loop, raft commands are batched by channel buffer.
// After commands are handled, we collect apply messages by peers, make a applyBatch, send it to apply channel.
func (rw *raftWorker) run(closeCh <-chan struct{}, wg *sync.WaitGroup) {
	defer wg.Done()
	var msgs []message.Msg
	for {
		msgs = msgs[:0]
		select {
		case <-closeCh:
			return
		case msg := <-rw.raftCh:
			msgs = append(msgs, msg)
		}
		pending := len(rw.raftCh)
		for i := 0; i < pending; i++ {
			msgs = append(msgs, <-rw.raftCh)
		}
		peerStateMap := make(map[uint64]*peerState)
		for _, msg := range msgs {
			peerState := rw.getPeerState(peerStateMap, msg.RegionID)
			if peerState == nil {
				continue
			}
			newPeerMsgHandler(peerState.peer, rw.ctx).HandleMsg(msg)
		}
		for _, peerState := range peerStateMap {
			newPeerMsgHandler(peerState.peer, rw.ctx).HandleRaftReady()
		}
	}
}
```

> 根据msg中的RegionID，找到当前store中对应的peer，然后通过peerMsgHandler的HandleMsg方法来处理msg。
> 之后再遍历每个peer，通过peerMsgHandler的HandleRaftReady方法来处理每个peer的新的Ready。

HandleMsg(msg message.Msg) 负责分类处理各种 msg。

- MsgTypeRaftMessage，来自外部接收的 msg，在接收 msg 前会进行一系列的检查，最后通过 RawNode 的 Step() 方法直接输入，不需要回复 proposal。
- MsgTypeRaftCmd，通常都是从 client 或自身发起的请求，比如 Admin 管理命令，read/write 等请求，需要回复 proposal。
- MsgTypeTick，驱动 RawNode 的 tick 用。
- MsgTypeSplitRegion，触发 region split，在 project 3B split 中会用到。
- MsgTypeRegionApproximateSize，修改自己的 ApproximateSize 属性，不用管。
- MsgTypeGcSnap，清理已经安装完的 snapshot。
- MsgTypeStart，启动 peer，新建的 peer 的时候需要发送这个请求启动 peer，在 Project3B split 中会遇到。

主要是proposeRaftCommand(msg *raft_cmdpb.RaftCmdRequest, cb *message.Callback)方法:

> 对于每个MsgRaftCmd，它除了有request，还有callback用于回调表示msg成功propose。

```go
func (d *peerMsgHandler) proposeRaftCommand(msg *raft_cmdpb.RaftCmdRequest, cb *message.Callback) {
	err := d.preProposeRaftCommand(msg)
	if err != nil {
		cb.Done(ErrResp(err))
		return
	}
	// Your Code Here (2B).
	if msg.AdminRequest != nil {
		d.proposeAdminRequest(msg, cb)
	} else {
		d.proposeRequest(msg, cb)
	}
}

func (d *peerMsgHandler) proposeRequest(msg *raft_cmdpb.RaftCmdRequest, cb *message.Callback) {
	key := getRequestKey(msg.Requests[0])
	if key != nil {
		err := util.CheckKeyInRegion(key, d.Region())
		if err != nil {
			cb.Done(ErrResp(err))
			return
		}
	}
	data, err := msg.Marshal()
	if err != nil {
		panic(err)
	}
	p := &proposal{index: d.nextProposalIndex(), term: d.Term(), cb: cb}
	d.proposals = append(d.proposals, p)
	d.RaftGroup.Propose(data)
}
```

> 对于每条msg首先检查其region是否合法，如果出现err则直接通过回调通知错误。
> 之后将其index，term与callback封装成proposal添加到peerMsgHandler的proposals列表中。
> 最后调用RawNode的Propose(data []byte) error来将消息propose到raft集群中。

接下来是HandleRaftReady()方法:

```go
func (d *peerMsgHandler) HandleRaftReady() {
	if d.stopped {
		return
	}
	// Your Code Here (2B).
	if !d.RaftGroup.HasReady() {
		return
	}
	ready := d.RaftGroup.Ready()
	// 将 Ready 中需要持久化的状态与快照保存到 badger
	applySnapshotResult, _ := d.peerStorage.SaveReadyState(&ready)
	if applySnapshotResult != nil {
		if !reflect.DeepEqual(applySnapshotResult.PrevRegion, applySnapshotResult.Region) {
			d.peerStorage.region = applySnapshotResult.Region
			d.ctx.storeMeta.Lock() // 要加锁，不然会冲突
			d.ctx.storeMeta.regions[applySnapshotResult.Region.GetId()] = applySnapshotResult.Region
			d.ctx.storeMeta.regionRanges.Delete(&regionItem{
				region: applySnapshotResult.PrevRegion,
			})
			d.ctx.storeMeta.regionRanges.ReplaceOrInsert(&regionItem{
				region: applySnapshotResult.Region,
			})
			d.ctx.storeMeta.Unlock()
		}
	}
	d.Send(d.ctx.trans, ready.Messages)
	// apply
	if len(ready.CommittedEntries) > 0 {
		kvWB := new(engine_util.WriteBatch)
		for _, v := range ready.CommittedEntries {
			kvWB = d.process(kvWB, &v)
			if d.stopped {
				return
			}
		}
		d.peerStorage.applyState.AppliedIndex = ready.CommittedEntries[len(ready.CommittedEntries)-1].Index
		kvWB.SetMeta(meta.ApplyStateKey(d.regionId), d.peerStorage.applyState)
		kvWB.WriteToDB(d.peerStorage.Engines.Kv)
	}
	d.RaftGroup.Advance(ready)
}
```

- 首先判断RaftGroup是否有Ready。
- 然后通过SaveReadyState方法将 Ready 中需要持久化的状态与快照保存到 badger
- 如果有快照应用且当前快照的region与先前的不一致时，则需要修改storeMeta中peer与region的信息，因为可能会有多个peer同时修改，所以需要上锁。
- 接下来将ready.Messages发送出去。
- 如果ready中有已commit但为apply的entries的话，则通过process方法对其进行apply，具体细节在processRequest方法中，首先根据请求的get/put/delete/snap类型apply to kv，之后再根据不同类型通过callback进行回调执行的结果，最后再删除proposals中的第一个proposal。
- 最后通过Advance方法与ready来推进状态机。

## Project2C

project2c 在 project2b 的基础上完成集群的快照功能。分为五个部分：快照生成，快照分发，快照接收，快照应用，日志压缩。

快照生成：

- 当 Leader 需要发送 Snapshot 时，调用 r.RaftLog.storage.Snapshot() 生成 Snapshot。因为 Snapshot 很大，生成 Snapshot 请求是异步的，一开始快照的状态是SnapState_Relax，会通过发送 RegionTaskGen 到 region_task.go 中处理，会异步的生成 Snapshot，此时将会返回raft.ErrSnapshotTemporarilyUnavailable错误，Leader 就应该放弃本次 Snapshot，等待下一次再次请求 Snapshot。
- 下一次 Leader 请求 Snapshot() 时，如果此时快照是的状态为SnapState_Generating，但snapState.Receiver管道中并没有快照，则说明还在生成中，依旧会返回raft.ErrSnapshotTemporarilyUnavailable错误，Leader 就应该放弃本次 Snapshot，等待下一次再次请求 Snapshot。
- 当 Leader 请求 Snapshot() 时，如果此时快照是的状态为SnapState_Generating，且snapState.Receiver管道中有快照，则已经创建完成，直接拿出来，包装为 pb.MessageType_MsgSnapshot 发送至目标节点。

```go
func (ps *PeerStorage) Snapshot() (eraftpb.Snapshot, error) {
	var snapshot eraftpb.Snapshot
	if ps.snapState.StateType == snap.SnapState_Generating {
		select {
		case s := <-ps.snapState.Receiver:
			if s != nil {
				snapshot = *s
			}
		default:
			return snapshot, raft.ErrSnapshotTemporarilyUnavailable
		}
		ps.snapState.StateType = snap.SnapState_Relax
		if snapshot.GetMetadata() != nil {
			ps.snapTriedCnt = 0
			if ps.validateSnap(&snapshot) {
				return snapshot, nil
			}
		} else {
			log.Warnf("%s failed to try generating snapshot, times: %d", ps.Tag, ps.snapTriedCnt)
		}
	}

	if ps.snapTriedCnt >= 5 {
		err := errors.Errorf("failed to get snapshot after %d times", ps.snapTriedCnt)
		ps.snapTriedCnt = 0
		return snapshot, err
	}

	log.Infof("%s requesting snapshot", ps.Tag)
	ps.snapTriedCnt++
	ch := make(chan *eraftpb.Snapshot, 1)
	ps.snapState = snap.SnapState{
		StateType: snap.SnapState_Generating,
		Receiver:  ch,
	}
	// schedule snapshot generate task
	ps.regionSched <- &runner.RegionTaskGen{
		RegionId: ps.region.GetId(),
		Notifier: ch,
	}
	return snapshot, raft.ErrSnapshotTemporarilyUnavailable
}
```

快照发送：

- 每个节点有个字段叫 pendingSnapshot，可以理解为待应用的 Snapshot，如果 leader 发快照时pendingSnapshot 有值，那就直接发这个，否者通过Snapshot()生成一个新的快照发过去。
- 发送 Snapshot 并不是和普通发送消息一样。在 peer_msg_handler.go 中通过 Send() 发送，这个函数最后会调用 transport.go 中的 Send()，你根据方法链调用，看到在 WriteData() 函数中，Snapshot 是通过 SendSnapshotSock() 方法发送。在sendSnap(addr string, msg *raft_serverpb.RaftMessage)方法中，它就会将 Snapshot 切成小块，发送到目标 RaftStore 上面去。

快照接收：

- Follower 收到 Leader 发来的 pb.MessageType_MsgSnapshot 之后，会根据其中的 Metadata 来更新自己的 committed、applied、stabled 等等指针。然后将在 Snapshot 之前的 entry 均从 RaftLog.entries 中删除。之后，根据其中的 ConfState 更新自己的 Prs 信息。做完这些操作好，把 pendingSnapshot 置为该 Snapshot，等待 raftNode 通过 Ready( ) 交给 peer 层处理。

```go
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
```

快照应用：

- 在 HandleRaftReady( ) 中，如果收到的 Ready 包含 SnapState，就需要对其进行应用。调用链为HandleRadtReady( ) -> SaveReadyState( ) -> ApplySnaptshot( )
- 在 ApplySnaptshot( ) 中，首先要通过 ps.clearMeta 和 ps.clearExtraData 来清空旧的数据。然后更新根据 Snapshot 更新 raftState 和 applyState。其中，前者需要把自己的 LastIndex 和 LastTerm 置为 Snapshot.Metadata 中的 Index 和 Term。后者同理需要更改自己的 AppliedIndex 以及 TruncatedState。按照文档的说法，还需要给 snapState.StateType 赋值为 snap.SnapState_Applying
- 接下来就是将 Snapshot 中的 K-V 存储到底层。这一步不需要我们来完成，只需要生成一个 RegionTaskApply，传递给 ps.regionSched 管道即可。region_task.go 会接收该任务，然后异步的将 Snapshot 应用到 kvDB 中去。

快照压缩：

- 日志压缩和生成快照是两步。每当节点的 entries 满足一定条件时，就会触发日志压缩。
- 在 HandleMsg( ) 会中收到 message.MsgTypeTick，然后进入 onTick( ) ，触发 d.onRaftGCLogTick( ) 方法。这个方法会检查 appliedIdx - firstIdx >= d.ctx.cfg.RaftLogGcCountLimit，appliedIdx指最后应用的日志index，firstIdx为已压缩日志的index+1，即已经应用但未的 entry 数目是否大于等于你的配置。如果是，就开始进行压缩。
- 该方法会通过 proposeRaftCommand( ) 提交一个 AdminRequest 下去，类型为 AdminCmdType_CompactLog。然后 proposeRaftCommand( ) 就像处理其他 request 一样将该 AdminRequest 封装成 entry 交给 raft 层来同步。
- 当该 entry 需要被 apply 时，HandleRaftReady( ) 开始执行该条目中的命令 。这时会首先修改相关的状态，然后调用 d.ScheduleCompactLog( ) 发送 raftLogGCTask 任务给 raftlog_gc.go。raftlog_gc.go 收到后，会删除 raftDB 中对应 index 以及之前的所有已持久化的 entries，以实现压缩日志。