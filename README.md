# agmux - Agent Multiplexer

agmux 是一个为 AI Agent 设计的 SSH/本地会话管理和命令执行工具。借鉴 tmux 的 client-server 架构，支持命名会话、detach/attach 语义、结构化输出和自动重连。

**设计原则：完全非交互、纯脚本化。所有参数通过命令行传递，不依赖 TTY。**

## 特性

- 命名会话管理（SSH + 本地）
- detach/attach 语义：detach 保持 SSH 连接存活，kill 才真正关闭
- 本地命令执行（无需 SSH）
- 结构化输出：stdout/stderr/exit_code 分离返回
- TCP 端口转发（本地/远程）
- 断线自动重连（指数退避：5s → 10s → 20s → ... → max 5min）
- SFTP 文件传输（上传/下载/同步）
- Sudo 安全执行（`sudo -S` + stdin，无 shell 注入风险）
- Unix Socket 0600 认证（与 tmux 相同模型）
- 审计日志（`~/.agmux/audit.log`）
- 状态持久化（daemon 重启可恢复会话）
- 被连接机器零配置（仅需标准 SSH 服务）

## 架构

```
┌──────────────┐  Unix Socket (0600)  ┌──────────────────┐
│  agmux CLI   │ ──────────────────── │  agmux-server    │
│  (薄客户端)  │  imsg 二进制协议      │  (常驻进程)       │
└──────────────┘                       │                  │
                                       │  Session Manager │
                                       │  ┌──────────────┤
                                       │  │ SSH Session  │────── SSH tunnel
                                       │  │ Local Session│────── local exec
                                       │  └──────────────┤
                                       │  Services:      │
                                       │  ExecService    │
                                       │  ForwardService │
                                       │  TransferService│
                                       │  ReconnectMon   │
                                       │  AuditLogger    │
                                       └──────────────────┘
```

## 安装

### 从源码构建

```bash
git clone https://github.com/forechoandlook/agmux.git
cd agmux
make build
make install
```

## 启动服务

```bash
# 启动 daemon
agmux start

# 或手动启动
agmux-server &
```

## 使用方法

### 连接 SSH

```bash
# 密码认证
agmux connect -u admin1 -h 192.168.1.1 -p 7080 -P "your-password"

# 密钥认证
agmux connect -u admin1 -h example.com -i ~/.ssh/id_ed25519

# 指定会话名称
agmux connect -u admin -h 10.0.1.1 -n production -P password
```

### 创建本地会话

```bash
# 创建本地执行上下文
agmux local

# 指定名称
agmux local -n dev
```

### 执行命令

```bash
# 在默认会话执行命令（SSH 或本地）
agmux exec "ls -la"

# 指定会话执行
agmux exec -n production "pwd"

# 带超时（秒）
agmux exec -t 10 "sleep 5"

# sudo 命令（安全方式，无 shell 注入）
agmux exec --sudo --sudo-password "1234" "systemctl restart nginx"

# sudo 以其他用户运行
agmux exec --sudo --sudo-password "1234" --sudo-user "www-data" "whoami"

# sudo login shell
agmux exec --sudo --sudo-password "1234" --sudo-login "whoami"

# 一次性本地执行（无需会话）
agmux run "ls -la /tmp"
agmux run --sudo --sudo-password "1234" "cat /etc/shadow"
```

### 会话管理

```bash
# 列出所有会话
agmux list

# 切换默认会话
agmux use production

# 脱离会话（SSH 连接保持存活，端口转发继续工作）
agmux detach -n production

# 重新附着到会话
agmux attach -n production

# 重新附着并提供密码（用于 daemon 重启后的离线会话）
agmux attach -n production -P password

# 关闭会话（真正关闭 SSH 连接）
agmux kill production
```

### 端口转发

```bash
# 本地端口转发：本地 8080 -> 远程 80
agmux forward -n production -l 8080 -r 80

# 远程端口转发：远程 9000 -> 本地 3000
agmux forward -n production -R -l 3000 -r 9000

# 列出所有转发
agmux forwards

# 关闭转发
agmux forward-close <forward_id>
```

### 文件传输

```bash
# 上传文件或文件夹
agmux scp -n production -put /path/to/local/file /path/to/remote/file
agmux scp -n production -put /path/to/local/dir /path/to/remote/dir

# 下载文件或文件夹
agmux scp -n production -get /path/to/remote/file /path/to/local/file

# 列出远程目录
agmux sftp -n production -c ls -p /path/to/remote/dir

# 创建远程目录
agmux sftp -n production -c mkdir -p /path/to/remote/newdir

# 删除远程文件
agmux sftp -n production -c rm -p /path/to/remote/file.txt
```

### 其他

```bash
# 检查 daemon 是否运行
agmux ping

# 停止 daemon（优雅关闭：保存状态 → 关闭连接 → 清理）
agmux stop

# 强制重连
agmux reconnect -n production

# 查看版本
agmux -v
```

## 命令行选项

| 选项 | 说明 |
|------|------|
| `-S socket_path` | Unix socket 路径（默认：`/tmp/agmux.sock`） |
| `-n name` | 会话名称 |
| `-u user` | 用户名 |
| `-h host` | 主机地址 |
| `-p port` | SSH 端口（默认：22） |
| `-P password` | SSH 密码 |
| `-i key_path` | SSH 密钥路径 |
| `-t timeout` | 命令超时时间（秒） |
| `--sudo` | 启用 sudo |
| `--sudo-password` | sudo 密码 |
| `--sudo-user` | sudo 以指定用户运行 |
| `--sudo-login` | sudo login shell（-i） |
| `-l local` | 本地端口 |
| `-r remote` | 远程端口 |
| `-R` | 远程端口转发 |
| `-put` | 上传模式 |
| `-get` | 下载模式 |
| `-c command` | SFTP 命令（ls/mkdir/rm） |

## 项目结构

```
agmux/
├── cmd/
│   ├── agmux/main.go              # CLI 客户端
│   └── agmux-server/main.go       # Daemon 入口
├── internal/
│   ├── protocol/                   # 消息类型 + 请求/响应结构体
│   ├── ssh/                        # SSH 客户端 + TOFU + 认证
│   ├── session/                    # 命名会话 + detach/attach/kill
│   ├── exec/                       # 命令执行（SSH + 本地 + sudo）
│   ├── portforward/                # 端口转发 + 生命周期管理
│   ├── transfer/                   # SFTP 文件传输
│   ├── reconnect/                  # 指数退避重连监控
│   ├── persist/                    # 状态持久化
│   ├── audit/                      # 审计日志
│   └── server/                     # Unix socket 服务端 + 路由分发
├── pkg/
│   └── imsg/                       # 二进制帧协议实现
├── go.mod
├── Makefile
├── DESIGN.md
└── README.md
```

## 限制

- **不支持交互式命令**：vim、less、top 等需要 TTY 的程序无法使用
- **密码暴露**：密码通过命令行传递，在 `ps aux` 中可能可见（与 SSH 原生行为一致）
- **daemon 重启后 SSH 会话需重新认证**：密码不持久化到磁盘，agent 需重新 `attach -P password`

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
