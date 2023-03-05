# Project3 MultiRaftKV

本节实现一个基于多 raft 的 kv server 和 balance scheduler，它由多个 raft 组组成，每个 raft 组负责一个键范围，这里命名为 region。

![MultiRaftKV](https://cdn.jsdelivr.net/gh/starmilkxin/picturebed/img/multiraft.png)

共三部分:

1. 对 Raft 算法实施成员变更和领导层变更
2. 在 raftstore 上实现 conf change 和 region split
3. 引入调度器

## Project3A

Project3A主要实现:

1. raft层的LeaderTransfer
2. Add/Remove Node的函数实现。

### LeaderTransfer

1. 收到 AdminCmdType_TransferLeader 请求，调用 d.RaftGroup.TransferLeader()。
2. 通过 Step() 函数输入 Raft。
3. 先判断自己是不是 Leader，因为只有 Leader 才有权利转移，否则直接忽略。
4. 判断自己的 leadTransferee 是否为空，如果不为空，则说明已经有 Leader Transfer 在执行，忽略本次。
5. 如果目标节点拥有和自己一样新的日志，则发送 pb.MessageType_MsgTimeoutNow 到目标节点。否则启动 append 流程同步日志。当同步完成后再发送 pb.MessageType_MsgTimeoutNow。
6. 当 Leader 的 leadTransferee 不为空时，不接受任何 propose，因为正在转移。
7. 如果在一个 electionTimeout 时间内都没有转移成功，则放弃本次转移，重置 leadTransferee。因为目标节点可能已经挂了。
8. 目标节点收到 pb.MessageType_MsgTimeoutNow 时，应该立刻重置自己的定时器并自增 term 开始选举。

### Add/Remove Node

需要注意的是，在 removeNode 之后，Leader 要重新计算 committedIndex:

```go
// addNode add a new node to raft group
func (r *Raft) addNode(id uint64) {
	// Your Code Here (3A).
	if _, ok := r.Prs[id]; !ok {
		r.Prs[id] = &Progress{Next: 1}
	}
	r.PendingConfIndex = None
}

// removeNode remove a node from raft group
func (r *Raft) removeNode(id uint64) {
	// Your Code Here (3A).
	if _, ok := r.Prs[id]; ok {
		delete(r.Prs, id)
		if r.State == StateLeader {
			r.checkRaftCommit()
		}
	}
	r.PendingConfIndex = None
}
```

## Project3B

### Add/Remove

和其他 cmd 的 propose 不一样，该指令是通过 d.RaftGroup.ProposeConfChange() 提交的:

应用的流程如下：

1. 调用 d.RaftGroup.ApplyConfChange() 方法，修改 raft 内部的 peers 信息，其会调用 3A 中实现的 Add 和 Remove；
2. 修改 region.Peers，是删除就删除，是增加就增加一个 peer。如果删除的目标节点正好是自己本身，那么直接调用 d.destroyPeer() 方法销毁自己，并直接返回；
3. 令 region.RegionEpoch.ConfVer ++ ；
4. 持久化修改后的 Region，写到 kvDB 里面。使用 meta.WriteRegionState() 方法。其中 state 要使用 rspb.PeerState_Normal，因为其要正常服务请求的；
5. 调用 d.insertPeerCache() 或 d.removePeerCache() 方法，更新缓存；
6. 更新 scheduler 那里的 region 缓存

### Split

![SPlit](https://cdn.jsdelivr.net/gh/starmilkxin/picturebed/img/split.png)

Split触发

1. peer_msg_handler.go 中的 onTick() 定时检查，调用 onSplitRegionCheckTick() 方法，它会生成一个 SplitCheckTask 任务发送到 split_checker.go 中。
2. splitCheckHandler调用 Handle(t worker.Task) 方法处理任务，如果region的大小 > maxSize，则生成一个 MsgTypeSplitRegion 请求。

Split propose

1. 在 peer_msg_handler.go 中的 HandleMsg() 方法中调用 onPrepareSplitRegion()，发送 SchedulerAskSplitTask 请求到 scheduler_task.go 中，申请其分配新的 region id 和 peer id。申请成功后其会发起一个 AdminCmdType_Split 的 AdminRequest 到 region 中。
2. 继续在 peer_msg_handler.go 中的 HandleMsg() 方法中接收普通 AdminRequest 一样，propose 等待 apply，注意 propose 的时候检查 splitKey 是否在目标 region 中和 regionEpoch 是否为最新，因为目标 region 可能已经产生了分裂。

Split apply

1. 基于原来的 region clone 一个新 region，这里把原来的 region 叫 oldRegion，新 region 叫 newRegion。复制方法如下：
    1. 把 oldRegion 中的 peers 复制一份；
    2. 将复制出来的 peers 逐一按照 req.Split.NewPeerIds[i] 修改 peerId，作为 newRegion 中的 peers，记作 cpPeers；
    3. 创建一个新的 region，即 newRegion。其中，Id 为 req.Split.NewRegionId，StartKey 为 req.Split.SplitKey，EndKey 为 d.Region().EndKey，RegionEpoch 为初始值，Peers 为 cpPeers；
2. oldRegion 的 EndKey 改为 req.Split.NewRegionId；
3. oldRegion 和 newRegion 的 RegionEpoch.Version 均自增；
4. 持久化 oldRegion 和 newRegion 的信息；
5. 更新 storeMeta 里面的 regionRanges 与 regions；
6. 通过 createPeer() 方法创建新的 peer 并注册进 router，同时发送 message.MsgTypeStart 启动 peer；
7. 调用两次 d.notifyHeartbeatScheduler()，更新 scheduler 那里的 region 缓存；

## Project3C

这一节实现上层的调度器，与Split自发分裂不同，Scheduler是属于第三方进行监控与自动调度的。  
这里涉及到了PD Scheduler，Placement Driver (后续以 PD 简称) 是 TiDB 里面全局中心总控节点，它负责整个集群的调度，负责全局 ID 的生成，以及全局时间戳 TSO 的生成等。PD 还保存着整个集群 TiKV 的元信息，负责给 client 提供路由功能。详细可看[https://cn.pingcap.com/blog/placement-driver](https://cn.pingcap.com/blog/placement-driver)  
因为PD拥有整个Raft集群的元信息，所以包括前面split时需要新增的region ID，新的 peer ID，都需要 PD 生成，并将其返回给 leader。

### 收集区域心跳

processRegionHeartbeat

1. pd server在 RegionHeartbeat 方法中接受region的心跳，并通过HandleRegionHeartbeat方法处理region心跳。具体方法在processRegionHeartbeat方法中。
2. 收到汇报来的心跳，先检查一下 RegionEpoch 是否是最新的，如果是新的则调用 c.putRegion() 和 c.updateStoreStatusLocked() 进行更新。

### 实施区域平衡调度器

![balance1](https://cdn.jsdelivr.net/gh/starmilkxin/picturebed/img/balance1.png)

![balance2](https://cdn.jsdelivr.net/gh/starmilkxin/picturebed/img/balance2.png)

Schedule

这一部分由pd主要负责 region 的调度，从 region size 最大的 store 中取出一个 region 放到 region size 最小的 store 中，让集群中的 stores 所负载的 region 趋于平衡，避免一个 store 中含有很多 region 而另一个 store 中含有很少 region 的情况。

1. 选出 suitableStores，并按照 regionSize 进行排序。SuitableStore 是那些满足 DownTime() 时间小于 MaxStoreDownTime 的 store。DownTime()为该store上次向自己发送心跳的时间与当前时刻的时间差。
2. 开始遍历 suitableStores，从 regionSize 最大的开始遍历，依次调用 GetPendingRegionsWithLock()，GetFollowersWithLock() 和 GetLeadersWithLock()。直到找到一个目标 region。如果实在找不到目标 region，直接放弃本次操作。
3. 判断目标 region 的 store 数量，如果小于 cluster.GetMaxReplicas()，直接放弃本次操作。
4. 再次从 suitableStores 开始遍历，这次从 regionSize 最小的开始遍历，选出一个目标 store，目标 store 不能在原来的 region 里面。如果目标 store 找不到，直接放弃。
5. 判断两个 store 的 regionSize 是否小于 2*ApproximateSize 。是的话直接放弃，因为如果此时接着转移，很有可能过不了久就重新转了回来。
6. 调用 cluster.AllocPeer() 创建 peer，创建 CreateMovePeerOperator 操作，返回结果。