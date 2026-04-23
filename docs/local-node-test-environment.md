# TrapTunnel 本地可复现测试环境

本文档说明如何在单台 Linux 主机上，使用 Linux network namespace 构建一套可重复创建、可重复销毁的 `node` 本地测试环境。

该环境用于验证最小闭环：

`ns-device -> ns-edge(node edge) -> ns-sink(node sink) -> UDP 162 listener`

## 1. 目标

该测试环境主要用于：

- 验证 `capture`
- 验证 `egress`
- 验证 `ingress`
- 验证 `inject`
- 为后续 `relay / fanout / export / agent-addr 修正` 提供扩展基线

## 2. 拓扑

使用三个 namespace：

- `ns-device`
  - 模拟设备发送 Trap
- `ns-edge`
  - 运行 `node edge`
- `ns-sink`
  - 运行 `node sink`
  - 同时运行一个 UDP 162 监听器，模拟 `inManage`

地址规划：

- `ns-device <-> ns-edge`
  - `ns-device = 10.10.1.2/24`
  - `ns-edge = 10.10.1.1/24`
- `ns-edge <-> ns-sink`
  - `ns-edge = 10.10.2.1/24`
  - `ns-sink = 10.10.2.2/24`

## 3. 已提供的文件

脚本：

- [scripts/dev/setup-netns.sh](/home/qqy/TrapTunnel/scripts/dev/setup-netns.sh)
- [scripts/dev/cleanup-netns.sh](/home/qqy/TrapTunnel/scripts/dev/cleanup-netns.sh)
- [scripts/dev/build-dev-binaries.sh](/home/qqy/TrapTunnel/scripts/dev/build-dev-binaries.sh)
- [scripts/dev/run-edge.sh](/home/qqy/TrapTunnel/scripts/dev/run-edge.sh)
- [scripts/dev/run-sink.sh](/home/qqy/TrapTunnel/scripts/dev/run-sink.sh)
- [scripts/dev/run-sink-listener.sh](/home/qqy/TrapTunnel/scripts/dev/run-sink-listener.sh)
- [scripts/dev/run-sink-listener-socat.sh](/home/qqy/TrapTunnel/scripts/dev/run-sink-listener-socat.sh)
- [scripts/dev/send-udp.sh](/home/qqy/TrapTunnel/scripts/dev/send-udp.sh)
- [scripts/dev/send-snmptrap-v1.sh](/home/qqy/TrapTunnel/scripts/dev/send-snmptrap-v1.sh)
- [scripts/dev/run-edge-tcpdump.sh](/home/qqy/TrapTunnel/scripts/dev/run-edge-tcpdump.sh)
- [scripts/dev/run-sink-tcpdump.sh](/home/qqy/TrapTunnel/scripts/dev/run-sink-tcpdump.sh)

示例配置：

- [examples/node-edge.toml](/home/qqy/TrapTunnel/examples/node-edge.toml)
- [examples/node-sink.toml](/home/qqy/TrapTunnel/examples/node-sink.toml)

辅助工具：

- [cmd/udp-listener/main.go](/home/qqy/TrapTunnel/cmd/udp-listener/main.go)

## 4. 前置条件

需要：

- Linux 主机
- `iproute2`，即 `ip netns`
- root 或 sudo 权限
- Go 工具链

原因：

- `ip netns` 需要管理员权限
- `node` 的 `capture` / `inject` 依赖原始套接字，也需要管理员权限

## 5. 快速开始

### 5.1 构建测试二进制

```bash
./scripts/dev/build-dev-binaries.sh
```

默认会生成：

- `.tmp/dev-bin/node`
- `.tmp/dev-bin/udp-listener`

### 5.2 创建 namespace 网络

```bash
./scripts/dev/setup-netns.sh
```

### 5.3 启动 sink 侧监听器

在第一个终端执行：

```bash
./scripts/dev/run-sink-listener.sh
```

默认监听：

- `127.0.0.1:162`

如果你想直接用现成开源工具而不是仓库自带监听器，也可以执行：

