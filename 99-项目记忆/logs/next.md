# 下一步

## 当前优先级

1. 与用户核对阶段 1 的 Gateway ABI、事件、leaf、lazy anchor 和 recursive proof 口径。
2. 核对通过后进入阶段 2：topology schema、renderer 和 Docker skeleton。
3. 生成默认三链配置与 21 链静态配置，逐链支持 Chain ID 和出块时间覆盖。
4. 验证一条链只对应一个 Geth 和一个 MapNode 服务，运行时密钥不入 Git。

## 最近第一步

展示阶段 1 验证结果和公开合约边界，等待用户确认是否进入阶段 2。

## 阻塞

- 无技术阻塞；当前位于阶段检查点。

## 暂不做

- 不启动 21 链运行压测。
- 不提前实现阶段 3 的 SQLite、Planner 或 P2P。
- 不修改 `TrustMap-ETH` 和论文仓库。
