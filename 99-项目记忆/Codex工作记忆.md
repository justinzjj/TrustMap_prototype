# Codex 工作记忆

## 当前目标

在不修改旧仓库和论文的前提下，实现可开源的 TrustMap 原型；重点是把旧版模拟 MapNode 替换为真实可运行的 Go MapNode。

## 当前路线

- 一链一 MapNode。
- Geth 与 MapNode 全部容器化，并位于同一 Docker bridge network。
- topology 配置和生成器支持最多 21 链；默认实际运行 3 链。
- 每条链可独立配置出块时间，采用锁定版本的 Geth developer mode 和 `--dev.period`。
- MapNode 使用 go-ethereum、go-libp2p 和 SQLite WAL。
- DirectVerifier 可插拔；首版使用明确标注的实验 verifier。
- P2P 只传播发现线索，active edge 必须有链上可观察证据。

## 已确认事实

- 新仓库初始化时只有空 README 和用户创建的空目录。
- 旧仓库当前实验分支保留合约与模拟器，旧 relayer 主要存在于历史 `TrustMap4` 分支。
- 历史 relayer 的 shortest path、proof generation、链监听和验证层仍有关键 TODO，并依赖全局模拟高度。
- 旧合约可参考增量 Merkle 思路，但旧 `proofFlags`、手工 block hash、测试 root setter 和重复事件不能成为新规范接口。
- Geth 官方已弃用新版 Clique sealing；设计采用 developer mode。

## 关键决策

- 论文优先于旧代码。
- 请求以链上 `VerificationRequested` 为主入口，HTTP/CLI 为辅助入口。
- PathPlan 和 DirectPlan 都通过 Gateway 原子完成验证、依赖记录和请求解决。
- PathProof ABI 直接表达逐跳 membership witness。
- 用户已逐段批准整体设计。
- 设计规格提交：`1bfa5c7 docs: add TrustMap prototype design`。
- 实施采用六个大阶段，并在每个阶段结束后与用户核对，不维护逐文件微计划。
- 实现工作位于 `feat/prototype-v1` 分支，用户明确允许直接在仓库工作目录内开发。

## 已完成实现

- 阶段 1 已完成：Go module、仓库 guardrails、完整 uint256 domain 类型、Solidity/Go 共用 Merkle 向量和纯 Go proof 算法。
- 链上已实现 `TrustMapGateway`、`TrustRootCommitment`、`PathProofVerifier`、`IDirectVerifier` 和实验 attestation verifier。
- Gateway 已覆盖 lazy anchor、递归 PathProof、依赖幂等、请求原子解决、重入防护、历史 update-block root 和可重放事件。
- 实验 verifier 明确不是轻客户端；签名绑定 verifier、Gateway、home chain 和完整 source context，并检查一次性双向绑定。
- 阶段 1 质量门通过：Go race/vet、55 个 Foundry 测试、合约尺寸与 high-severity lint；规格和代码质量复审均批准。

## 活跃假设

- 首版不实现真实通用轻客户端，这与论文把 direct verification 视为外部原语的口径一致。
- 三链端到端测试足以验证递归路径闭环；21 链首版只做配置生成和静态校验。
- 默认 Merkle depth 8 用于复现实验，部署配置允许更深的树。

## 未解决问题

- 无阶段 1 阻塞。
- 阶段 2 尚未实现 topology schema、Compose renderer、Geth/MapNode Docker skeleton 和 3/21 链配置。

## 给新对话的接续提示

先读设计规格和分阶段实施计划，再从阶段 2 开始。优先固定 topology schema 与 renderer，生成默认三链和静态 21 链 Compose；保持一链一 MapNode、Geth v1.17.3 dev mode 和逐链 `--dev.period`，不要启动 21 链实跑。
