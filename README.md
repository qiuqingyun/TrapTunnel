# TrapTunnel

TrapTunnel 是一个基于 Go 语言开发的轻量级网络隧道工具，旨在发送端节点捕获 UDP 流量，将其封装并通过 TCP 隧道传输到接收端节点，最后在接收端解包并修改目的 IP 后注入到网络中。

该工具特别适用于需要将边缘节点的 UDP 流量（例如 SNMP Trap、Syslog）通过可靠的 TCP 链路转发到中心收集器的场景，同时能够保留原始数据包的结构（目的 IP 除外）。

## 架构

系统主要由两个组件组成：

1.  **Sender (发送端/客户端)**
    *   运行在边缘节点。
    *   使用原始套接字 (`ip4:udp`) 监听指定端口的传入 UDP 数据包。
    *   捕获与配置的监听端口匹配的数据包。
    *   使用自定义头部（包含长度、节点 ID、序列号）封装原始 IP 数据包。
    *   通过持久的 TCP 连接将封装后的数据转发给 Receiver。

2.  **Receiver (接收端/服务端)**
    *   运行在中心节点。
    *   监听来自 Sender 的 TCP 连接。
    *   对传入的数据进行解包。
    *   跟踪序列号以监测丢包情况。
    *   将内部 IP 数据包的目的 IP 修改为配置的 `inject_ip`。
    *   重新计算 IP 和 UDP 校验和以确数据包有效。
    *   使用原始套接字 (`ip4:raw`) 将修改后的数据包注入网络接口。

## 特性

*   **原始套接字捕获/注入**: 在 IP 层操作，保证高保真度。
*   **可靠传输**: 通过 TCP 隧道传输 UDP 流量，确保跨网络交付。
*   **丢包监测**: 根据节点 ID 跟踪序列号，检测丢失的数据包。
*   **可配置**: 发送端和接收端均使用简单的 INI 格式配置文件。
*   **轻量级**: 使用 Go 编写，依赖极少。

## 前置要求

*   **操作系统**: Linux (推荐) 或 Windows (需要相应的权限/驱动)。
*   **Go**: 已安装 Go 1.18+。
*   **权限**: Sender 和 Receiver 都 **必须** 拥有 **Root/管理员** 权限才能创建原始套接字。

## 构建

你可以使用 `go build` 命令为两个组件构建二进制文件。

### 构建 Sender
```bash
cd sender
go build -o sender
```

### 构建 Receiver
```bash
cd receiver
go build -o receiver
```

## 配置

### Sender (`sender/sender.conf`)

```ini
[common]
node_id = 1                 # 此发送节点的唯一 ID
b_server_ip = 215.1.194.73  # Receiver (服务端) IP 地址
b_server_port = 10000       # Receiver (服务端) TCP 端口

[advanced]
listen_port = 162           # 要捕获流量的 UDP 端口 (例如 162 用于 SNMP Trap)
reconnect_interval = 5      # 重连等待时间 (秒)
max_buffer_size = 2000      # 内部通道缓冲区大小
```

### Receiver (`receiver/receiver.conf`)

```ini
[server]
listen_port = 10000         # 监听 Sender 连接的 TCP 端口
inject_ip = 127.0.0.1       # 注入数据包的新目的 IP

[logging]
log_file = origin_trap_receiver.log # 日志文件路径
```

## 使用方法

### 1. 启动 Receiver
在中心服务器上运行（必须作为 root 运行）：
```bash
sudo ./receiver -c receiver.conf
```

### 2. 启动 Sender
在边缘节点上运行（必须作为 root 运行）：
```bash
sudo ./sender -c sender.conf
```

## 协议格式

隧道使用简单的自定义二进制协议：

| 字段 | 大小 | 描述 |
| :--- | :--- | :--- |
| **Total Length** | 4 字节 | 后续数据的长度 (BigEndian) |
| **Node ID** | 2 字节 | 发送节点的 ID (BigEndian) |
| **Sequence ID** | 4 字节 | 用于丢包跟踪的递增序列号 (BigEndian) |
| **Payload** | 可变 | 原始 IP 数据包 |

## 免责声明

本工具使用原始套接字，绕过了特定协议的标准网络栈处理。仅供合法的网络管理、测试和监控目的使用。请确保您有权捕获并向您的网络注入流量。
