# 记忆仓库 Manifest

## 记忆仓库身份

- 名称：TrustMap Prototype Project Memory
- 初始化日期：2026-07-20
- 用途：记录论文到原型的设计约束、实现路线、证据和交接状态

## 宿主项目

- 项目路径：`TrustMap_prototype`
- 代码仓库：`git@github.com:justinzjj/TrustMap_prototype.git`
- 默认分支：`main`
- 只读实现参考：相邻 `TrustMap-ETH` 仓库
- 只读规范参考：TrustMap LaTeX 论文仓库

## 管理模式

- 当前模式：embedded
- 是否独立 git 仓库：否
- 迁移计划：项目复杂度需要时再评估，不在首版范围内

## 认知主线

- 当前活跃思路：按论文实现真实 MapNode 和可配置多 Geth 链原型
- 已暂停思路：无
- 已合流思路：Docker 化、一链一 MapNode、可插拔 DirectVerifier 已合入主线

## 引用边界

- 代码引用：仅记录旧仓库路径、分支和提交，不复制或修改旧代码
- 实验引用：首版只复用设计思想和可公开的测试向量
- 论文/材料引用：以论文正式章节为规范，不在记忆目录复制正文

## 给未来 agent 的读取顺序

1. `memory/index.md`
2. `memory/branches/main.md`
3. `Codex工作记忆.md`
4. `项目地图.md`
5. `logs/next.md`
6. `docs/superpowers/specs/2026-07-20-trustmap-prototype-design.md`
