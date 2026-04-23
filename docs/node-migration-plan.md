# TrapTunnel Node 改造方案

## 1. 目标

本文档描述从当前 `sender + receiver` 实现演进到统一 `node` 架构的改造步骤、阶段目标、风险控制和上线建议。

改造目标：

1. 保留当前链路可用性
2. 逐步收敛为单一 `node` 模型
3. 支持 relay、fanout、inject 修正和 export
4. 降低对现网 `A-F`、`G/H` 的切换风险

## 2. 当前基线

当前代码基线：

- `sender` 负责本地抓包并向单个主用上游转发
- `receiver` 负责接收后注入本地网络
- 隧道帧已经具备 `node_id + seq + raw_ip_packet` 基础结构

当前环境基线：

- `A/B`、`C/D`、`E/F` 三组入口节点
- `G/H` 为中心 `inManage` 集群节点
- HMS 仍在 `A-F` 上直接监听 `162/UDP`

## 3. 改造原则

### 3.1 原则一：先统一内核，再统一程序名

先完成内部能力模型和流水线统一，再将外部运行形式收敛为 `node`。避免在功能改动和部署改动同时发生时放大风险。

### 3.2 原则二：原始帧优先

- 中继和导出优先保留原始 frame
- 注入修正只对本地副本生效

### 3.3 原则三：边缘入口 HA 与业务消费 HA 分离

- 三组 VIP 继续承担设备入口职责
- HMS 或其替代程序的主备切换建立在 export 消费层

### 3.4 原则四：逐步替换 HMS 接入方式

- 首阶段保留现有 UDP/162 接入方式
- 第二阶段新增 export，供新告警程序直接消费

## 4. 目标阶段划分

## 阶段 1：抽取统一 frame 与流水线

目标：

- 将当前 `sender/receiver` 中重复的配置、协议和日志逻辑收敛为共享模块
- 明确 `frame`、`sink`、`profile` 等内部抽象

主要工作：

- 抽取 frame 编解码模块
- 抽取共享配置和日志模块
- 抽取内部分发总线

交付结果：

- 保持现有 `sender` / `receiver` 外部行为不变
- 为后续 `node` 能力组合打基础

## 阶段 2：实现统一 node 二进制

目标：

- 新增 `node` 二进制
- 支持通过 profile 或显式开关启用能力

主要工作：

- 增加统一入口 `node/main.go`
- 支持 `capture`、`ingress`、`egress`、`inject`
- 保留 `sender` / `receiver` 作为兼容入口或薄封装

交付结果：

- `edge`、`relay`、`sink` 三类节点可运行

## 阶段 3：实现 relay 和多上游 fanout

目标：

- 支持一个 node 同时做本地采集和下游 ingress
- 支持 `[[egress.groups]]` 的“组内主备、组间扇出”配置模型

主要工作：

- 将内部发送对象从 `PacketData` 升级为 `frame`
- ingress 接收到的 frame 可直接进入 egress
- 每个 fanout group 独立连接、独立缓冲、独立重连

交付结果：

- A 类节点可作为“本地采集 + 下游汇聚 + 上游转发”的 relay

## 阶段 4：实现 inject 修正

目标：

- 在 `inject` 路径增加 SNMPv1 `agent-addr` 修正

主要工作：

- 解析 UDP payload 中的 SNMPv1 Trap-PDU
- 提取 `agent-addr`
- 对注入副本修正源 IP
- 重新计算 IPv4/UDP 校验和

交付结果：

- `G/H` 上的 node 可兼容 `inManage` 的解析限制

## 阶段 5：实现 export

目标：

- 提供对外 TCP 订阅接口

主要工作：

- 增加 export listener
- 广播原始 frame 给订阅客户端
- 每客户端独立缓冲
- 慢客户端保护主链路

交付结果：

- 新告警程序可直接通过 TCP 获取原始 Trap

## 阶段 6：部署切换

目标：

- 平滑替换现网组件

主要工作：

- `A-F` 由 `sender` 切换为 `node edge/relay`
- `G/H` 由 `receiver` 切换为 `node sink`
- 验证 `inManage` 收到修正后的 SNMPv1 Trap
- 新程序逐步从 `162/UDP` 切换到 `export`

交付结果：

- 完成从双程序模型到统一 node 模型的线上迁移

## 5. 代码层面改造清单

### 5.1 目录结构建议

建议从当前双入口结构演进为：

```text
cmd/
  node/
  sender/
  receiver/
internal/
  config/
  logging/
  frame/
  capture/
  ingress/
  egress/
  inject/
  export/
  pipeline/
```

### 5.2 关键重构点

1. `sender` 当前内部发送对象是 `PacketData`
   - 需要升级为统一 `frame`
