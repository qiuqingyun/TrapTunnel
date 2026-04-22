# TrapTunnel Node 架构设计

## 1. 文档目标

本文档定义 TrapTunnel 从当前 `sender + receiver` 双程序模型，演进到统一 `node` 模型后的目标形态。重点覆盖：

- 能力模型
- 数据流与协议语义
- 配置模型
- 部署拓扑
- 与现有业务系统的关系

本文档描述的是目标架构，不代表当前主分支已经全部实现。

## 2. 设计目标

统一 `node` 架构需要满足以下目标：

1. 使用单一节点模型覆盖边缘采集、中继、中心注入和外部导出。
2. 保留原始 Trap 报文事实，不在中继链路中提前做不可逆改写。
3. 支持“组内主备、组间扇出”的多上游发送策略。
4. 支持中继节点同时处理本地采集和下游节点转发流量。
5. 兼容遗留系统 `inManage` 对 `162/UDP` 的接入方式。
6. 为后续替代 HMS 的新告警程序提供可靠的 TCP 导出接口。

## 3. 核心概念

### 3.1 Node

`node` 是统一后的唯一运行单元。一个 `node` 可以启用以下能力中的任意组合：

- `capture`
- `ingress`
- `egress`
- `inject`
- `export`

### 3.2 Frame

`frame` 是节点内部和节点之间的统一传输单位，结构为：

- `total_length`
- `node_id`
- `sequence_id`
- `raw_ip_packet`

规则：

- 本地抓到的原始 IP 包，在首次进入隧道时分配 `node_id` 和 `sequence_id`
- 中继节点只转发 frame，不重新封装为新的节点序列
- `node_id` 在全网范围内应保持唯一

### 3.3 Sink

`inject`、`egress`、`export` 都属于输出 sink。

- `inject sink` 面向本机网络栈
- `egress sink` 面向其他 node
- `export sink` 面向外部业务程序

同一个输入 frame 可以同时进入多个 sink。

## 4. 能力模型

### 4.1 Capture

职责：

- 使用原始套接字监听指定 UDP 目的端口
- 读取完整原始 IP 包
- 生成 frame 并分配序列号

约束：

- 仅首次进入隧道的数据包由 capture 分配序列号
- 一个节点上的多个 capture 端口共享同一个 `node_id`

### 4.2 Ingress

职责：

- 监听来自其他 node 的 TCP 隧道连接
- 解析 frame
- 将 frame 送入本地分发流水线

约束：

- ingress 不对 frame 重编号
- ingress 不默认修改 payload

### 4.3 Egress

职责：

- 将 frame 转发到一个或多个上游目标
- 支持主备与扇出组合语义

目标配置：

- 使用 `[[egress.groups]]` 表示 fanout group
- 每个 group 输出一份副本
- `members` 列表表示 failover members
- group 内任意时刻只有一个活跃目标
- 只有一个成员的 group 等价于单目标直连

设计要求：

- 每个 fanout group 独立维护连接与缓冲
- 某个 group 故障或阻塞不能拖垮其他 group
- 热重载支持修改 group 列表和优先级

### 4.4 Inject

职责：

- 将 frame 的副本按本地策略修正后注入本机网络

处理逻辑：

1. 复制原始 payload
2. 应用本地注入目标 IP
3. 可选应用 SNMPv1 `agent-addr` 源 IP 修正
4. 重新计算 IPv4/UDP 校验和
5. 将副本注入本机网络

关键原则：

- `inject` 只处理本地副本
- `egress` 和 `export` 继续使用原始 frame

### 4.5 Export

职责：

- 向外部程序提供 TCP 长连接订阅接口
- 持续输出原始 frame

首版协议：

- 直接复用内部隧道帧格式
- 订阅端可以获得 `node_id`、`sequence_id` 和原始 IP 包

设计要求：

- export 为旁路消费能力，不得影响主业务转发
- 每个客户端独立缓冲
- 客户端过慢时应断开或丢弃，不得反压主链路

## 5. 角色模板

建议内置以下角色模板：

- `edge`
  - `capture + egress`
- `relay`
  - `capture + ingress + egress`
- `sink`
  - `ingress + inject`
- `full`
  - `capture + ingress + egress + inject + export`

角色模板仅用于简化配置，不限制显式覆盖。例如一个 `relay` 节点可以额外开启 `export`。

## 6. 数据流

统一数据流如下：

`capture / ingress -> frame bus -> inject sink / egress sink / export sink`

### 6.1 本地采集流

1. `capture` 从本地端口抓到 Trap
2. 节点为其分配 `node_id + seq`
3. frame 进入内部总线
4. 根据配置并行输出到 `egress`、`inject`、`export`

### 6.2 中继流

1. `ingress` 收到来自下游节点的 frame
2. frame 保持原始 `node_id + seq`
3. frame 进入内部总线
4. 根据配置并行输出到 `egress`、`inject`、`export`

