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
    *   通过持久的 TCP 连接将封装后的数据转发给 Receiver，支持多服务器主备切换。
    *   支持配置热重载（基于 SIGHUP 信号）和日志轮转。

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
*   **高可用性**: Sender 支持配置多个目标服务器（主备模式），连接失败自动切换。
*   **热更新**: 支持通过 SIGHUP 信号热重载配置（目标服务器、NodeID、日志设置等），无需重启进程。
*   **完善的日志**: 支持结构化日志 (logfmt) 和动态日志级别 (DEBUG/INFO/WARN/ERROR)，支持文件自动轮转。
*   **丢包监测**: 根据节点 ID 跟踪序列号，检测丢失的数据包。
*   **轻量级**: 使用 Go 编写，依赖极少。

## 前置要求

*   **操作系统**: Linux (推荐) 或 Windows (需要相应的权限/驱动)。
*   **Go**: 已安装 Go 1.18+。
*   **权限**: Sender 和 Receiver 都 **必须** 拥有 **Root/管理员** 权限才能创建原始套接字。

## 构建

你可以使用 `go build` 命令为两个组件构建二进制文件。

```bash
# 初始化模块（首次）
go mod tidy

# 构建 Sender
go build -o sender ./sender

# 构建 Receiver
go build -o receiver ./receiver
```

## 配置

### Sender (`sender/sender.conf`)

```ini
[common]
node_id = 1                 # 此发送节点的唯一 ID
# 目标服务器列表，支持配置多个实现主备故障转移 (逗号分隔)
servers = 215.1.194.73:10000, 192.168.1.100:10000

[advanced]
listen_port = 162           # 要捕获流量的 UDP 端口 (例如 162 用于 SNMP Trap)
reconnect_interval = 5      # 重连等待时间 (秒)
max_buffer_size = 2000      # 内部通道缓冲区大小

[logging]
# 日志文件路径已使用系统默认值 (Linux: /var/log/traptunnel/sender.log)
max_log_size = 10           # 单个日志文件最大大小 (MB)
max_log_backups = 100       # 保留的旧日志文件最大数量
```

### Receiver (`receiver/receiver.conf`)

```ini
[server]
listen_port = 10000         # 监听 Sender 连接的 TCP 端口
inject_ip = 127.0.0.1       # 注入数据包的新目的 IP

[logging]
# 日志文件路径已使用系统默认值 (Linux: /var/log/traptunnel/receiver.log)
max_log_size = 10
max_log_backups = 100
log_level = INFO

[nodes]
1 = HSH                       # NodeID 1 对应的名称，日志中将显示 HSH(1)
2 = HF                        # NodeID 2 对应的名称
```

## 使用方法

### 1. 启动 Receiver
在中心服务器上运行（必须作为 root 运行）：
```bash
sudo ./receiver -c receiver.conf
```
*注意：在 Linux 下运行时，默认日志路径为 `/var/log/traptunnel/receiver.log`。请确保运行用户有权创建该目录或目录已存在。*

### 2. 启动 Sender
在边缘节点上运行（必须作为 root 运行）：
```bash
sudo ./sender -c sender.conf
```
*注意：在 Linux 下运行时，默认日志路径为 `/var/log/traptunnel/sender.log`。*

### 3. 配置热重载 (Sender 和 Receiver)
Sender 和 Receiver 都支持在不重启进程的情况下更新配置。
在 Linux 系统下，向进程发送 **SIGHUP** 信号即可触发重载：

```bash
# 重载 Sender 配置 (目标服务器、NodeID 等)
sudo kill -HUP <sender_pid>

# 重载 Receiver 配置 (节点名称映射、日志路径等)
sudo kill -HUP <receiver_pid>
```

日志中将显示 `收到 SIGHUP 信号，正在重载配置...`。

### 4. Systemd 集成与热重载
建议使用 Systemd 管理 Sender 和 Receiver 服务，并利用其 `reload` 命令触发热更新。

以 Sender 为例，创建 `/etc/systemd/system/traptunnel-sender.service`：

```ini
[Unit]
Description=TrapTunnel Sender Service
After=network.target

[Service]
Type=simple
# 请修改为实际路径
ExecStart=/opt/traptunnel/sender -c /opt/traptunnel/sender.conf
# 发送 SIGHUP 信号以重载配置
ExecReload=/bin/kill -HUP $MAINPID
Restart=always
RestartSec=5s

[Install]
WantedBy=multi-user.target
```

**管理命令：**
```bash
# 启动服务
systemctl start traptunnel-sender

# 修改配置文件后，热重载配置（不重启进程）
systemctl reload traptunnel-sender

# 查看状态
systemctl status traptunnel-sender
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
