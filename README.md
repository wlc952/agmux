# gssh - SSH Session Multiplexer for Agents

gssh 是一个为 AI Agent 设计的 SSH/本地会话管理和命令执行工具。借鉴 tmux 的 client-server 架构（单一二进制，daemon 按需自启），支持命名会话、会话切换与重连、结构化输出和自动重连。

**设计原则：完全非交互、纯脚本化。所有参数通过命令行传递，不依赖 TTY。**

## 为什么 Agent 应该用 gssh 而不是直接 SSH？

Agent（AI agent、自动化脚本）通过 gssh 执行远程命令比直接调用 `ssh` 有本质性优势：

| 比较项 | 直接 SSH | gssh |
| --- | --- | --- |
| **连接持久性** | 每次执行都新建连接，耗时 + 资源浪费 | daemon 保持长连接，多命令复用同一 SSH 通道 |
| **daemon 管理** | — | 无需手动启动：任何命令需要时自动拉起 daemon，崩溃后自动恢复 |
| **断线恢复** | 网络抖动即失败，agent 必须手动重试 | 自动指数退避重连（5s→10s→...→5min），端口转发同步恢复 |
| **结构化输出** | stdout/stderr 混合输出，exit code 需解析 | `--json` 返回 `{"stdout","stderr","exit_code"}`，零歧义解析 |
| **流式输出** | 无结构化流式能力 | `--stream` 实时推送 stdout/stderr chunk，带帧边界标识 |
| **会话命名** | 无 | 命名会话（`production`、`dev`），多主机一目了然 |
| **会话切换与重连** | 无 | `use` 切换默认会话，自动恢复断开的 SSH 连接 |
| **sudo 安全** | 密码拼接到 shell 命令 → shell 注入风险 | `sudo -S` + stdin 写入密码，零 shell 注入 |
| **超时控制** | 依赖 SSH 配置或手动 timeout | 内置 `-t timeout`，超时 SIGKILL + 返回 exit_code -1 |
| **审计日志** | 无 | 所有操作自动记录到 `~/.gssh/audit.log`（JSON 格式） |
| **状态持久化** | 无 | daemon 重启后恢复会话，密钥会话自动重连 |
| **端口转发** | 需保持 SSH 进程存活 | 断线后转发继续工作，重连后转发自动恢复 |
| **TTY 依赖** | 很多 SSH 操作隐式依赖 PTY | 完全非交互，不需要 TTY，适合 agent 程序化调用 |
| **远端零配置** | — | 远端仅需标准 SSH 服务，无需安装任何额外软件 |

**核心要点**：gssh 把"SSH 连接"从"每条命令的生命周期"中解耦出来——连接由 daemon 持久管理，命令在已有连接上执行。这让 agent 不再需要处理连接建立、断线重试、输出解析等底层问题。

## 特性

- 命名会话管理（SSH + 本地）
- 单一二进制：daemon 作为 `gssh server` 子命令，按需自动启动，无需手动管理
- 会话切换与重连：`use` 切换默认会话，自动恢复断开的 SSH 连接
- 本地命令执行（无需 SSH）
- 流式命令输出：实时推送 stdout/stderr（`--stream`）
- 结构化输出：`--json` 返回 stdout/stderr/exit_code 的 JSON 对象
- TCP 端口转发（本地/远程），forward ID 支持前缀匹配
- 断线自动重连（指数退避：5s → 10s → 20s → ... → max 5min）
- SFTP 文件传输（上传/下载/同步）
- Sudo 安全执行（`sudo -S` + stdin，无 shell 注入风险）
- Unix Socket 0600 认证 + 对端凭据校验（与 tmux 相同模型）
- 审计日志（`~/.gssh/audit.log`）
- 状态持久化（daemon 重启恢复会话，密钥会话自动重连）
- 被连接机器零配置（仅需标准 SSH 服务）

## 架构

```text
+------------------+     Unix Socket (0600) / imsg     +----------------------+
| gssh <command>   | <--------------------------------> | gssh server (daemon) |
| thin client      |     按需自动启动 daemon             |                      |
| (同一二进制)      |                                   |  Session Manager     |
+------------------+                                     |    |- SSH Session ---+--> SSH tunnel
                                                         |    |- Local Session -+--> local exec
                                                         |                      |
                                                         |  Services            |
                                                         |    - ExecService     |
                                                         |    - ForwardService  |
                                                         |    - TransferService |
                                                         |    - ReconnectMon    |
                                                         |    - AuditLogger     |
                                                         +----------------------+
```

