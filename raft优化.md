# Raft优化

## Raft读操作问题 

Raft的写操作是一定要写主，那么读操作呢？

1. 写主读从  
当读操作交给 follower 来执行的话，以下情况就可能会发生错误:
   + 仅需超过半数的 follower 接受日志后，leader 就会对其 commit，但读操作的 follower 正好属于小部分。
   + follower 属于大部分接受了该日志，leader 也 commit 了该日志，但是 leader 的心跳包尚未通知到该 follower。
2. 写主读主
如果leader直接进行读操作，那么以下情况就可能会发生错误:
    + leader 仅仅 commit 了写操作日志，还未 apply 就响应了客户端的写操作，此时客户端发起读操作，就会出错。(状态机落后于 committed log 导致脏读)
    + 发生了脑裂，存在两个 leader，写操作的为多数节点的分区的 leader，但读操作的为少数节点分区的 leader。(网络分区导致脏读)

### Raft Log Read

Raft 通过 Raft Log Read 来使得 Raft 的读操作是线性化的，将 读操作 也走一边 Raft 流程，使得其被 apply 时才响应客户端返回读操作的结果。  
这么做的目的是:

1. 当读操作被 commit后 ，那么先前的写操作一定已经 commit 并 apply 完成，所以此时读操作 apply 时进行读取，不会发生脏读。 
2. 读操作日志走一遍 Raft 流程，就会对其余节点发送 appendEntries 请求，这样可以根据反馈得知自己是不是多数节点的分区的 leader，防止脑裂导致脏读。

## Raft读操作优化

Raft Log Read 使得读操作性能不高，可以采用 Read Index 和 Lease Read 对其进行优化。

### Read Index

Read Index 通过 发送心跳包 与 等待状态机应用到指定index 来对读操作进行优化。

1. Leader 在收到客户端读请求时，记录下当前的 commit index，称之为 read index。
2. Leader 向 followers 发起一次心跳包而不是 appendEntries 请求，这一步是为了确保领导权，避免网络分区时少数派 leader 仍处理请求。
3. 等待状态机至少应用到 read index（即 apply index 大于等于 read index）。
4. 执行读请求，将状态机中的结果返回给客户端。

以此解决 状态机落后于 committed log 导致脏读 与 网络分区导致脏读 这两问题。

### Lease Read

Lease Read 相对比 Read Index，通过时间租期代替了 Read Index 的心跳包。

- leader 发送 heartbeat 的时候，会首先记录一个时间点 start，当系统大部分节点都回复了 heartbeat response，那么我们就可以认为 leader 的 lease 有效期可以到 start + election timeout这个时间点。  
- 在此时间点之前的读操作，仅需遵守 apply index 大于等于 read index 即可，无需发送心跳包并等待回复。
- 超过此时间点之后，则使用 Read Index 方案。心跳包可以为 leader 继续续约。 

这么做的原理是因为 follower 会在至少 election timeout 的时间之后，才会重新发生选举，所以下一个 leader 选出来的时间一定可以保证大于 start + election timeout。  
虽然采用 lease 的做法很高效，但仍然会面临风险问题，也就是我们有了一个预设的前提，各个服务器的 CPU clock 的时间是准的，即使有误差，也会在一个非常小的 bound 范围里面，如果各个服务器之间 clock 走的频率不一样，有些太快，有些太慢，这套 lease 机制就可能出问题。