### 6.3 最终落地流

1. `ingress` 收到 frame
2. `inject` 复制 payload
3. 若为 SNMPv1 Trap，检查是否需要按 `agent-addr` 改写源 IP
4. 将副本注入本地 `inject.ip:inject.port`
5. 同时可继续向 `export` 或其他 `egress` 输出原始 frame

## 7. SNMPv1 agent-addr 修正

### 7.1 背景

遗留系统 `inManage` 以 UDP 外层源 IP 作为设备标识进行解析。对于经过代理转发的 SNMPv1 Trap，UDP 外层源 IP 可能是代理设备地址，而真正的设备地址位于 PDU 内的 `agent-addr` 字段中。

### 7.2 规则

仅在以下条件同时满足时生效：

- 当前 sink 为 `inject`
- UDP payload 可识别为 SNMPv1 Trap-PDU
- `agent-addr` 为合法 IPv4
- `agent-addr` 与外层 UDP 源 IP 不一致

### 7.3 原则

- 不在 frame 主链路上修改源 IP
- 不在 `egress` 和 `export` 输出中覆盖原始源 IP
- 仅在最终注入副本上做兼容性改写

## 8. 环路控制

### 8.1 本机自回环

若同一节点同时启用 `capture` 和 `inject`，且注入目标端口重新命中本机 capture 端口，则可能形成自回环。

首版策略：

- `inject.port` 必须与任一 `capture.listen_ports` 不同
- 节点启动时进行显式冲突校验

### 8.2 拓扑环路

即使避免本机端口回环，节点之间仍可能因配置错误形成转发环路，例如 `A -> B -> A`。

首版约束：

- 拓扑设计上避免 relay 形成闭环
- 运维文档明确要求 egress 只朝上游方向配置

后续可选增强：

- hop 限制
- frame trace 标记
- loop detection 告警

## 9. 配置模型

建议统一配置为：

```toml
[node]
id = 101
name = "NODE-A"
profile = "relay"

[capture]
enabled = true
listen_ports = [162]

[ingress]
enabled = true
listen = "0.0.0.0:10000"

[egress]
enabled = true
reconnect_interval = 5

[[egress.groups]]
members = ["172.16.10.10:10000", "172.16.10.11:10000"]

[[egress.groups]]
members = ["172.16.20.10:10000"]

[inject]
enabled = false
ip = "127.0.0.1"
port = 1162
snmpv1_agent_addr_override = false

[export]
enabled = true
listen = "0.0.0.0:12000"
format = "frame"
max_clients = 32

[logging]
level = "INFO"
max_log_size = 10
max_log_backups = 100
```

## 10. 目标部署架构

### 10.1 现有环境抽象

- `A/B`、`C/D`、`E/F` 是三组设备入口节点
- 每组内部通过 Keepalived 维护一个 VIP
- 设备向三组 VIP 中的一组发送 Trap
- `G/H` 是中心系统 `inManage` 所在集群节点

### 10.2 目标架构

边缘层：

- `A-F` 部署 `node`
- 每组继续保留入口 VIP
- 边缘节点负责采集本地 Trap，并上送到中心层 node

中心层：

- `G/H` 部署 `node`
- 负责接收边缘 frame
- 负责在 `inject` 路径执行 SNMPv1 `agent-addr` 修正
- 负责将修正后的副本注入本地，供 `inManage` 接收

业务消费层：

- 旧 HMS 继续通过 `162/UDP` 接入时，只作为过渡
- 新告警程序优先通过 `export` 接口接入
- 业务消费高可用建立在 export 消费层，不与入口 VIP 绑定

## 11. 最佳实践对比

该方案相对当前架构已经明显收敛，但仍属于偏务实的工程落地路径。与更理想的最佳实践相比：

- 入口高可用和业务消费高可用被清晰拆分，这是正确方向
- 原始 Trap 事实保留在 `frame/export` 层，优于只依赖本地 UDP 注入
- `inManage` 兼容逻辑被限制在 `inject` 路径，避免污染主链路
- 但首版 `export` 仍是 TCP 流接口，不是具备重放/持久化能力的消息总线
- 若未来业务复杂度继续增加，可评估引入 Kafka、NATS 或其他事件总线

## 12. 非目标

首版 `node` 架构不处理以下内容：

- HMS 业务规则本身的重构
- 多租户隔离
- 持久化消息队列
- 跨机房多活调度
- 通用 SNMPv2/v3 内层地址修正

## 13. 结论

统一 `node` 架构的价值在于：

- 从“程序角色”切换为“能力组合”
- 保留原始 Trap 事实
- 兼容遗留系统
- 为新告警程序提供更稳定的接入面

该设计既能覆盖当前 `sender/receiver` 形态，也能支持未来边缘中继、中心修正和外部导出三类核心需求。
