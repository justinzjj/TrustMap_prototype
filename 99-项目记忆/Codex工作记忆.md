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

## 活跃假设

- 首版不实现真实通用轻客户端，这与论文把 direct verification 视为外部原语的口径一致。
- 三链端到端测试足以验证递归路径闭环；21 链首版只做配置生成和静态校验。
- 默认 Merkle depth 8 用于复现实验，部署配置允许更深的树。

## 未解决问题

- 无设计级阻塞。
- 具体 Go/Foundry 依赖版本、protobuf/schema 和任务拆分在实施计划中锁定。

## 给新对话的接续提示

先请用户审核书面规格。用户确认后，使用 writing-plans 生成详细实施计划；再按用户授权采用 subagent-driven development，并先固定公共协议/schema，再并行实现不重叠模块。