```bash
./scripts/dev/run-sink-listener-socat.sh
```

它会在 `ns-sink` 里用 `socat` 监听 `127.0.0.1:162` 并直接打印收到的 UDP 内容。

### 5.4 启动 node sink

在第二个终端执行：

```bash
./scripts/dev/run-sink.sh
```

默认读取：

- `examples/node-sink.toml`

### 5.5 启动 node edge

在第三个终端执行：

```bash
./scripts/dev/run-edge.sh
```

默认读取：

- `examples/node-edge.toml`

### 5.6 从设备侧发送测试 UDP

在第四个终端执行：

```bash
./scripts/dev/send-udp.sh
```

默认行为：

- 向 `10.10.1.1:162` 发送 payload `trap-test`

### 5.7 使用 snmptrap 发送真实 SNMPv1 Trap

如果需要验证真实 SNMPv1 Trap 报文，而不是普通 UDP payload，可以执行：

```bash
./scripts/dev/send-snmptrap-v1.sh
```

默认行为：

- 从 `ns-device` 向 `10.10.1.1:162` 发送一个 SNMPv1 Trap
- 默认 `community = public`
- 默认 `agent-addr = 10.10.1.2`

### 5.8 使用 tcpdump 抓包

如果需要直接观察边缘和 sink 侧链路，可分别执行：

```bash
./scripts/dev/run-edge-tcpdump.sh
./scripts/dev/run-sink-tcpdump.sh
```

默认抓取：

- `ns-edge`
  - UDP Trap 流量
  - 到 `10000/TCP` 的隧道流量
- `ns-sink`
  - 到 `10000/TCP` 的隧道流量
  - 到 `162/UDP` 的 inject 流量

## 6. 成功判定

若最小闭环正常，应该看到：

- `node edge` 日志中出现 `PacketCaptured` / `TrapSent`
- `node sink` 日志中出现 `TrapReceived`
- `udp-listener` 收到一条 UDP 消息

## 7. 稳定性测试

可以用循环多发一些包，例如：

```bash
for i in $(seq 1 100); do
  ./scripts/dev/send-udp.sh 10.10.1.1 162 "trap-$i"
done
```

观察：

- sink 侧是否都收到
- `node edge` / `node sink` 是否异常退出
- 日志中是否有明显错误

若配合现成工具，可以这样做：

1. 用 `./scripts/dev/run-edge-tcpdump.sh` 观察 `ns-edge` 是否收到 Trap
2. 用 `./scripts/dev/run-sink-tcpdump.sh` 观察 `10000/TCP` 和 `162/UDP` 是否都有流量
3. 用 `./scripts/dev/run-sink-listener-socat.sh` 直接看 sink 侧最终收到的 UDP 内容

## 8. 清理环境

```bash
./scripts/dev/cleanup-netns.sh
```

## 9. 当前范围与限制

这套测试环境目前主要验证 `12.2` 的最小闭环。

当前重点：

- `edge -> sink -> inject`
- 普通 UDP payload

暂不以该环境直接验证：

- SNMPv1 `agent-addr` 修正
- relay
- fanout
- export

这些能力应在后续阶段逐步加入测试样例。

## 10. 可直接使用的开源工具

除了当前仓库自带脚本，也可以直接使用一些常见开源工具：

- `ip netns`
  - Linux 自带的 namespace 工具，是本地网络拓扑模拟的基础
- `snmptrap`（来自 Net-SNMP）
  - 可直接发送真实 SNMP Trap，适合后续替代 `send-udp.sh`
- `socat`
  - 可用于快速做 UDP/TCP 收发验证
- `tcpdump`
  - 可用于在 namespace 内抓包观察链路

为什么当前仍保留仓库内脚本：

- 能做到仓库内自包含
- 不强依赖额外安装第三方工具
- 初期先验证最小闭环，脚本门槛最低

推荐策略：

- 第一阶段先用仓库内脚本
- 需要验证真实 SNMP Trap 语义时，用 `snmptrap`
- 需要快速替代监听器时，用 `socat`
- 需要直接看链路细节时，用 `tcpdump`
