# TrapTunnel

TrapTunnel 是一个面向 SNMP Trap、Syslog 等 UDP 报文的轻量级隧道与分发工具。当前代码基于 `sender` / `receiver` 双组件实现，目标架构将统一演进为单一 `node` 节点模型，用同一套能力组合覆盖边缘采集、中继、中心注入和外部订阅导出等场景。

## 当前状态

当前主分支已经实现的能力：

- `sender` 使用原始套接字 (`ip4:udp`) 捕获指定 UDP 目的端口的 IP 数据包。
- `sender` 将原始 IP 包封装为自定义隧道帧，通过 TCP 转发到 `receiver`。
- `receiver` 监听 TCP 隧道连接，解包后修改目的 IP 并重新注入网络。
- `sender` 支持多上游主备切换、`SIGHUP` 热重载和结构化日志。
- `receiver` 支持按 `node_id` 跟踪序列号，用于丢包监测。

当前实现入口：

- [sender/sender.go](/home/qqy/TrapTunnel/sender/sender.go)
- [receiver/receiver.go](/home/qqy/TrapTunnel/receiver/receiver.go)

## Node 目标架构

未来 TrapTunnel 将统一为单一 `node` 程序。`node` 通过组合能力开关或角色模板，覆盖当前 `sender` 和 `receiver` 的所有职责。

### 核心能力

- `capture`
  - 使用原始套接字捕获本地指定 UDP 端口的原始 IP 包。
- `ingress`
  - 监听来自其他 `node` 的 TCP 隧道流量。
- `egress`
  - 将收到的原始帧继续转发到多个上游目标，支持“组内主备、组间扇出”。
- `inject`
  - 将收到的数据包副本按本地策略修正后注入本机网络。
- `export`
  - 通过 TCP 向外部程序持续输出原始 Trap 数据，供更强的告警分析程序订阅消费。

### 角色模板

建议提供以下模板，并允许显式覆盖：

- `edge = capture + egress`
- `relay = capture + ingress + egress`
- `sink = ingress + inject`
- `full = capture + ingress + egress + inject + export`

### 数据流模型

统一节点内部流水线：

`capture / ingress -> frame -> inject sink / egress sink / export sink`

设计约束：

- `frame` 是节点内外统一的数据单元。
- 本地抓到的报文只在首次进入隧道时分配 `node_id + seq`。
- 中继节点只透传已有 `frame`，不得重新编号。
- `inject` 只修改用于本地注入的那一份副本，不污染继续转发和导出的原始帧。

## 规划中的新功能

以下能力属于目标中的 `node` 新能力，当前主分支尚未全部实现：

### 1. 接力传递

支持某个节点同时承担：

- 本地抓包
- 接收下游节点转发过来的隧道流量
- 将本地和下游流量统一转发到上游

示例：

- `B -> A -> G`
- `C -> A -> G`
- 同时 `A` 也发送自身本地采集到的 Trap

### 2. 多接收者发送

`egress.groups` 将支持“组内主备、组间扇出”的结构化配置。

语义如下：

- 每个 `[[egress.groups]]` 都会收到一份副本
- `members` 列表内部为主备组，任一时刻只向其中一个健康目标发送
- 只有一个成员的 group 等价于单目标直连

### 3. SNMPv1 agent-addr 源 IP 修正

为兼容只看 UDP 外层源 IP 的遗留系统，`inject` 路径将支持以下逻辑：

- 仅对可识别为 `SNMPv1 Trap-PDU` 的数据包生效
- 解析 PDU 中的 `agent-addr`
- 若 `agent-addr` 为合法 IPv4 且与 UDP 外层源 IP 不一致，则使用 `agent-addr` 覆盖注入副本的源 IP
- 重新计算 IPv4/UDP 校验和后再注入

该修正仅作用于本地 `inject` 副本，不影响 `egress` 和 `export` 输出的原始数据。

### 4. 原始 Trap 导出接口

`export` 将提供 TCP 长连接接口，对外持续输出原始 Trap 帧，供后续告警管理程序直接订阅。

首版建议直接复用隧道帧格式：

- `4B total_length`
- `2B node_id`
- `4B sequence_id`
- `raw_ip_packet`

优点：

- 复用现有帧协议
- 对外保留 `node_id` 和 `seq`
- 便于新程序直接消费边缘原始事实，而非依赖本地 UDP 注入

## 规划中的部署形态

### 现状

- `A/B`、`C/D`、`E/F` 三组边缘接入节点，各组内部通过 Keepalived 切换一个 VIP。
- 设备将 Trap 发到三组 VIP 中的某一组。
- `A-F` 上部署 `sender`，把 Trap 转发到 `G/H`。
- `G/H` 上部署 `receiver`，把 Trap 注入本地 `162/UDP`，供 `inManage` 接收。
- `A-F` 上还各自部署 HMS，从本机 `162/UDP` 获取 Trap。

### 目标

- `A-F` 部署统一 `node`，承担边缘接入和上送职责。
- `G/H` 部署统一 `node`，承担中心接入、SNMPv1 `agent-addr` 修正和本地注入职责。
- 新一代告警程序不再依赖 `162/UDP`，而是直接消费 `node export` 接口。

部署原则：

- 设备入口高可用域与业务消费高可用域分离。
- 三组 VIP 继续作为设备入口层。
- HMS 或其替代程序的主备策略建立在 `export` 消费层，而不是入口 VIP 层。

## 配置方向

未来 `node` 配置会围绕能力开关展开，示意如下：

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
members = ["10.0.0.1:10000", "10.0.0.2:10000"]

[[egress.groups]]
members = ["10.0.1.1:10000"]

[inject]
enabled = false
ip = "127.0.0.1"
port = 1162
snmpv1_agent_addr_override = false

[export]
enabled = true
listen = "0.0.0.0:12000"
format = "frame"
```

环路控制原则：

- 当同一节点同时启用 `capture` 和 `inject` 时，`inject.port` 不应与任一 `capture.listen_ports` 相同。
- 启动阶段应对该配置冲突做显式校验并拒绝启动或至少告警。

## 当前构建方式

当前代码仍使用双组件构建方式：

```bash
# 初始化模块（首次）
go mod tidy

# 构建 Sender
go build -o sender ./sender

# 构建 Receiver
go build -o receiver ./receiver
```

配套脚本位于：

- [build/scripts/build.sh](/home/qqy/TrapTunnel/build/scripts/build.sh)

## 文档

- [Node 架构设计](/home/qqy/TrapTunnel/docs/node-architecture.md)
- [Node 改造方案](/home/qqy/TrapTunnel/docs/node-migration-plan.md)
- [告警管理平台整体架构](/home/qqy/TrapTunnel/docs/alerting-platform-architecture.md)

## 协议格式

当前隧道使用简单的自定义二进制协议：

| 字段 | 大小 | 描述 |
| :--- | :--- | :--- |
| `Total Length` | 4 字节 | 后续数据总长度，BigEndian |
| `Node ID` | 2 字节 | 发送节点 ID，BigEndian |
| `Sequence ID` | 4 字节 | 节点内递增序列号，BigEndian |
| `Payload` | 可变 | 原始 IPv4 数据包 |

## 前置要求

- 操作系统：Linux 为主，Windows 需额外评估原始套接字权限和驱动限制
- Go：当前代码依赖 Go 1.18+
- 权限：创建原始套接字和注入报文需要 `root/管理员` 权限

## 免责声明

本工具使用原始套接字，绕过了部分协议栈的标准处理流程。仅供合法的网络管理、测试和监控目的使用，请确保您有权捕获、转发与注入相关流量。
