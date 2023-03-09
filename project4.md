# Project 4 Transactions

本节实现MVCC及对应的事务API

## Percolator

### Primary Key

Primary Key 用于标记当前事务的状态。

对于一次事务中的多次操作(包含key,value和op(operation))，都需要上锁防止写-写/读-写冲突。因为他们遵循原子性原则，所以需要一个统一key对他们进行上锁。  
选取一个 Primary Key 来作为主锁，只要该锁不存在，那么该事务中所有操作都可以视为无锁，反之都视为有锁。  
Primary Key 的选取通常为从所有操作的 key 中随机选取一个。  
因此上锁时，key 为每个操作自己的key，对应的 Lock 的 Primary 则就是Primary Key。

各类操作时 Put 和 Delete 的数据结构:

![transactions](https://cdn.jsdelivr.net/gh/starmilkxin/picturebed/img/Transactions.png)

### KvGet

1. 根据 key 获取 Lock。
2. 如果 Lock 的 Ts <= 当前的 startTs，说明存在之前存在尚未 commit 的请求，返回错误。
3. 通过 GetValue(the most recent value committed before the start of this transaction) 获取 value。

### KvPrewrite

2PC 阶段中的第一阶段

1. 对于请求中的每个操作通过 MostRecentWrite 方法根据操作的 key 获取其最近的 Write，如果 Write 存在且其写入的 ts >= 当前请求的 StartVersion，那么就说明发生了写-写冲突，返回冲突错误。
2. 根据操作的 key 获取对应的 Lock，如果存在 Lock ，则说明该 key 被其他事务使用中，发生了，返回锁冲突。
3. 根据此次操作的 Op 决定是 PutValue/DeleteValue (modify 统一添加到 txn.Writes()中)。
4. 对此次操作的 key 上锁。循环遍历剩余操作。
5. 将 txn.Writes() 持久化到数据库中。

### KvCommit

2PC 阶段中的第二阶段

1. 对于请求中的每个 key，获取其锁，无锁则直接返回。
2. 锁存在，则判断 lock.Ts != req.StartVersion，防止不是同一把锁，如果为 true，则返回重试错误。
3. 调用 PutWrite 方法，写入的 Write 包含了(startTs 和 commitTs)，startTs 的作用是帮助你查找对应的 Data，因为 PutValue 的 key 为 key+txn.StartTS。
4. 解除该 key 的锁。循环遍历剩余 key。
5. 将 txn.Writes() 持久化到数据库中。

### KvCheckTxnStatus

1. 通过 CurrentWrite() 获取 primary key 的 Write。
2. 如果 Write 不为 null，且不是 WriteKindRollback，则说明已经被 commit 了，直接返回 commitTs。
3. 通过 GetLock() 获取 primary key 的 Lock。如果没有 Lock，说明 primary key 已经被回滚了，创建一个 WriteKindRollback 并直接返回。
4. 检查 Lock 的 TTL，判断 Lock 是否超时，如果超时，移除该 Lock 和 Value，并创建一个 WriteKindRollback 标记回滚。否则直接返回。

### KvBatchRollback

1. 通过 CurrentWrite 获取 Write，如果已经是 WriteKindRollback，说明这个 key 已经被回滚完毕，跳过这个 key。
2. 否则先获取 Lock，如果获取 Lock 的 startTs 不是当前事务的 startTs，则终止操作，说明该 key 被其他事务拥有。
3. 否则移除 Lock，删除 Value 并增加 WriteKindRollback。

### KvResolveLock

这个方法主要用于解决锁冲突，当客户端已经通过 KvCheckTxnStatus() 检查了 primary key 的状态，这里打算要么全部回滚，要么全部提交，具体取决于 ResolveLockRequest 的 CommitVersion。

1. 通过 iter 获取到含有 Lock 的所有 key；
2. 如果 req.CommitVersion == 0，则调用 KvBatchRollback() 将这些 key 全部回滚；
3. 如果 req.CommitVersion > 0，则调用 KvCommit() 将这些 key 全部提交；