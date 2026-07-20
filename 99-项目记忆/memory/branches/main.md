# 思路：main

## 状态

active

## 一句话

严格按论文实现一个可复现、可恢复、可扩展到 21 链配置的 TrustMap 开源原型。

## 为什么这是主线

用户的核心诉求是将旧版链下模拟器替换为真实可用的 MapNode，同时保留论文的链上安全边界和成本感知路径复用机制。

## 核心假设

- DirectVerifier 是外部原语，首版实验适配器不妨碍 MapNode 闭环真实性。
- 一链一 MapNode 最符合论文的 local TrustView 与最终一致传播模型。
- Docker Compose 生成器足以支持最多 21 链的可配置研究环境。
- P2P 消息只有经过链上证据校验后才能影响 active TrustView。

## 已观察证据

- 论文明确区分链上接受与链下发现。
- 旧合约已有增量 Merkle 原型。
- 旧 relayer 的真实链交互和证明闭环未完成。
- Geth developer mode 支持可配置 `--dev.period`。

## 关联实现/代码状态

- 代码分支：`main`
- commit：`1bfa5c7`
- 实验/脚本：实现尚未开始

## 关键材料

- `docs/superpowers/specs/2026-07-20-trustmap-prototype-design.md`
- `项目基础盘点.md`

## 当前判断

设计范围已经足够明确，可以在用户复核书面规格后进入详细实施计划。

## 下一步

用户审核设计规格；通过后生成 TDD 实施计划并使用并行 subagent 执行。

## 变更记录

### 2026-07-20

- 初始化论文优先的原型主线。
- 确认 Docker/Geth、一链一 MapNode、P2P 证据边界和 DirectVerifier 适配器。