2. `receiver` 当前入口和注入耦合较紧
   - 需要拆分为 `ingress` 和 `inject`
3. 当前多目标发送是 failover 语义
   - 需要扩展为 `fanout group + failover member`
4. 当前日志与配置散落在两个入口程序中
   - 需要提取共享模块

## 6. 配置改造方案

### 6.1 现有配置

- `sender.conf`
- `receiver.conf`

### 6.2 目标配置

统一为 `node.toml`：

```toml
[node]
id = 101
profile = "relay"

[capture]
enabled = true
listen_ports = [162]

[ingress]
enabled = true
listen = "0.0.0.0:10000"

[egress]
enabled = true

[[egress.groups]]
members = ["172.16.1.10:10000", "172.16.1.11:10000"]

[[egress.groups]]
members = ["172.16.2.10:10000"]

[inject]
enabled = false
ip = "127.0.0.1"
port = 1162
snmpv1_agent_addr_override = false

[export]
enabled = true
listen = "0.0.0.0:12000"
```

### 6.3 配置兼容策略

采用破坏性切换策略：

- `node` 只接受统一的 `node.toml`
- 不保留 `sender.conf` / `receiver.conf` 的兼容读取
- 不提供旧配置自动转换工具
- 现网切换时按角色模板手工改写为 `node.toml`

## 7. 部署方案

### 7.1 边缘侧 A-F

建议角色：

- `A-F` 初期使用 `profile = edge`
- 若未来需要 `B/C -> A -> G/H` 这类汇聚场景，则 A 使用 `profile = relay`

说明：

- 三组 VIP 继续保持不变
- 设备入口方式不需要改

最小示例：

```toml
[node]
id = 101
name = "edge-a"
profile = "edge"

[capture]
listen_ports = [162]

[egress]
reconnect_interval = 5

[[egress.groups]]
members = ["10.10.10.10:10000", "10.10.10.11:10000"]
```

### 7.2 中心侧 G-H

建议角色：

- `G/H` 使用 `profile = sink`

最小示例：

```toml
[node]
name = "sink-g"
profile = "sink"

[ingress]
listen = "0.0.0.0:10000"

[inject]
ip = "127.0.0.1"
port = 162
snmpv1_agent_addr_override = true
```

### 7.3 部署说明

- `A-F` 统一使用 `node.toml`
- `G/H` 统一使用 `node.toml`
- 若使用 systemd，推荐部署目录：
  - `/opt/traptunnel/node`
- 推荐服务名：
  - `traptunnel-node`
- 若同机需要多实例，应改成独立安装目录和独立 service 名，而不是复用同一个 `node.toml`

说明：

- 启用 `ingress + inject`
- 启用 `snmpv1_agent_addr_override`
- `inject.port` 设为 `162`

### 7.3 HMS 与新告警程序

过渡期：

- HMS 继续从 `162/UDP` 获取 Trap

目标期：

- 新告警程序改为订阅 `export`
- 将业务处理高可用建立在 export 消费端，而不是设备入口 VIP 端

## 8. 验证方案

## 8.1 功能验证

需要覆盖：

1. 本地采集
2. ingress 接收
3. relay 转发
4. fanout + failover
5. inject 注入
6. export 订阅
7. SIGHUP 热重载

## 8.2 协议验证

需要确认：

- frame 边界正确
- `node_id` 不串号
- relay 后 `seq` 不重置
- 多 group 扇出互不影响

## 8.3 业务验证

需要重点确认：

- `inManage` 是否按修正后的源 IP 正确识别设备
- 旧 HMS 是否继续可用
- 新程序是否可稳定消费 export 流

### 8.4 本地可复现测试环境

为降低对现网环境的依赖，建议建立一套基于 Linux network namespace 的单机可复现测试环境。该环境用于验证 `12.2` 及之后各阶段的关键链路。

推荐拓扑：

- `ns-device`
  - 模拟设备发送 Trap
- `ns-edge`
  - 运行 `node edge`
- `ns-sink`
  - 运行 `node sink`
  - 同时运行一个 UDP `162` 监听器，用于模拟 `inManage`

推荐链路：

`ns-device -> ns-edge(node edge) -> ns-sink(node sink) -> UDP 162 listener`

该链路可覆盖以下能力：

- `capture`
- `egress`
- `ingress`
- `inject`

推荐地址规划：

- `ns-device <-> ns-edge`
  - `ns-device = 10.10.1.2/24`
  - `ns-edge   = 10.10.1.1/24`
- `ns-edge <-> ns-sink`
  - `ns-edge   = 10.10.2.1/24`
  - `ns-sink   = 10.10.2.2/24`

测试时：

- 设备侧向 `10.10.1.1:162` 发送 Trap
- `node edge` 将 frame 发往 `10.10.2.2:10000`
- `node sink` 将副本 inject 到本地 `162/UDP`
- `ns-sink` 内部的 UDP 监听器负责收包验证

