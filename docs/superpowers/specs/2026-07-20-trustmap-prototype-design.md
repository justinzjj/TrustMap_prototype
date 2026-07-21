# TrustMap Prototype Repository Design

## 1. 文档状态

- 状态：已确认设计
- 日期：2026-07-20
- 最近更新：2026-07-21（固定实验 DirectVerifier 的可校准成本 profile）
- 目标仓库：`TrustMap_prototype`
- 规范来源：TrustMap 论文中的 Overview、TrustMap、Security Analysis 与 Implementation and Evaluation
- 实现参考：`TrustMap-ETH`

本文档定义 TrustMap 开源原型仓库的首版范围、架构和验收标准。论文是规范来源；`TrustMap-ETH` 中的合约、实验脚本、模拟器和历史 relayer 仅作为实现参考。若参考代码与论文的算法、安全边界或术语不一致，新仓库必须按论文实现，并在可追踪文档中记录差异。

## 2. 项目目标

首版交付一个可实际运行的 TrustMap 原型：

1. 在 Docker 中启动多条彼此独立的 Geth EVM 开发链。
2. 每条链运行一个独立 MapNode。
3. MapNode 真实订阅链上区块和合约事件，维护持久化的本地 TrustView。
4. MapNode 通过真实 P2P 网络传播链上可验证的增量证据。
5. MapNode 根据论文成本模型选择 PathPlan 或 DirectPlan。
6. MapNode 构造 PathProof、在本地预验证并提交到 home chain。
7. 链上合约完成最终验证、更新 TrustRoot 并发出可重放事件。
8. 节点在重启、WebSocket 断开、P2P 分区和短暂链重组后能够恢复。

编排配置和生成脚本必须支持最多 21 条链。日常开发、自动化测试和首版验收只要求实际运行 2 至 3 条链；`topology-21.yaml` 用于证明配置与生成能力，不代表完成了 21 链运行压测。

## 3. 非目标

首版不承担以下工作：

- 实现完整的 PoW SPV、Ethereum PoS sync committee 或通用跨链轻客户端。
- 将 MapNode 的 P2P 消息作为链上接受依据。
- 构造面向主网资产的生产级 bridge。
- 提供 Kubernetes 编排。
- 实际运行和压测 21 条链。
- 修改 `TrustMap-ETH` 或论文仓库。
- 保持旧实验合约 ABI、`proofFlags` 编码或旧 relayer 内部包结构兼容。

DirectPlan 通过稳定的 `IDirectVerifier` 接口隔离。首版提供实验型 verifier 以完成本地端到端闭环，并明确标注其安全假设；真实轻客户端可以在不改变 MapNode 核心和 TrustRoot 语义的前提下追加。

## 4. 设计原则

### 4.1 论文优先

以下论文设计是不可削弱的约束：

- TrustRoot 只提交链上已验证依赖。
- TrustRoot 叶子是 `(tr, bh)`，叶子哈希为 `Hash(tr || bh)`。
- TrustRoot 使用链内锚点承接上一高度的递归承诺。
- TrustRoot 采用 lazy update；无新验证时不产生额外状态写入。
- PathProof 逐跳完成 Merkle membership verification，并最终闭合到目标链当前 TrustRoot。
- TrustView 是本地、可能过时且可能不完整的优化索引。
- TrustView 中可用的依赖必须由链上可观察证据支持。
- PathPlan 只有在可验证且成本不高于 DirectPlan 时才被选择。
- PathPlan 不可用、证明缺失、证明过期或成本不优时安全回退 DirectPlan。
- 链上合约做最终接受决定；MapNode 不进入安全信任根。

### 4.2 一链一 MapNode

每个 MapNode 实例归属一条 home chain：

- 只持有 home chain 的交易签名能力。
- 持续订阅 home chain 的新区块和 TrustMap 合约事件。
- 为其他链保存只读 RPC 注册信息，并在证据校验或证明构造时按需访问。
- 拥有独立的数据库、P2P 身份、请求状态和日志。

同一主机可以运行多个 MapNode，但逻辑身份和持久化状态始终按链隔离。

### 4.3 证据先于图

