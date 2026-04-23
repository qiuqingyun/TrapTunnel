# TrapTunnel 本地可复现测试环境

本文档说明如何在单台 Linux 主机上，使用 Linux network namespace 构建一套可重复创建、可重复销毁的 `node` 本地测试环境。

该环境用于验证两类链路：

- 最小闭环：
  - `ns-device -> ns-edge(node edge) -> ns-sink(node sink) -> UDP 162 listener`
- relay / fanout / failover：
  - `ns-device -> ns-edge(node edge) -> ns-relay(node relay) -> ns-sink-a / ns-sink-b`

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
- [scripts/dev/run-export-client.sh](/home/qqy/TrapTunnel/scripts/dev/run-export-client.sh)
- [scripts/dev/setup-relay-netns.sh](/home/qqy/TrapTunnel/scripts/dev/setup-relay-netns.sh)
- [scripts/dev/cleanup-relay-netns.sh](/home/qqy/TrapTunnel/scripts/dev/cleanup-relay-netns.sh)
- [scripts/dev/run-relay-edge.sh](/home/qqy/TrapTunnel/scripts/dev/run-relay-edge.sh)
- [scripts/dev/run-relay-node.sh](/home/qqy/TrapTunnel/scripts/dev/run-relay-node.sh)
- [scripts/dev/run-relay-sink-a.sh](/home/qqy/TrapTunnel/scripts/dev/run-relay-sink-a.sh)
- [scripts/dev/run-relay-sink-b.sh](/home/qqy/TrapTunnel/scripts/dev/run-relay-sink-b.sh)
- [scripts/dev/run-relay-listener.sh](/home/qqy/TrapTunnel/scripts/dev/run-relay-listener.sh)
- [scripts/dev/send-relay-udp.sh](/home/qqy/TrapTunnel/scripts/dev/send-relay-udp.sh)

示例配置：

- [examples/node-edge.toml](/home/qqy/TrapTunnel/examples/node-edge.toml)
- [examples/node-sink-export.toml](/home/qqy/TrapTunnel/examples/node-sink-export.toml)
- [examples/node-sink-agent-addr.toml](/home/qqy/TrapTunnel/examples/node-sink-agent-addr.toml)
- [examples/node-relay.toml](/home/qqy/TrapTunnel/examples/node-relay.toml)
- [examples/node-sink.toml](/home/qqy/TrapTunnel/examples/node-sink.toml)
- [examples/relay-test-edge.toml](/home/qqy/TrapTunnel/examples/relay-test-edge.toml)
- [examples/relay-test-relay.toml](/home/qqy/TrapTunnel/examples/relay-test-relay.toml)
- [examples/relay-test-sink-a.toml](/home/qqy/TrapTunnel/examples/relay-test-sink-a.toml)
- [examples/relay-test-sink-b.toml](/home/qqy/TrapTunnel/examples/relay-test-sink-b.toml)

辅助工具：

- [cmd/udp-listener/main.go](/home/qqy/TrapTunnel/cmd/udp-listener/main.go)
- [cmd/export-client/main.go](/home/qqy/TrapTunnel/cmd/export-client/main.go)

## 4. 前置条件

需要：

- Linux 主机
- `iproute2`，即 `ip netns`
- root 或 sudo 权限
- Go 工具链

原因：

- `ip netns` 需要管理员权限
- `node` 的 `capture` / `inject` 依赖原始套接字，也需要管理员权限

说明：

- 绝大多数运行时缓冲和超时参数都可通过 `[tuning]` 配置覆盖。
- 若测试洪峰、慢客户端、异常大包，建议显式调整：
  - `tuning.pipeline_buffer_size`
  - `tuning.egress_group_buffer_size`
  - `tuning.export_client_buffer_size`
  - `tuning.max_frame_total_length`
  - `export.slow_client_policy`
- 若配置文件不写这些项，会自动使用代码里的默认值。

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

已通过 Go 单元测试验证：

- relay 收到的 `frame` 可直接进入 egress
- `[[egress.groups]]` 的组间 fanout
- group 内 failover
- relay 后 `node_id + seq` 保持不变

已通过本地 `netns` 实测验证：

- `edge -> relay -> sink-a / sink-b` 可实际跑通
- relay 的 fanout 可同时送达两个 sink
- relay 的 failover 可在首成员连接失败时切到备成员
- sink 侧收到的 `node_id + seq` 与 edge 发出的值保持一致
- `SNMPv1 agent-addr` 修正可在 `inject` 路径生效
- 当 `agent-addr` 与外层 UDP 源 IP 不一致时，sink 侧监听器看到的是修正后的源 IP
- `export` 客户端可直接消费原始 frame 流