## 安装

### 从源码构建

```bash
git clone https://github.com/wlc952/gssh.git
cd gssh
make build
make install
```

## 启动服务

**通常不需要手动启动**：任何需要 daemon 的命令（connect/exec/forward 等）会在 daemon 未运行时自动启动它（包括 socket 残留导致的崩溃恢复）。

```bash
# 显式启动（可选）
gssh start

# 前台运行（调试用，日志输出到 stderr）
gssh server

# daemon 日志文件
tail -f ~/.gssh/server.log
```

## 使用方法

### 连接 SSH

```bash
# 密码认证
gssh connect -u admin1 -h 192.168.1.1 -P 7080 -p "your-password"

# 密钥认证
gssh connect -u admin1 -h example.com -i ~/.ssh/id_ed25519

# 指定会话名称
gssh connect -u admin -h 10.0.1.1 -n production -p password
```

### 创建本地会话

```bash
# 创建本地执行上下文
gssh local

# 指定名称
gssh local -n dev
```

### 执行命令

```bash
# 在默认会话执行命令（SSH 或本地）
gssh exec "ls -la"

# 指定会话执行
gssh exec -n production "pwd"

# 带超时（秒）
gssh exec -t 10 "sleep 5"

# JSON 结构化输出（{"stdout","stderr","exit_code"}，适合程序解析）
gssh exec --json -n production "uname -a"

# 流式输出（实时推送 stdout/stderr，适合长耗时命令）
gssh exec --stream "tail -f /var/log/syslog"
gssh exec --stream -n production "python train.py"

# sudo 命令（安全方式，无 shell 注入）
gssh exec --sudo --sudo-password "1234" "systemctl restart nginx"

# sudo 以其他用户运行
gssh exec --sudo --sudo-password "1234" --sudo-user "www-data" "whoami"

# sudo login shell
gssh exec --sudo --sudo-password "1234" --sudo-login "whoami"

# 一次性本地执行（无需会话）
gssh run "ls -la /tmp"
gssh run --sudo --sudo-password "1234" "cat /etc/shadow"

# 一次性本地执行 + 流式输出
gssh run --stream "docker build -t app ."
```

### 流式输出详解

`--stream` 模式适用于长耗时命令（日志监控、训练任务、构建过程等），stdout/stderr 实时逐块推送，而非等待命令结束才返回全部输出：

- 每个输出块标识来源（`stdout` 或 `stderr`），不会混淆
- 命令结束时发送 `StreamEnd`，包含 `exit_code` 和可能的 `error`
- 输出块之间有帧边界，不存在解析歧义
- 缓冲模式（非 `--stream`）的响应上限为 4MB；输出可能超过时请使用 `--stream`

### 会话管理

```bash
# 列出所有会话
gssh list
gssh list --json

# 切换默认会话（已连接的会话仅切换默认）
gssh use production

# 切换并恢复断开的会话（提供认证凭据）
gssh use production -p password
gssh use production -i ~/.ssh/id_ed25519

# 关闭会话（真正关闭 SSH 连接）
gssh kill production
```

### 端口转发

```bash
# 本地端口转发：本地 8080 -> 远程 80
gssh forward -n production -l 8080 -r 80

# 远程端口转发：远程 9000 -> 本地 3000
gssh forward -n production -R -l 3000 -r 9000

# 显式暴露到远端所有网卡（默认不开放）
gssh forward -n production -R -l 3000 -r 9000 --public

# 列出所有转发
gssh forwards
gssh forwards --json

# 关闭转发（支持完整 ID 或唯一前缀，列表中显示的 8 位前缀即可）
gssh forward-close <forward_id>
```

### 文件传输

```bash
# 上传文件或文件夹
gssh scp -n production -put /path/to/local/file /path/to/remote/file
gssh scp -n production -put /path/to/local/dir /path/to/remote/dir

# 下载文件或文件夹
gssh scp -n production -get /path/to/remote/file /path/to/local/file

# 列出远程目录
gssh sftp -n production -c ls -p /path/to/remote/dir

# 创建远程目录
gssh sftp -n production -c mkdir -p /path/to/remote/newdir

# 删除远程文件
gssh sftp -n production -c rm -p /path/to/remote/file.txt
```

