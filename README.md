# gssh - SSH Session Multiplexer for Agents

gssh 是一个为 AI Agent 设计的 SSH/本地会话管理和命令执行工具。借鉴 tmux 的 client-server 架构（单一二进制，daemon 按需自启），**CLI 用法与 ssh/scp 对齐**：`gssh user@host -p 7080` 连接、`gssh user@host "cmd"` 直接执行、`gssh scp file session:/path` 传文件。默认使用 ssh-agent 和 `~/.ssh` 默认密钥认证。

**设计原则：完全非交互、纯脚本化。所有参数通过命令行传递，不依赖 TTY。**

## 为什么 Agent 应该用 gssh 而不是直接 SSH？

Agent（AI agent、自动化脚本）通过 gssh 执行远程命令比直接调用 `ssh` 有本质性优势：

| 比较项 | 直接 SSH | gssh |
| --- | --- | --- |
| **用法** | `ssh user@host "cmd"` | 完全一致：`gssh user@host "cmd"`，零学习成本 |
| **连接持久性** | 每次执行都新建连接，耗时 + 资源浪费 | daemon 保持长连接，重复执行复用同一 SSH 通道 |
| **daemon 管理** | — | 无需手动启动：任何命令需要时自动拉起 daemon，崩溃后自动恢复 |
| **断线恢复** | 网络抖动即失败，agent 必须手动重试 | 自动指数退避重连（5s→10s→...→5min），端口转发同步恢复 |
| **结构化输出** | stdout/stderr 混合输出，exit code 需解析 | 非流式默认返回 `{"stdout","stderr","exit_code"}` JSON，零歧义解析；`--raw` 可切回原始输出 |
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

- **CLI 与 ssh/scp 对齐**：`gssh user@host`、`gssh user@host "cmd"`、`gssh scp src session:/dst`
- **默认密钥认证**：自动尝试 ssh-agent + `~/.ssh/id_ed25519` 等默认密钥，与 ssh 一致
- 命名会话管理（SSH + 本地）
- 单一二进制：daemon 作为 `gssh server` 子命令，按需自动启动，无需手动管理
- 会话切换与重连：`use` 切换默认会话，自动恢复断开的 SSH 连接
- 流式命令输出：实时推送 stdout/stderr（`--stream`）
- 结构化输出：非流式默认返回 `{"stdout","stderr","exit_code"}` JSON（`--raw` 切回原始输出）
- TCP 端口转发（`-L`/`-R` 与 ssh 语义一致），forward ID 支持前缀匹配
- 断线自动重连（指数退避：5s → 10s → 20s → ... → max 5min）
- SFTP 文件传输（scp 风格语法）
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

### 连接 SSH（ssh 风格）

```bash
# 默认密钥认证（ssh-agent → ~/.ssh/id_ed25519 等，与 ssh 行为一致）
gssh admin1@192.168.1.1
gssh admin1@example.com -p 7080

# 指定密钥
gssh admin1@example.com -i ~/.ssh/id_ed25519

# 密码认证
gssh admin1@192.168.1.1 -p 7080 --pswd "your-password"

# 指定会话名称（默认会话名为 user@host）
gssh admin@10.0.1.1 -n production

# 显式子命令形式（等价）
gssh connect admin1@192.168.1.1 -p 7080 --pswd "your-password"
```

认证顺序：显式 `--pswd` 优先 → `-i` 指定密钥 → ssh-agent（`SSH_AUTH_SOCK`）→ `~/.ssh/id_ed25519`、`id_ecdsa`、`id_rsa`。带密码短语的密钥请先 `ssh-add` 到 agent（非交互设计不支持输入短语）。

### 直接执行（ssh 风格）

```bash
# 连接（如需要）并执行，重复执行复用同一会话连接
# 非流式默认输出 JSON：{"stdout","stderr","exit_code"}
gssh admin@10.0.1.1 "df -h"
gssh admin@10.0.1.1 -p 7080 "uptime"

# 原始输出（给人看或接管道）
gssh admin@10.0.1.1 --raw "tail -20 /var/log/syslog"

# 流式输出
gssh admin@10.0.1.1 --stream "tail -f /var/log/syslog"
```

### 创建本地会话

```bash
gssh local
gssh local -n dev
```

### 在会话中执行命令

```bash
# 在默认会话执行命令（SSH 或本地）；默认输出 JSON：{"stdout","stderr","exit_code"}
gssh exec "ls -la"

# 指定会话执行
gssh exec -n production "pwd"

# 带超时（秒）
gssh exec -t 10 "sleep 5"

# 原始输出（stdout/stderr 直接打印，适合给人看或接管道）
gssh exec --raw -n production "uname -a"

# 流式输出（实时推送 stdout/stderr，适合长耗时命令）
gssh exec --stream -n production "python train.py"

# sudo 命令（安全方式，无 shell 注入）
gssh exec --sudo --pswd "1234" "systemctl restart nginx"

# sudo 以其他用户运行
gssh exec --sudo --pswd "1234" --sudo-user "www-data" "whoami"

# sudo login shell
gssh exec --sudo --pswd "1234" --sudo-login "whoami"

# 一次性本地执行（无需会话）
gssh run "ls -la /tmp"
gssh run --sudo --pswd "1234" "cat /etc/shadow"
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
gssh use production --pswd password
gssh use production -i ~/.ssh/id_ed25519

# 关闭会话（真正关闭 SSH 连接）
gssh kill production
```

### 端口转发（ssh 风格 -L/-R）