P2P 更新只提供发现线索。远端更新必须经过链上证据校验，才能成为 active TrustView edge。节点永远不能因为消息带有合法 libp2p 签名就直接信任其中的依赖关系。

### 4.4 可重放和可恢复

链上事件必须包含足够信息，使一个没有本地快照的新 MapNode 可以从部署高度开始重放并重建：

- TrustRoot 演进；
- Merkle 叶子顺序；
- 跨链验证边；
- 请求结果；
- 本地 frontier 估计所需的证据。

## 5. 总体架构

```text
topology.yaml
      │
      ▼
Topology Generator
      │
      ├── Geth chain-01 ... chain-N
      ├── contract deploy-01 ... deploy-N
      └── MapNode mapnode-01 ... mapnode-N
                         │
                   trustmap-net

每个 MapNode：

home-chain events
      │
      ▼
Indexer ──> Evidence Validator ──> TrustView Store
                                           │
VerificationRequested ─> Coordinator ──> Planner
                                           │
                             ┌─────────────┴────────────┐
                             ▼                          ▼
                       PathProof Builder          DirectVerifier
                             │                          │
                             └────────> Executor <──────┘
                                           │
                                           ▼
                                      home chain

P2P Gossip <──> Evidence Inbox/Outbox
HTTP/CLI   <──> Status, inspection and operations
```

## 6. Docker 与 Geth 编排

### 6.1 编排方式

仓库维护声明式 topology 配置，由 `trustmapctl topology render` 生成运行时文件：

- Docker Compose 文件；
- 每条链的 genesis/runtime 配置；
- MapNode 配置；
- P2P bootstrap peer 配置；
- 合约部署清单路径；
- 宿主机调试端口映射。

生成内容写入 `runtime/`，不进入 Git。生成器必须确定性输出：相同输入应生成等价配置。

### 6.2 Geth 运行模式

每条链使用锁定具体版本的官方 Geth 容器，并采用 developer mode：

- `--dev.period` 控制固定出块周期；
- 每条链使用独立 Chain ID 和数据卷；
- 每条链提供容器内 HTTP 与 WebSocket RPC；
- 自定义 genesis 预充值部署账户和 MapNode 账户；
- Geth P2P 不用于连接不同 TrustMap 链；这些链是独立账本。

采用 developer mode 而不是 Clique。Geth 官方已说明新版客户端不能继续封装 Clique/Ethash 私链区块，而 developer mode 仍支持 `--dev.period`、持久化 datadir 和自定义 genesis：