### 其他

```bash
# 检查 daemon 是否运行
gssh ping

# 停止 daemon（优雅关闭：保存状态 → 关闭连接 → 清理）
gssh stop

# 查看版本
gssh -v
```

## 命令行选项

| 选项 | 说明 |
| --- | --- |
| `-S socket_path` | Unix socket 路径（默认：`$XDG_RUNTIME_DIR/gssh/gssh.sock`，否则 `~/.gssh/run/gssh.sock`） |
| `-n name` | 会话名称 |
| `-u user` | 用户名 |
| `-h host` | 主机地址 |
| `-P port` | SSH 端口（默认：22） |
| `-p password` | SSH 密码（sftp 子命令中为远程路径） |
| `-i key_path` | SSH 密钥路径 |
| `-t timeout` | 命令超时时间（秒） |
| `--stream` | 流式输出（实时推送 stdout/stderr，适合长耗时命令） |
| `--json` | JSON 结构化输出（exec/run/list/forwards） |
| `--sudo` | 启用 sudo |
| `--sudo-password` | sudo 密码 |
| `--sudo-user` | sudo 以指定用户运行 |
| `--sudo-login` | sudo login shell（-i） |
| `-l local` | 本地端口 |
| `-r remote` | 远程端口 |
| `-R` | 远程端口转发 |
| `--bind` | 远程转发监听地址（仅 `-R`，默认：`127.0.0.1`） |
| `--public` | 远程转发监听 `0.0.0.0`（仅 `-R`） |
| `-put` | 上传模式 |
| `-get` | 下载模式 |
| `-c command` | SFTP 命令（ls/mkdir/rm） |

## 项目结构

```text
gssh/
├── cmd/
│   └── gssh/
│       ├── main.go           # 入口 + 命令分发 + usage
│       ├── client.go         # RPC 客户端 + daemon 自动启动
│       ├── server.go         # `gssh server` 子命令（daemon 前台运行）
│       ├── commands.go       # 各子命令的参数解析与处理
│       ├── spawn_unix.go     # daemon 进程脱离终端（setsid）
│       ├── spawn_other.go
│       └── peercred_*.go     # 对端凭据校验（darwin/linux/其他）
├── internal/
│   ├── protocol/              # 消息类型常量 + 请求/响应结构体
│   ├── ssh/                   # SSH 客户端 + known_hosts 校验 + 认证
│   ├── session/               # 命名会话（SSH/本地）+ Manager
│   ├── exec/                  # 命令执行（SSH + 本地 + sudo + stream）
│   ├── shellquote/            # POSIX shell 引号转义
│   ├── portforward/           # 端口转发 + 生命周期管理
│   ├── transfer/              # SFTP 文件传输
│   ├── reconnect/             # 指数退避重连监控
│   ├── persist/               # 状态持久化（~/.gssh/state.json）
│   ├── audit/                 # 审计日志（~/.gssh/audit.log）
│   ├── socketpath/            # socket 路径解析 + 归属/权限校验
│   └── server/                # Unix socket 服务端 + 路由分发
├── pkg/
│   └── imsg/                  # 二进制帧协议实现
├── go.mod
├── Makefile
├── DESIGN.md
└── README.md
```

## 限制

- **不支持交互式命令**：vim、less、top 等需要 TTY 的程序无法使用
- **密码暴露**：密码通过命令行传递，在 `ps aux` 中可能可见（与 SSH 原生行为一致）
- **daemon 重启后密码会话需重新认证**：密码不持久化到磁盘，agent 需重新 `use <name> -p password`；密钥会话会自动重连
- **缓冲输出上限 4MB**：超过时请使用 `--stream`
- **首次主机密钥默认不自动信任**：需预先写入 `known_hosts`；仅在显式设置 `GSSH_INSECURE_ACCEPT_NEW_HOST_KEYS=1` 时启用自动接收

## 开发

```bash
# 构建
make build

# 测试
make test

# 清理
make clean
```

## License

MIT
