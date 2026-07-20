# 里程碑：TrustMap Prototype 设计确认

## 日期

2026-07-20

## 当时活跃思路

`main`

## 主要结论

- 采用配置驱动的 Docker Compose 生成器。
- 脚本与配置支持最多 21 条 Geth developer chain，并允许逐链设置出块时间。
- 一条链对应一个 MapNode。
- MapNode 使用真实事件订阅、持久化、P2P、规划、证明和交易提交。
- DirectVerifier 首版为可插拔实验适配器。
- 论文是规范来源，旧代码只作参考。

## 支撑证据

- 用户逐段确认设计。
- 论文的 Overview、TrustMap 和 Security Analysis 支持该职责划分。
- 对旧合约、模拟器和历史 relayer 的只读盘点说明需要重新实现链下闭环。

## 改变了什么

项目从空仓库进入已确认架构阶段，形成设计提交 `1bfa5c7`。

## 下一阶段方向

书面规格复核、详细实施计划、公共协议先行和并行实现。