建议准备以下测试资源：

- `scripts/dev/setup-netns.sh`
  - 创建 namespace、veth、IP、路由
- `scripts/dev/cleanup-netns.sh`
  - 清理 namespace 和虚拟链路
- `scripts/dev/send-udp.sh`
  - 从 `ns-device` 发送测试 UDP 包
- `examples/node-edge.toml`
  - `profile = "edge"`
- `examples/node-relay.toml`
  - `profile = "relay"`
- `examples/node-sink.toml`
  - `profile = "sink"`
- 一个简单的 UDP 监听工具或脚本
  - 在 `ns-sink` 中监听 `162/UDP`
  - 打印源 IP、源端口和 payload 大小

推荐验证阶段：

1. 最小闭环
   - 建立 `ns-device / ns-edge / ns-sink`
   - 在 `ns-sink` 启动 UDP 162 监听器
   - 启动 `node sink`
   - 启动 `node edge`
   - 从 `ns-device` 发送一个测试 UDP 包
   - 验证 `ns-sink` 监听器收到数据

2. 稳定性验证
   - 连续发送 100 或 1000 个 UDP 包
   - 统计 sink 侧接收数量
   - 观察程序是否异常退出或阻塞

3. 配置验证
   - 调整 `ingress.listen`
   - 调整 `egress.groups`
   - 调整 `inject.ip` / `inject.port`
   - 重启后验证行为变化是否符合预期

4. 后续扩展验证
   - 在 `12.4` 后加入 SNMPv1 `agent-addr` 修正样本
   - 在 `12.3` 后加入 relay / fanout 用例
   - 在 `12.5` 后加入 export 订阅用例

本地测试环境的执行要求：

- 需要 root 或 sudo 权限
- `ip netns` 需要管理员权限
- `node` 的 `capture` / `inject` 需要原始套接字权限

建议命令形态：

- `sudo ip netns exec ns-edge ./node -c examples/node-edge.toml`
- `sudo ip netns exec ns-sink ./node -c examples/node-sink.toml`

注意：

- 第一阶段无需先验证真正的 SNMP Trap 语义
- 先用普通 UDP payload 验证最小闭环是否稳定
- 等 `inject` 修正能力落地后，再补 SNMPv1 样本测试

配套实现与使用说明见：

- [docs/local-node-test-environment.md](/home/qqy/TrapTunnel/docs/local-node-test-environment.md)

## 9. 风险与控制

### 9.1 配置复杂度上升

控制：

- 引入 `profile`
- 增加配置校验
- 为典型场景提供模板

### 9.2 本机回环风险

控制：

- 启动时校验 `inject.port` 与 `capture.listen_ports` 不冲突

### 9.3 拓扑环路风险

控制：

- 首版通过部署规范避免
- 文档中明确“只允许向上游配置 egress”

### 9.4 慢消费者拖垮主链路

控制：

- export 客户端独立缓冲
- 超时断开慢客户端

### 9.5 迁移期双体系并存

控制：

- 保持 sender/receiver 兼容入口一段时间
- 分阶段替换节点，不做一次性全网切换

## 10. 回滚方案

每个阶段都应具备独立回滚能力：

- 边缘节点可随时退回现有 `sender`
- 中心节点可随时退回现有 `receiver`
- 新告警程序异常时，不影响 `inManage` 的 `inject` 路径

关键要求：

- 在 `node` 未验证稳定前，不应下线现有部署模板
- 保留旧 systemd service 和配置文件转换能力

## 11. 推荐上线顺序

建议按以下顺序推进：

1. 完成 `node` 代码抽象与单机测试
2. 先在实验环境验证 `inject + agent-addr` 修正
3. 在 `G/H` 试点切换为 `node sink`
4. 保持 `A-F` 仍使用现有 sender，验证中心兼容逻辑
5. 再将 `A-F` 切到 `node edge`
6. 最后引入 `export`，让新程序接入

这样可以先解决 `inManage` 解析问题，再推进更大的消费层改造。

## 12. 改造 Checklist

以下 checklist 用于跟踪 `node` 改造的实际落地进度。建议在实施过程中按阶段逐项勾选。

### 12.1 基础抽象

- [x] 确定 `cmd/` 和 `internal/` 的新目录结构
- [x] 抽取统一 `frame` 数据结构
- [x] 抽取 `frame` 编解码模块
- [x] 抽取共享日志初始化模块
- [x] 抽取共享配置加载模块
- [x] 引入统一 `profile` 概念：`edge / relay / sink / full`
- [x] 建立统一 pipeline / sink 抽象

### 12.2 Node 最小闭环

