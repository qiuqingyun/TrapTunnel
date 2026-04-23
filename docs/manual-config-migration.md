# TrapTunnel 旧配置手工迁移指引

本文档说明如何将旧版 `sender.conf` / `receiver.conf` 手工迁移为统一的 `node.toml`。

当前策略是：

- `node` 只接受 `node.toml`
- 不兼容读取旧版 INI 配置
- 不提供自动转换工具

因此上线前需要按角色手工改写配置。

## 1. 迁移原则

- 旧 `sender.conf` 迁移为 `profile = "edge"` 或 `profile = "relay"`
- 旧 `receiver.conf` 迁移为 `profile = "sink"`
- 优先先迁移为最小等价配置，再按需要补 `export`、`inject` 修正、`tuning`
- 未使用的旧字段不要机械照搬

## 2. sender.conf -> node.toml

### 2.1 典型旧配置

```ini
[common]
node_id = 101
servers = 10.0.0.1:10000,10.0.0.2:10000

[advanced]
listen_port = 162
reconnect_interval = 5
max_buffer_size = 2000

[logging]
log_level = INFO
max_log_size = 10
max_log_backups = 100
```

### 2.2 对应的新配置

```toml
[node]
id = 101
name = "edge-101"
profile = "edge"

[capture]
listen_ports = [162]

[egress]
reconnect_interval = 5

[[egress.groups]]
members = ["10.0.0.1:10000"]

[[egress.groups]]
members = ["10.0.0.2:10000"]

[logging]
level = "INFO"
max_log_size = 10
max_log_backups = 100
```

### 2.3 字段映射

| 旧字段 | 新字段 | 说明 |
| :--- | :--- | :--- |
| `common.node_id` | `node.id` | 保持不变 |
| `advanced.listen_port` | `capture.listen_ports` | 迁移为数组 |
| `advanced.reconnect_interval` | `egress.reconnect_interval` | 单位仍为秒 |
| `common.servers` | `[[egress.groups]].members` | 若要等价替换旧 sender，通常迁成同一个 group 的有序 members |
| `logging.log_level` | `logging.level` | 名称变化 |
| `logging.max_log_size` | `logging.max_log_size` | 基本不变 |
| `logging.max_log_backups` | `logging.max_log_backups` | 基本不变 |

### 2.4 关于 `servers` 的迁移

旧 `sender` 的 `servers` 语义更接近：

- 一个有序主备列表

统一 `node` 后，推荐你先明确目标语义再迁移：

- 如果要等价替换旧 sender，就把它们放在同一个 group 的 `members` 里
- 如果你明确想升级成“多个上游都收一份”，再拆成多个 `[[egress.groups]]`

两种写法的差别：

```toml
[[egress.groups]]
members = ["10.0.0.1:10000", "10.0.0.2:10000"]
```

这表示：

- 组内 failover
- 任一时刻只发给一个可用目标

```toml
[[egress.groups]]
members = ["10.0.0.1:10000"]

[[egress.groups]]
members = ["10.0.0.2:10000"]
```

这表示：

- 组间 fanout
- 两个目标都会收到一份副本

如果你只是想等价替换旧 `sender`，通常更接近第一种。

### 2.5 关于 `max_buffer_size`

旧 `sender.conf` 里的 `advanced.max_buffer_size` 不再一对一对应新的单个字段。

在 `node.toml` 里，相关控制拆成了：

- `tuning.pipeline_buffer_size`
- `tuning.egress_group_buffer_size`

如果你不确定，第一版可以不写，使用默认值。

## 3. receiver.conf -> node.toml

### 3.1 典型旧配置

```ini
[server]
listen_port = 10000
inject_ip = 127.0.0.1

[logging]
log_level = INFO
max_log_size = 10
max_log_backups = 100

[nodes]
101 = edge-a
102 = edge-b
```

### 3.2 对应的新配置

```toml
[node]
name = "sink-gh"
profile = "sink"

[ingress]
listen = "0.0.0.0:10000"

[inject]
ip = "127.0.0.1"
port = 162
snmpv1_agent_addr_override = false

[logging]
level = "INFO"
max_log_size = 10
max_log_backups = 100
```

### 3.3 字段映射

| 旧字段 | 新字段 | 说明 |
| :--- | :--- | :--- |
| `server.listen_port` | `ingress.listen` | 需改写成 `host:port`，通常用 `0.0.0.0:<port>` |
| `server.inject_ip` | `inject.ip` | 基本不变 |
| `logging.log_level` | `logging.level` | 名称变化 |
| `logging.max_log_size` | `logging.max_log_size` | 基本不变 |
| `logging.max_log_backups` | `logging.max_log_backups` | 基本不变 |
| `[nodes]` | 无直接等价字段 | 当前 `node` 不再消费这个映射段 |