```bash
# 本地端口转发：本地 8080 -> 远程 80（ssh -L 语义：localPort:remotePort）
gssh forward -n production -L 8080:80

# 远程端口转发：远程 9000 -> 本地 3000（ssh -R 语义：remotePort:localPort）
gssh forward -n production -R 9000:3000

# 显式暴露到远端所有网卡（默认绑定 127.0.0.1）
gssh forward -n production -R 9000:3000 --public

# 列出所有转发
gssh forwards
gssh forwards --json

# 关闭转发（支持完整 ID 或唯一前缀，列表中显示的 8 位前缀即可）
gssh forward-close <forward_id>
```

### 文件传输（scp 风格）

```bash
# 上传文件或文件夹：gssh scp <本地路径> <会话名:远程路径>
gssh scp ./app.zip production:/opt/
gssh scp ./build production:/srv/app

# 下载文件或文件夹
gssh scp production:/var/log/app.log ./logs/

# 增量同步（目录传输自动跳过 size+mtime 相同的文件；sync 是 scp 的别名）
gssh sync ./dist production:/srv/app

# 上传到远程 home 目录（省略路径）
gssh scp ./notes.txt production:

# 注意：下载时远程路径必须显式指定（gssh scp production: ./dst 会被拒绝，
# 防止误把整个远程 home 目录递归拉下来）

# SFTP 操作（位置参数）
gssh sftp -n production ls /var/log
gssh sftp -n production mkdir /opt/newdir
gssh sftp -n production rm /tmp/old.log
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

## 命令速查

| 命令 | 说明 |
| --- | --- |
| `gssh user@host [-p port] [-i key] [--pswd pw] [-n name]` | 连接（创建/复用会话） |
| `gssh user@host [flags] "command"` | 连接并执行（复用会话） |
| `gssh exec [-n name] [-t sec] [--stream] [--raw] [--sudo ...] "cmd"` | 会话内执行（默认 JSON 输出） |
| `gssh run [flags] "cmd"` | 一次性本地执行 |
| `gssh local [-n name]` | 创建本地会话 |
| `gssh list [--json]` | 列出会话 |
| `gssh use <name> [--pswd pw] [-i key]` | 切换默认 / 重连会话 |
| `gssh kill <name>` | 关闭会话 |
| `gssh forward [-n name] -L l:r` | 本地转发 |
| `gssh forward [-n name] -R r:l [--public]` | 远程转发 |
| `gssh forwards [--json]` | 列出转发 |
| `gssh forward-close <id>` | 关闭转发（支持前缀） |
| `gssh scp <src> <dst>` | 传输（一侧为 `session:path`） |
| `gssh sftp [-n name] ls\|mkdir\|rm <path>` | SFTP 操作 |
| `gssh ping` / `gssh stop` | daemon 健康检查 / 停止 |
| `-S <socket_path>` | 覆盖 socket 路径（默认：`$XDG_RUNTIME_DIR/gssh/gssh.sock` 或 `~/.gssh/run/gssh.sock`） |

## 项目结构

```text
gssh/
├── cmd/
│   └── gssh/
│       ├── main.go           # 入口 + 命令分发（含 user@host 简写）+ usage
│       ├── client.go         # RPC 客户端 + daemon 自动启动
│       ├── server.go         # `gssh server` 子命令（daemon 前台运行）
│       ├── commands.go       # 各子命令的参数解析与处理
│       ├── dest.go           # ssh 风格目标/转发/scp 端点解析
│       ├── spawn_unix.go     # daemon 进程脱离终端（setsid）
│       ├── spawn_other.go
│       └── peercred_*.go     # 对端凭据校验（darwin/linux/其他）
├── internal/
│   ├── protocol/              # 消息类型常量 + 请求/响应结构体
│   ├── ssh/                   # SSH 客户端 + agent/默认密钥认证 + known_hosts
│   ├── session/               # 命名会话（SSH/本地）+ Manager
│   ├── exec/                  # 命令执行（SSH + 本地 + sudo + stream）
│   ├── shellquote/            # POSIX shell 引号转义
│   ├── portforward/           # 端口转发 + 生命周期管理
│   ├── transfer/              # SFTP 文件传输（+ safefile_* 平台加固）
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
- **密码暴露**：`--pswd` 通过命令行传递，在 `ps aux` 中可能可见（与 SSH 原生行为一致；优先使用密钥）
- **密码短语密钥**：不支持交互输入 passphrase，请先 `ssh-add` 到 ssh-agent
- **daemon 重启后密码会话需重新认证**：密码不持久化到磁盘，需重新 `use <name> --pswd password`；密钥会话（显式 `-i` 或默认密钥/agent）会自动重连
- **缓冲输出上限 4MB**：超过时请使用 `--stream`
- **首次主机密钥默认不自动信任**：需预先写入 `known_hosts`；仅在显式设置 `GSSH_INSECURE_ACCEPT_NEW_HOST_KEYS=1` 时启用自动接收

## 从 v0.2 迁移

v0.3 将 CLI 与 ssh 对齐，存在破坏性变更：

| v0.2 | v0.3 |
| --- | --- |
| `gssh connect -u u -h host -P 7080 -p pw` | `gssh u@host -p 7080 --pswd pw` |
| `gssh connect -i key`（必须显式指定） | 默认自动尝试 agent + 默认密钥 |
| `gssh use name -p pw` | `gssh use name --pswd pw` |
| `gssh scp -n s -put a b` | `gssh scp a s:b` |
| `gssh scp -n s -get a b` | `gssh scp s:a b` |
| `gssh sftp -n s -c ls -p /x` | `gssh sftp -n s ls /x` |
| `gssh forward -n s -l 8080 -r 80` | `gssh forward -n s -L 8080:80` |
| `gssh forward -n s -R -l 3000 -r 9000` | `gssh forward -n s -R 9000:3000` |
| `gssh exec --json "cmd"` | 默认即 JSON（`--json` 保留兼容）；需要原始输出用 `--raw` |

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