暂不以该环境直接验证：

- 复杂的 MQ 桥接消费链路

这些能力应在后续阶段继续补充到测试样例。

## 10. relay / fanout / failover 实测

推荐拓扑：

- `ns-device`
  - 发送 UDP Trap
- `ns-edge`
  - 运行 `node edge`
- `ns-relay`
  - 运行 `node relay`
- `ns-sink-a`
  - 运行 `node sink`
  - 运行 UDP 监听器
- `ns-sink-b`
  - 运行 `node sink`
  - 运行 UDP 监听器

测试命令顺序：

```bash
./scripts/dev/build-dev-binaries.sh
./scripts/dev/setup-relay-netns.sh
./scripts/dev/run-relay-listener.sh ns-sink-a
./scripts/dev/run-relay-listener.sh ns-sink-b
./scripts/dev/run-relay-sink-a.sh
./scripts/dev/run-relay-sink-b.sh
./scripts/dev/run-relay-node.sh
./scripts/dev/run-relay-edge.sh
./scripts/dev/send-relay-udp.sh 10.20.1.1 162 relay-stage3-test
```

默认语义：

- `relay-test-edge.toml`
  - `edge` 抓 `10.20.1.1:162`
  - 上送到 `10.20.2.2:11000`
- `relay-test-relay.toml`
  - `relay` 监听 `10.20.2.2:11000`
  - `group 0 = ["10.20.3.2:10001", "10.20.3.2:10000"]`
    - 用错误端口触发 failover
  - `group 1 = ["10.20.4.2:10000"]`
    - 用于验证 fanout

成功判定：

- `node edge` 日志出现：
  - `PacketCaptured`
  - `TrapSent`
- `node relay` 日志出现：
  - `RelayEnqueue`
  - `FanoutDispatch`
  - 对 `10.20.3.2:10001` 的 `ConnFailed`
  - 对 `10.20.3.2:10000` 和 `10.20.4.2:10000` 的 `ConnEstablished`
- `node sink-a` / `node sink-b` 日志都出现：
  - `TrapReceived`
- 两个 UDP listener 都收到同一条 payload

清理命令：

```bash
./scripts/dev/cleanup-relay-netns.sh
```

## 11. SNMPv1 agent-addr 修正实测

测试命令顺序：

```bash
./scripts/dev/build-dev-binaries.sh
./scripts/dev/setup-netns.sh
./scripts/dev/run-sink-listener.sh
./scripts/dev/run-sink.sh ./examples/node-sink-agent-addr.toml
./scripts/dev/run-edge.sh
./scripts/dev/send-snmptrap-v1.sh 10.10.1.1 162 public 10.200.30.40
```

成功判定：

- `node edge` 日志出现：
  - `PacketCaptured`
  - `TrapSent`
- `node sink` 日志出现：
  - `TrapReceived`
  - `InjectSourceOverride`
- UDP listener 最终收到的源 IP 为 `10.200.30.40`
- 而不是设备侧 namespace 的外层源 IP `10.10.1.2`

该用例用于证明：

- 仅 `inject` 副本执行源 IP 修正
- 修正来源于 SNMPv1 Trap 内的 `agent-addr`
- 修正后的副本已能被本地 UDP 应用正常接收

清理命令：

```bash
./scripts/dev/cleanup-netns.sh
```

## 12. Export 实测

测试命令顺序：

```bash
./scripts/dev/build-dev-binaries.sh
./scripts/dev/setup-netns.sh
./scripts/dev/run-sink-listener.sh
./scripts/dev/run-sink.sh ./examples/node-sink-export.toml
./scripts/dev/run-edge.sh
./scripts/dev/run-export-client.sh ns-edge 10.10.2.2:12000 1
./scripts/dev/send-udp.sh 10.10.1.1 162 export-test
```

成功判定：

- `node sink` 日志出现：
  - `ExportStartup`
  - `ExportClientConnected`
- `export-client` 输出一条 frame，例如：
  - `frame=1 node_id=101 seq=1 size=45`
- `udp-listener` 仍正常收到同一条 UDP payload

该用例用于证明：

- `export` 输出的是原始 frame，不影响 inject 路径
- 新程序可以直接通过 TCP 订阅 `node export`
- `export` 与本地 inject 可并行工作

清理命令：

```bash
./scripts/dev/cleanup-netns.sh
```

## 13. 可直接使用的开源工具

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