### 3.4 关于 `[nodes]`

旧 `receiver.conf` 中的 `[nodes]` 主要用于：

- `node_id -> 名称` 的展示映射

当前统一 `node` 运行时并不依赖这段配置，因此迁移时：

- 可以先删除
- 若后续仍需要展示映射，应放到上层平台或独立元数据配置中，而不是继续塞进 `node.toml`

## 4. sender -> relay 的迁移

如果原来的某台 sender 后续要承担：

- 本地采集
- 接收下游节点 TCP ingress
- 再向上游转发

那就不应迁成 `edge`，而应直接迁成 `relay`。

示例：

```toml
[node]
id = 101
name = "relay-a"
profile = "relay"

[capture]
listen_ports = [162]

[ingress]
listen = "0.0.0.0:11000"

[egress]
reconnect_interval = 5

[[egress.groups]]
members = ["10.20.0.10:10000", "10.20.0.11:10000"]

[logging]
level = "INFO"
```

## 5. 开启 inject 修正

如果目标节点是 `G/H` 这类 sink，且要兼容 `inManage` 对 SNMPv1 Trap 的源 IP 识别限制，可在迁移时直接开启：

```toml
[inject]
ip = "127.0.0.1"
port = 162
snmpv1_agent_addr_override = true
```

注意：

- 该修正只影响 inject 副本
- 不影响 egress 和 export 输出的原始 frame

## 6. 开启 export

如果目标节点需要给新程序提供原始 frame 订阅接口，可补：

```toml
[export]
enabled = true
listen = "0.0.0.0:12000"
format = "frame"
max_clients = 32
slow_client_policy = "disconnect"
```

`export` 是可选能力：

- `edge` 可开
- `relay` 可开
- `sink` 也可开

## 7. tuning 迁移建议

第一版建议：

- 不主动迁移旧系统的所有“缓存类参数”
- 先用默认值跑通
- 只有在压测或现场观测到瓶颈后，再显式调整

常见可调项：

```toml
[tuning]
pipeline_buffer_size = 1024
egress_group_buffer_size = 1024
export_client_buffer_size = 1024
max_frame_total_length = 10485760
egress_dial_timeout_ms = 5000
egress_write_timeout_ms = 5000
egress_backoff_max_ms = 30000
egress_backoff_jitter_pct = 20
```

## 8. 迁移检查清单

### 8.1 sender 迁移检查

- `node.id` 已填写
- `profile` 已明确为 `edge` 或 `relay`
- `capture.listen_ports` 已填写
- `egress.groups` 已按真实目标语义拆好
- 未误把 fanout 和 failover 搞反
- `logging.level` 已从旧名 `log_level` 改成新名 `level`

### 8.2 receiver 迁移检查

- `profile = "sink"`
- `ingress.listen` 已写成 `host:port`
- `inject.ip` / `inject.port` 已确认
- 若要兼容 SNMPv1，已显式设置 `snmpv1_agent_addr_override = true`
- 已确认不再依赖旧 `[nodes]` 映射段

### 8.3 通用检查

- 不再使用 `sender.conf` / `receiver.conf`
- 最终文件名统一为 `node.toml`
- `go run ./cmd/node -c node.toml` 或部署命令可正常加载
- 配置中若启用 `capture + inject`，已确认 `inject.port` 不与 `capture.listen_ports` 冲突

## 9. 推荐做法

推荐的实际迁移顺序：

1. 先按最小等价能力改写为 `node.toml`
2. 先在测试环境验证 `edge -> sink -> inject`
3. 再根据节点角色决定是否补 `relay`
4. 再按需要开启 `agent-addr` 修正
5. 最后再开启 `export` 和更细的 `[tuning]`

如果你在迁移过程中只想找一个可参考的最小模板，优先看这些示例：

- [examples/node-edge.toml](/home/qqy/TrapTunnel/examples/node-edge.toml)
- [examples/node-relay.toml](/home/qqy/TrapTunnel/examples/node-relay.toml)
- [examples/node-sink.toml](/home/qqy/TrapTunnel/examples/node-sink.toml)
- [examples/node-sink-agent-addr.toml](/home/qqy/TrapTunnel/examples/node-sink-agent-addr.toml)
- [examples/node-sink-export.toml](/home/qqy/TrapTunnel/examples/node-sink-export.toml)