- [x] 新建统一入口 `cmd/node`
- [x] 支持 `capture` 能力
- [x] 支持 `ingress` 能力
- [x] 支持基础 `egress` 能力
- [x] 支持 `inject` 能力
- [x] 支持 `profile = edge`
- [x] 支持 `profile = sink`
- [x] 跑通 `edge -> sink -> inject` 的最小链路

### 12.3 Relay 与多上游转发

- [x] 将内部发送对象从 `PacketData` 升级为统一 `frame`
- [x] ingress 接收到的 `frame` 可直接进入 egress
- [x] 支持 `profile = relay`
- [x] 支持 `[[egress.groups]]` 配置结构
- [x] 实现组内 failover
- [x] 实现组间 fanout
- [x] 每个 group 拥有独立连接和缓冲

### 12.4 Inject 修正

- [x] 在 `inject` 路径解析 UDP payload
- [x] 识别 SNMPv1 Trap-PDU
- [x] 提取 `agent-addr`
- [x] 仅在 inject 副本中覆盖源 IP
- [x] 重新计算 IPv4 校验和
- [x] 重新计算 UDP 校验和
- [x] 确认主链路 `frame` 不被污染
- [ ] 验证 `G/H -> inManage` 识别结果正确

### 12.5 Export

- [x] 增加 export listener
- [x] 定义 export 首版输出协议
- [x] 支持多个 export 客户端订阅
- [x] 每个客户端独立缓冲
- [x] 慢客户端不会拖垮主链路
- [x] 验证新程序可稳定消费 export 流

### 12.6 配置与兼容

- [x] 统一配置格式切换为 `node.toml`
- [x] 配置结构支持 `capture / ingress / egress / inject / export`
- [x] 增加配置合法性校验
- [x] 增加 `inject.port` 与 `capture.listen_ports` 冲突校验
- [ ] 现网 `sender.conf` / `receiver.conf` 已完成手工迁移到 `node.toml`

### 12.7 部署与运行

- [x] 保留旧 `sender` / `receiver` 作为兼容入口
- [ ] 引入 `SIGHUP` 配置热重载框架
- [ ] 热重载支持修改 egress group
- [x] 新增 `node` 的 systemd/service 模板
- [x] 更新构建脚本，支持 `node`
- [x] 更新安装/卸载/验证脚本
- [x] 补充 `edge / relay / sink` 示例配置
- [x] 补充 `A-F` 和 `G/H` 的部署示例
- [ ] 明确旧 `sender` / `receiver` 的下线条件和回滚窗口

### 12.8 测试与验证

- [x] 验证本地采集
- [x] 验证 ingress 接收
- [x] 验证 relay 转发
- [x] 验证 fanout + failover
- [x] 验证 inject 注入
- [x] 验证 export 订阅
- [ ] 验证 SIGHUP 热重载
- [x] 验证 `node_id + seq` 在 relay 后保持不变
- [ ] 验证 `inManage` 对 SNMPv1 Trap 的设备识别正确
- [ ] 验证旧 HMS 在过渡期仍可工作

### 12.9 上线切换

- [ ] 在实验环境完成单机测试
- [ ] 在实验环境完成端到端链路测试
- [ ] 先在 `G/H` 试点切换为 `node sink`
- [ ] 保持 `A-F` 仍使用旧 sender 验证中心链路
- [ ] 再将 `A-F` 切换为 `node edge`
- [ ] 验证 `A-F -> MQ` 主链路稳定
- [ ] 验证 `A-F -> G/H -> inject -> inManage` 修复链路稳定
- [ ] 最后引入 export 给新程序接入
- [ ] 确认回滚路径和旧部署模板可用

### 12.10 完全切换到 Node

- [ ] `cmd/node` 已具备替代旧 `sender` 的能力
- [ ] `cmd/node` 已具备替代旧 `receiver` 的能力
- [ ] `A-F` 生产环境已稳定运行 `node edge/relay`
- [ ] `G/H` 生产环境已稳定运行 `node sink`
- [ ] `node` 的构建、安装、验证和运行脚本已完全替代旧入口
- [ ] README 和部署文档已切换为以 `node` 为主
- [ ] 旧 `sender` / `receiver` 已完成至少一轮回滚演练
- [ ] 已确认回退到旧入口的窗口可以关闭
- [ ] 删除旧 `sender` 目录
- [ ] 删除旧 `receiver` 目录
- [ ] 删除旧入口对应的模板和无用配置文件

## 13. 结论

本改造方案的核心不是简单把 `sender` 改名为 `node`，而是把当前系统重构为：

- 统一的 frame
- 统一的能力模型
- 统一的配置和部署抽象

按该路线推进，可以在不破坏现网入口拓扑的前提下，逐步实现 relay、fanout、兼容注入和 export 四项关键能力。