- [Geth Developer Mode](https://geth.ethereum.org/docs/developers/dapp-developer/dev-mode)
- [Geth Command-line Options](https://geth.ethereum.org/docs/fundamentals/command-line-options)
- [Geth Private Networks](https://geth.ethereum.org/docs/fundamentals/private-network)

### 6.3 Topology 配置

配置支持简写默认值和逐链覆盖。概念结构如下：

```yaml
version: 1
network_name: trustmap-local
defaults:
  geth_image: ethereum/client-go:v1.17.3
  block_period_seconds: 3
  confirmations: 2
  merkle_depth: 8
  direct_verifier:
    profile_id: pow-spv-3m
    authorized_signer_count: 3
    signature_checks: 3
    hash_rounds: 4497
chains:
  - name: chain-a
    chain_id: 10001
  - name: chain-b
    chain_id: 10002
    block_period_seconds: 5
mapnodes:
  p2p_enabled: true
  database: sqlite
```

设计基线锁定官方稳定版 `ethereum/client-go:v1.17.3`。实现阶段若因为可复现测试发现该版本存在阻断性问题，只能升级到更新的具体稳定版本，并在规格变更记录和集成测试证据中说明；不能使用 `latest` 或浮动的 `stable` 标签。配置校验至少拒绝：

- 链数小于 2 或大于 21；
- 重复 Chain ID 或链名；
- 非正数出块周期；
- 冲突的宿主机调试端口；
- 不支持的配置版本；
- 缺少 home-chain MapNode 映射。

### 6.4 容器生命周期

启动顺序由健康检查和完成标记控制，不使用固定 sleep：

1. 生成 runtime 配置和仅限本地使用的密钥。
2. 启动 Geth 容器。
3. 等待 `eth_chainId`、最新区块和 WS RPC 健康。
4. 运行一次性部署容器。
5. 原子写入每条链的 deployment manifest。
6. MapNode 校验 manifest、链 ID 和部署代码哈希后启动。

私钥、keystore、数据库、deployment runtime 状态和链数据全部位于 Git 忽略目录。仓库不得复制旧历史分支中曾出现的 keystore。

## 7. 链上合约设计

### 7.1 合约边界

```text
VerificationGateway
 ├── IDirectVerifier
 ├── PathProofVerifier
 └── TrustRootCommitment
```

首版可以将三个职责部署为一个组合合约和一个外部 DirectVerifier 合约，但源码必须保持边界清晰。Gateway 负责请求去重、调用验证、原子记录依赖和发出请求结果事件。

### 7.2 TrustRoot 叶子

叶子严格遵守论文定义：

```solidity
leaf = keccak256(abi.encodePacked(sourceTrustRoot, sourceBlockHash));
```

Chain ID 和高度是验证上下文及事件元数据，不加入叶子哈希。实现不能为了方便数据库索引而改变该承诺格式。

### 7.3 Lazy update 与链内锚点

合约保存：

- 当前 `trustRoot`；
- 当前更新树对应的 EVM block number；
- Merkle frontier；
- 当前树的下一个 leaf index；
- 可配置的 Merkle depth；
- 已记录 dependency key；
- 历史更新 block 对应的 root 元数据。

当某个 EVM 区块首次成功记录新外部依赖时：

1. 读取更新前的 `trustRoot`，它等价于论文中的 `TR_L(h-1)`。
2. 读取 `blockhash(block.number - 1)`。
3. 重置本区块的 Merkle frontier。
4. 将 `(previousTrustRoot, previousBlockHash)` 作为 leaf 0 插入。
5. 插入新验证的外部依赖。

同一 EVM 区块中的后续成功验证只追加外部依赖，不重复 leaf 0。没有成功验证的区块不写 TrustRoot 状态。

部署参数 `merkleDepth` 默认是 8，以对应论文实验实现；允许在创建不同实验拓扑时选择更深的树。当前 EVM 区块的更新树满时交易显式 revert，MapNode 等待下一个 EVM 区块建立新的锚定树后重试；若同一 topology 持续触发树满，则通过容量指标提示提高部署深度。

### 7.4 PathProof ABI

```solidity
struct MembershipWitness {
    uint32 leafIndex;
    bytes32[] siblings;
}

function verifyPath(
    bytes32 baseTrustRoot,
    bytes32[] calldata blockHashes,
    MembershipWitness[] calldata witnesses
) external view returns (bool);
```

约束：

- `blockHashes.length == witnesses.length`；
- 路径至少包含一跳；
- 每个 witness 的 sibling 数等于部署配置的 Merkle depth；
- leaf index 必须在树容量范围内；
- 每跳使用 `Hash(rootPrev || blockHashes[i])` 作为 leaf；
- 每跳重建出的 root 成为下一跳的 `rootPrev`；
- 最终 root 必须等于调用时的当前 TrustRoot。

MapNode 提交前执行完全相同的本地算法，但本地结果只用于减少失败交易，不能替代链上判断。

### 7.5 验证与记录原子性

Gateway 暴露两个执行入口：

- `verifyDirectAndRecord(...)`
- `verifyPathAndRecord(...)`

每个入口在单笔交易内完成：

1. 校验 request ID 尚未解决。
2. 执行对应 verifier。
3. 得到被接受源区块的 `(sourceTrustRoot, sourceBlockHash)`。
4. 幂等地记录依赖。
5. 更新 TrustRoot。
6. 标记请求完成。
7. 发出验证、依赖和请求结果事件。

同一 dependency key 重复出现时不重复插入 Merkle leaf，但请求仍可依据已有依赖完成。PathPlan revert 后不会在同一交易内隐式执行 DirectPlan；MapNode 根据错误重新规划并显式提交 DirectPlan。

### 7.6 DirectVerifier 接口

```solidity
interface IDirectVerifier {
    function verify(
        uint256 sourceChainId,
        uint256 sourceHeight,
        bytes32 sourceBlockHash,
        bytes calldata proof
    ) external returns (bytes32 sourceTrustRoot);
}
```

首版实验实现命名为 `ExperimentalCostedDirectVerifier`。它采用“真实签名闭环 + 可校准开销模拟”，部署时固定：

- `authorizedSigners[]`；
- `signatureChecks`；
- `hashRounds`；
- topology 中稳定的 `profileId`。

DirectProof 编码 `sourceTrustRoot + signatures[]`。合约先对完整验证上下文执行 `hashRounds` 轮链式 Keccak，再验证部署 profile 指定数量的真实签名。`signatureChecks` 与 `hashRounds` 不从 proof 读取，提交者不能降低开销。首版 topology 为每个被检查签名生成独立的本地实验 signer，且要求 `signatureChecks <= authorizedSigners.length`。

该 verifier 可以复用旧代码的签名开销模拟思想，但必须：

- 明确命名为开发/实验 verifier；
- 保留 source chain、height、block hash、source TrustRoot、home chain、verifier 和 Gateway 的完整签名上下文绑定；
- 只接受部署时授权 signer 的有效真实签名，不使用空签名或无效签名模拟 Gas；
- 通过 Gas 校准测试把 profile 映射到论文使用的 SPV、委员会验证等成本区间；
- 不能直接写 TrustRoot；
- 不能提供任意设置当前 TrustRoot 的生产接口；
- 通过相同 Gateway 流程发出完整事件；
- 在 README 和安全说明中写明它不是通用轻客户端。

#### 默认 PoW-SPV 成本校准 profile

首个开源默认 profile 命名为 `pow-spv-3m`，只表示实验成本目标，不表示实现了 PoW SPV 或继承其安全性。它以论文表中的 PoW SPV header verification 代表值为基准：

- 目标成本：`3,000,000 gas`；
- 自动化验收区间：`2,700,000–3,300,000 gas`（目标值 ±10%）；
- 固定校准参数：`authorized_signer_count = 3`、`signatureChecks = 3`、`hashRounds = 4497`；
- 固定实测记录：cold-state 完整 `verifyDirectAndRecord` 为 `3,000,096 gas`，以 `measured_direct_cost_gas`/`measuredDirectCostGas` 随同同一 profile 发布；
- 校准入口：一次成功的 `TrustMapGateway.verifyDirectAndRecord`，包含实验 DirectVerifier 验证和同一 DirectPlan 的链上依赖记录，不包含前置 `requestVerification` 交易；
- 校准状态：使用固定 depth、固定请求上下文和确定性的新 dependency 状态，避免重复 dependency 或 tree 状态改变测量边界；
- 调参优先级：保持默认 `authorized_signer_count = signatureChecks = 3`，主要调整部署时固定的 `hashRounds`，避免靠扩大 calldata 或伪造大量 signer 把成本堆高；
- 参数发现：校准测试可以先搜索满足区间的 `hashRounds`，最终选定值必须写回 topology 默认 profile，常规回归测试只验证固定 profile，不在每次测试中动态改变部署参数；
- 成本记录：实测结果写入与 profile 参数绑定的 calibration fixture/deployment manifest，MapNode Planner 后续只读取同一 profile 的实测值。

Gas 校准测试必须在仓库锁定的 Solidity、optimizer、EVM 和 Foundry 配置下运行。若工具链升级使固定 profile 超出区间，测试应失败并要求重新校准，而不是放宽区间。测试和文档必须继续明确：循环 Keccak 与真实 ECDSA 恢复只是成本模拟，不验证 PoW 难度、header chain、Merkle inclusion 或 finality。

### 7.7 权限与事件

PathProof 提交 permissionless。任何提交者只要证明正确且请求有效即可完成交易。实验 DirectVerifier 可以限制其调用者为 Gateway。

事件至少包括：

- `VerificationRequested`
- `DirectVerificationSucceeded`
- `PathVerificationSucceeded`
- `DependencyRecorded`
- `TrustRootUpdated`
- `RequestResolved`

事件字段必须足以重建 request ID、source chain、source height、source block hash、source TrustRoot、leaf index、旧根、新根、交易和 log 顺序。

## 8. MapNode 设计

### 8.1 语言与依赖边界

MapNode 使用 Go，并通过 go-ethereum 访问 Geth RPC、交易类型、日志和合约绑定。核心包不能依赖 Docker；Docker 只是运行适配层。

### 8.2 Chain Registry

Chain Registry 保存：

- Chain ID 与稳定名称；
- HTTP/WS RPC 地址；
- TrustMap Gateway 和 DirectVerifier 地址；
- deployment block；
- confirmation depth；
- direct verification cost；
- 是否为 home chain；
- home chain 交易签名配置。

只有 home chain 条目可以包含签名器。MapNode 拒绝在非 home chain 上发送交易。

### 8.3 Indexer

Indexer：

- 通过 WS 订阅 home chain 新区块和日志；
- 使用 HTTP RPC 补扫断连区间；
- 按 `(blockNumber, transactionIndex, logIndex)` 确定性排序；
- 保存最后安全 cursor；
- 校验 Chain ID、合约地址和部署代码哈希；
- 将确认前事件标记 provisional；
- 达到 confirmation depth 后发布 confirmed evidence。

### 8.4 Reorg Manager

Reorg Manager 保存已处理高度的规范区块哈希。若同一高度哈希发生变化：

1. 找到共同祖先。
2. 在单个数据库事务中撤销受影响的 provisional blocks、events、roots、edges 和 plans。
3. 将相关未提交 proof 标记 stale。
4. 从共同祖先后的高度重新抓取日志。

已经达到配置确认深度的数据正常情况下不回滚；若 RPC 报告深层重组，节点进入 degraded 状态并停止自动提交，等待显式运维处理，而不是继续基于矛盾证据规划。

### 8.5 Evidence Validator

Evidence 状态：

```text
candidate -> verified -> confirmed -> active
         \-> invalid
```

本链 Indexer 产生的 confirmed event 可以直接进入 active。P2P 收到的 candidate 必须通过远端只读 RPC 校验：

- Chain ID 匹配；
- 区块哈希属于规范链；
- 交易 receipt 成功；
- 合约地址匹配 registry；
- topic 和 ABI 解码正确；
- log block hash 与 receipt 一致；
- 事件达到目标链配置的确认深度；
- dependency ID 和事件内容一致。

### 8.6 TrustView

节点键：

```text
(chainId, height, blockHash)
```

节点元数据包含 TrustRoot、证据状态和首次观察时间。边分为：

- intra-chain edge：新块指向旧块；
- inter-chain verification edge：目标链记录块指向已验证的源链块。

只有 active evidence 能生成 active edge。frontier 是本地估计，不写链上，也不要求节点间一致。

### 8.7 Planner

Planner 在数据库一致性快照上运行：

1. 确定 home chain 当前可接受起点和目标源区块。
2. 只遍历 active edges。
3. 运行带 cost cutoff 的 Dijkstra。
4. 计算 `pathCost = hopCount * pathStepCost`，或使用配置中的等价逐边成本。
5. 读取目标链部署清单中同一 DirectVerifier profile 的实测 `directCost`；未校准或 profile 不匹配时不得猜测成本。
6. 当且仅当路径存在、证明材料可获得且 `pathCost <= directCost` 时输出 PathPlan。
7. 其他情况输出 DirectPlan，并记录 fallback reason。

首版不加入论文 Discussion 中的链风险权重、allowlist 或 minimum assurance 扩展；这些属于后续兼容扩展，不能改变首版论文成本模型。

`directCost` 必须来自与链上 `profileId`、`signatureChecks`、`hashRounds` 和授权 signer 数量一致的 Gas 校准结果。MapNode 不接受请求提交者提供的成本值，也不把实验 profile 的签名数量解释为真实轻客户端安全等级。

### 8.8 Proof Builder

Proof Builder 保存或按需重建每个 TrustRoot 更新树的：

- anchor leaf；
- external dependency leaves；
- leaf index；
- sibling path；
- resulting root；
- 对应链上 evidence locator。

证明构造流程：

1. 冻结 Planner 使用的 TrustView snapshot ID。
2. 为路径每一跳查找对应 membership witness。
3. 缺失时通过链日志回放重建；P2P proof request 只作为加速来源。
4. 使用 Go 实现本地递归验证。
5. 读取 home chain 最新 TrustRoot。
6. 若最新根变化导致 proof 不再闭合，则标记 stale 并重新规划。

### 8.9 Request Coordinator

请求通过两种入口进入同一状态机：

- 主入口：home-chain `VerificationRequested` 事件；
- 辅助入口：本地 HTTP/CLI 发起演示请求，最终仍需产生链上请求或使用同一 Gateway request ID。

状态机：

```text
Observed
  -> EvidenceReady
  -> Planned
  -> ProofReady
  -> Submitted
  -> Confirmed

Retryable -> Replanned -> ProofReady
Retryable -> DirectFallback -> Submitted
InvalidInput -> Rejected
```

状态转换和副作用使用数据库事务及唯一 request ID 保证幂等。

### 8.10 Executor

Executor：

- 串行管理 home chain 账户 nonce；
- 在发送前执行 gas estimate；
- 保存 raw transaction、tx hash、nonce 和 fee 参数；
- 等待 receipt 和配置确认深度；
- 支持同 nonce replacement；
- 区分 RPC 暂时错误、nonce 错误、out-of-gas、contract revert 和 stale proof；
- stale proof 触发重新规划；
- 确定性无效输入进入 Rejected；
- 不把交易广播成功误认为验证成功。

## 9. P2P 协议

### 9.1 传输

使用 go-libp2p：

- GossipSub 用于 confirmed evidence 通知；
- request/response stream 用于缺失事件和 proof material；
- Compose 生成 bootstrap peer；
- 节点使用持久化 libp2p identity；
- 应用协议带独立版本号。

### 9.2 Gossip envelope

每条消息至少包含：

```text
protocolVersion
messageId
originPeerId
observedAt
evidenceType
chainId
contractAddress
blockNumber
blockHash
transactionHash
transactionIndex
logIndex
payloadDigest
```

`messageId` 和 evidence ID 根据规范化字段确定性计算。peer 签名只用于来源追踪和限流，不能跳过 RPC 证据校验。

### 9.3 Inbox 与 Outbox

- confirmed 本链 evidence 与 outbox 写入同一数据库事务；
- P2P worker 至少一次发送；
- 接收端依靠 message ID 幂等；
- invalid evidence 记录原因和 peer，但不修改 active TrustView；
- peer 连续发送无效消息时进行本地限流，不形成全网共识黑名单。

### 9.4 网络分区语义

网络分区允许 TrustView 不一致。分区只可能造成：

- 漏掉可复用路径；
- 选择较长路径；
- proof material 获取变慢；
- 回退 DirectPlan。

它不能改变 Gateway 的接受条件。

## 10. 持久化设计

每个 MapNode 使用独立 SQLite 数据库并启用 WAL。这样不需要额外数据库容器，且便于开源用户检查状态。

逻辑表包括：

- `chains`
- `canonical_blocks`
- `chain_cursors`
- `contract_events`
- `trust_roots`
- `merkle_leaves`
- `dependency_edges`
- `evidence`
- `frontiers`
- `requests`
- `plans`
- `proofs`
- `transactions`
- `p2p_inbox`
- `p2p_outbox`

数据库 migration 具有单调版本号。MapNode 启动时在事务中升级；不支持的未来 schema version 必须拒绝启动。

## 11. HTTP 与 CLI

HTTP 默认仅监听容器内部管理地址，提供：

- `/health/live`
- `/health/ready`
- `/v1/status`
- `/v1/chains`
- `/v1/trustview/nodes`
- `/v1/trustview/edges`
- `/v1/requests/{id}`
- `/v1/requests/{id}/retry`
- `/v1/requests`

写操作只用于触发、重试或取消本地工作，不允许直接插入 active edge、修改 TrustRoot 或伪造 confirmed evidence。

`trustmapctl` 提供 topology render/validate、up/down、status、logs、request 和 inspect 子命令。脚本可以包装这些命令，但业务逻辑不能只存在于 shell 脚本中。

## 12. 故障处理

| 故障 | 行为 |
|---|---|
| WS 断开 | 重连后从 cursor 通过 HTTP 补扫 |
| RPC 短暂不可用 | 指数退避，节点进入 degraded，不丢请求 |
| provisional reorg | 数据库事务回滚并重放 |
| 深层 reorg | 停止自动提交，暴露运维告警 |
| P2P 分区 | 保持本地运行，必要时 DirectPlan fallback |
| 恶意 gossip | candidate 校验失败，不进入 active graph |
| proof 缺失 | 链上日志重建或请求 peer，最终 fallback |
| TrustRoot 改变 | proof stale，重新规划 |
| nonce 冲突 | 重新同步 pending nonce 并按 request 恢复 |
| contract revert | 解码错误；stale proof 可重试，确定性输入错误拒绝 |
| 数据库重启 | WAL 恢复，状态机从最后提交状态继续 |
| DirectVerifier 不可用 | 保持 Retryable，不伪造成功 |

## 13. 测试策略

### 13.1 Solidity

Foundry 单元、fuzz 和 invariant 测试覆盖：

- 第一条依赖创建 anchor leaf；
- 同区块追加依赖不重复 anchor；
- 无验证区块不改变 TrustRoot；
- 跨更新区块递归锚定；
- 单跳和多跳 PathProof；
- 错误 base root、block hash、index、siblings 和长度；
- proof 最终未闭合到当前 root；
- 重复 dependency 幂等；
- request 不能被重复解决；
- DirectVerifier 失败不更新 TrustRoot；
- Costed DirectVerifier 的 profile 在部署后不可降低；
- 每个被检查签名都真实有效，并绑定完整请求、链和 Gateway 上下文；
- 错误 signer、签名数量、hash rounds/profile、跨链或跨 Gateway replay 均失败；
- 默认 `pow-spv-3m` profile 的完整 `verifyDirectAndRecord` 实测成本位于 2.7M–3.3M gas；
- 校准结果与 profile 的 signer 数、signature checks、hash rounds 和工具链版本绑定；
- PathVerifier 失败不更新 TrustRoot；
- 无任意管理员 TrustRoot setter；
- tree capacity 与部署 depth 一致。

### 13.2 Go

单元测试覆盖：

- Go/Solidity Merkle golden vectors；
- Indexer 排序、cursor 和补扫；
- provisional reorg 回滚；
- evidence 状态转换；
- P2P candidate 无法直接产生 active edge；
- TrustView 幂等合并；
- shortest path 和 cost cutoff；
- PathPlan/DirectPlan/fallback reason；
- proof 构造和本地递归验证；
- request state machine；
- nonce manager；
- outbox 至少一次投递与 inbox 去重；
- schema migrations。

### 13.3 Docker 集成测试

两链场景：

1. 启动 A、B 及对应 MapNode。
2. 在 B 发起验证 A 区块的请求。
3. Planner 选择 DirectPlan。
4. B Gateway 验证并记录 `B -> A`。
5. 两个 MapNode 最终观察到相同链上 evidence。

三链场景：

1. 先通过 DirectPlan 建立 `B -> A`。
2. 再通过 DirectPlan 建立 `C -> B`。
3. 在 C 请求验证 A 的已覆盖区块。
4. C Planner 找到 `C -> B -> A`。
5. C 构造 PathProof，本地验证通过并提交。
6. C Gateway 链上验证通过、记录新依赖并解决请求。

恢复场景：

- MapNode 在事件确认前重启；
- WS 断开后产生新区块；
- P2P 临时分区；
- 构造 proof 后 TrustRoot 被其他请求更新；
- 收到带合法 peer 签名但链上不存在的 evidence；
- 删除本地派生 proof cache 后通过事件重放恢复。

### 13.4 21 链静态验收

测试生成 `topology-21.yaml` 的完整 runtime 配置，并检查：

- 恰好 21 个 Geth 和 21 个 MapNode 服务；
- Chain ID、服务名和 volume 唯一；
- 所有 home-chain 映射一对一；
- 所有 block period 正确传播；
- bootstrap 配置可解析；
- Compose 配置通过静态校验。

此项不启动 42 个长期运行容器。

## 14. 仓库布局

```text
TrustMap_prototype/
├── cmd/
│   ├── mapnode/
│   └── trustmapctl/
├── internal/
│   ├── api/
│   ├── chain/
│   ├── config/
│   ├── evidence/
│   ├── executor/
│   ├── p2p/
│   ├── planner/
│   ├── proof/
│   └── trustview/
├── contracts/
│   ├── script/
│   ├── src/
│   └── test/
├── db/migrations/
├── api/
├── configs/
│   ├── topology.yaml
│   └── topology-21.yaml
├── docker/
├── scripts/
├── tests/
│   ├── fixtures/
│   └── integration/
├── docs/
│   ├── architecture/
│   ├── operations/
│   ├── protocol/
│   ├── superpowers/specs/
│   └── paper-traceability.md
├── 99-项目记忆/
└── runtime/
```

现有空目录不会决定最终包结构。Go 入口采用 `cmd/`，内部实现采用职责清晰的 `internal/` 包。

## 15. 论文可追踪性

`docs/paper-traceability.md` 在实现阶段维护下表，并要求每项至少对应一个代码模块和一个自动化测试：

| 论文设计 | 实现责任 | 验收证据 |
|---|---|---|
| TrustRoot leaf `(tr,bh)` | TrustRootCommitment | Solidity golden vector |
| In-chain anchor | TrustRootCommitment | block-boundary test |
| Lazy update | Gateway/TrustRoot | no-verification state test |
| Recursive verification | PathProofVerifier/Go proof | cross-language vector |
| Local TrustView | TrustView Store | divergent-view integration test |
| Chain-observable evidence | Evidence Validator | malicious gossip test |
| Cost-aware planning | Planner | cutoff and fallback tests |
| DirectPlan fallback | Coordinator | missing/stale path tests |
| Off-chain not in trust base | Gateway + evidence boundary | invalid path rejection test |

旧实现差异也记录在该文档中，包括旧手工 block hash、任意 root setter、重复事件和 flattened `proofFlags`，但不把这些实验接口延续为规范。

## 16. 可观测性与开源交付

日志使用结构化格式，至少带：

- `chain_id`
- `mapnode_id`
- `request_id`
- `evidence_id`
- `block_number`
- `tx_hash`
- `plan_type`
- `fallback_reason`

指标至少覆盖：

- Indexer lag；
- active/candidate edge 数；
- PathPlan/DirectPlan 选择数；
- proof 构造耗时；
- stale proof 数；
- P2P 收发与 invalid evidence 数；
- pending/retryable 请求数；
- transaction confirmation latency。

开源 README 必须明确：

- 默认 DirectVerifier 的实验性质；
- 该原型不是资产 bridge；
- 21 链是配置能力，不是默认运行规模；
- 最小两链和推荐三链演示命令；
- 如何设置逐链出块时间；
- 如何清理 runtime 数据；
- 如何替换 DirectVerifier。

## 17. Git 与实施纪律

- 所有实现只发生在 `TrustMap_prototype`。
- `TrustMap-ETH` 和论文仓库保持只读。
- 从已确认规格生成分任务实施计划。
- 功能按可独立测试的模块拆分，并使用小而有意义的提交。
- 并行任务必须拥有不重叠的主要文件边界；公共协议和 schema 先行确定。
- 每个阶段合并前运行对应单元或集成测试。
- 不提交 runtime、链数据、私钥、数据库或生成的密钥材料。

## 18. 首版完成标准

首版只有在以下条件全部满足时才视为完成：

1. 默认三链 topology 能由单一命令生成和启动。
2. 每条链的出块时间可独立配置并在运行时生效。
3. 配置生成器可生成并静态校验 21 链 topology。
4. 一链一 MapNode，且只在 home chain 签名提交。
5. 两链 DirectPlan 集成场景通过。
6. 三链递归 PathPlan 集成场景通过。
7. P2P 未验证消息不能进入 active TrustView。
8. MapNode 重启和 WS 补扫测试通过。
9. stale PathProof 能重新规划或回退 DirectPlan。
10. Go 与 Solidity 的 Merkle/PathProof vectors 一致。
11. 所有论文可追踪项都有代码和自动化测试证据。
12. README、安全边界、配置说明和演示步骤完整。
13. Git 不包含私钥、keystore、链数据或 runtime 数据。
