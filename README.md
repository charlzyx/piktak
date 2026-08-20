# PIK.TAK

## Pik. Tak. Paired.

PIK.TAK 是一套连接远程 Client 与本地 Service 的 Relay 和传输协议。本地 Machine 主动连出，不需要公网 IP，也不用做端口转发。

[English](README.en.md) · [网站](https://piktak.pages.dev) · [协议](docs/protocol.md) · [Cloudflare 部署](piktak-cf/README.md) · [v0.1.0](https://github.com/charlzyx/piktak/releases/tag/v0.1.0)

> [!WARNING]
> PIK.TAK 目前没有完整的公网安全方案。Cloudflare 模式使用共享机器码接入 Host，流量会经过 Cloudflare，也没有端到端加密。**公开访问的每个 Service 域名都必须配置 Cloudflare Access。没有 Access 时，不要接入 DSH、Agent、管理面板或其他真实服务。**

## 架构

```text
Phone / Browser / Client
           │
           ▼
      PIK.TAK Relay
 Cloudflare Worker / Go
           │ outbound connection
           ▼
        Machine
           │
        piktakd
           │ Bridge
           ▼
 DSH · PiAgent · local Service
```

### 角色

| 名称 | 含义 |
| --- | --- |
| **Client** | 发起访问的一端，例如手机浏览器或原生客户端 |
| **Relay** | 让 Client 找到 Host，并转发控制消息和数据 |
| **Machine** | 用户看到的本地电脑或设备 |
| **Host** | Machine 上主动连接 Relay 的协议角色 |
| **Bridge** | 把现有 localhost 服务接入 Host，无需修改原应用 |
| **Service** | DSH、PiAgent、开发面板或其他本地 HTTP、WebSocket、TCP 服务 |

### 数据路径

```text
Client → Relay → Host (piktakd) → Bridge → Service
```

Relay 不等于云服务。PIK.TAK 提供 Cloudflare Worker 和 Go 两种 Relay 实现，你需要将其中一种部署到自己的账号或服务器。

## 组件

| 组件 | 路径 | 用途 |
| --- | --- | --- |
| `piktakd` | `cmd/piktakd` | Machine 后台程序；实现 Host、维持 Relay 连接、桥接本地 Service、提供状态页 |
| `piktak-relay` | `cmd/piktak-relay` | 自建 Go Relay；提供控制端口和透明 TCP ingress |
| Cloudflare Relay | `piktak-cf/worker` | Worker + Durable Object；面向 HTTP、流式响应和浏览器 WebSocket |
| Native examples | `examples/echo-host`、`examples/echo-client` | 原生 Host/Client 协议示例，不作为用户命令发布 |
| Protocol | `internal/wire`、`internal/l0`、`internal/l2`、`internal/l3` | Framing、配对、能力协商和 Adapter |

## `piktakd`

`piktakd` 是普通用户需要安装的唯一 Machine 端程序。它可以在一个进程里注册多个 Service：

```yaml
relay: wss://relay.example.com/host
code: replace-with-a-random-code
status: true
status_addr: 127.0.0.1:8765

services:
  - name: dsh
    protocol: ws
    local: 127.0.0.1:3080
    rewrite_host: true

  - name: dashboard
    protocol: ws
    local: 127.0.0.1:8080
```

字段：

- `relay`：Machine 主动连接的 Relay 地址；
- `code`：当前版本使用的共享机器码；
- `name`：Service 的路由键，通常与浏览器域名的第一个 label 对应；
- `protocol`：`ws` 使用 Cloudflare Relay，`tcp` 使用 Go Relay；
- `local`：本地目标地址；
- `rewrite_host`：为信任 loopback 或同源请求的应用重写 Host/Origin；
- `status_addr`：只监听 loopback 的状态页和 JSON API。

运行：

```sh
piktakd -config ~/.config/piktak/config.yml
piktakd help -config ~/.config/piktak/config.yml
```

状态：

```text
http://127.0.0.1:8765/
http://127.0.0.1:8765/api/status
```

状态 API 只返回是否已配置配对信息，不返回机器码本身。

## Relay

### Cloudflare Relay

Cloudflare Relay 适合手机和浏览器访问本地 Web 服务：

- Worker 接收 HTTP 和 WebSocket；
- Durable Object 按 Service name 保存当前 Host 会话；
- `piktakd` 通过出站 WebSocket 连接 `/host`；
- 浏览器入口必须由 Cloudflare Access 保护；
- 不需要额外 VPS。

#### 一键部署

[![Deploy to Cloudflare Workers](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/charlzyx/piktak/tree/main/piktak-cf/worker)

按钮会在你的账号中创建 Worker 和 Durable Object。部署后还必须手动完成：

1. 生成随机机器码并保存为 Worker Secret `PIKTAK_CODES`；
2. 为 `/host` 配置 Machine 使用的域名或 Access bypass 规则；
3. 为每个浏览器 Service 绑定独立域名；
4. **为每个浏览器 Service 域名创建 Cloudflare Access Application 和 Allow Policy；**
5. 确认 Access 生效后，将 `PIKTAK_REQUIRE_ACCESS` 设为 `1` 并重新部署；
6. 未登录访问 Service 域名必须跳转到 Access；直接访问 Worker 浏览器路径必须返回 `401`。

完整步骤见 [`piktak-cf/README.md`](piktak-cf/README.md)。

也可以手动部署：

```sh
cd piktak-cf/worker
npm install
npm run typecheck
openssl rand -hex 16 | npx wrangler secret put PIKTAK_CODES
npx wrangler deploy
```

### Go Relay

Go Relay 是自建和协议参考实现，适合透明 TCP：

```text
Client / ingress → piktak-relay → piktakd → local Service
```

三分钟本地测试：

```sh
make run-relay       # control :7681 / ingress :7682
make run-http        # example Service on 127.0.0.1:7531
make run-bridge      # start piktakd
make run-curl        # request through the Relay
```

> [!CAUTION]
> Go Relay 的 raw ingress 当前没有逐连接认证。不要直接开放到公网；必须使用防火墙、私网或 IP 白名单限制来源。

## 安装

### Homebrew

```sh
brew tap charlzyx/piktak https://github.com/charlzyx/piktak
brew trust --formula charlzyx/piktak/piktak
brew install charlzyx/piktak/piktak
brew services start piktak
```

Formula 安装 `piktakd`，服务读取 `~/.config/piktak/config.yml`。如果 GitHub Release 下载较慢，可在运行 Homebrew 时设置 `HTTPS_PROXY`、`HTTP_PROXY` 和 `ALL_PROXY`。

### Release assets

[`v0.1.0`](https://github.com/charlzyx/piktak/releases/tag/v0.1.0) 提供 Linux/macOS 的 amd64 和 arm64 静态二进制。Release CI 会计算 SHA-256，并自动更新 `Formula/piktak.rb`。

## 当前能力

- 一个 `piktakd` 进程配置多个 Service；
- HTTP、流式响应和浏览器 WebSocket；
- Cloudflare Worker + Durable Object 路由；
- Go Relay 透明 TCP；
- 机器码白名单、自动重连和协议版本检查；
- Host/Origin loopback 兼容；
- 本机状态页和脱敏 JSON API；
- Linux/macOS amd64/arm64 Release 与 Homebrew Formula。

## 协议

```text
L0  identity and pairing
L1  discovery and transport
L2  capability negotiation
L3  application adapters
```

当前 L0 是共享机器码。现有 Service 通过 Bridge 接入；未来应用可以直接集成 Host/Client SDK。详见 [`docs/protocol.md`](docs/protocol.md)。

## 安全边界

- **Cloudflare Access 是公开浏览器入口的必需配置，不是可选增强。**
- 机器码只保护 Host 接入，不能替代浏览器用户认证；
- 机器码是共享静态秘密，不是成熟的设备配对；
- 不要把机器码提交到仓库、写入截图或放进公开日志；
- Cloudflare Relay 经过 Cloudflare，不是端到端加密；
- 只接入必要的 Service，并检查应用对 Host、Origin、Cookie 和 loopback 的信任规则；
- Go raw ingress 必须限制网络来源；
- 项目仍处于早期阶段。

## 项目来源

PIK.TAK 最初用于从手机访问电脑上的 DSH 和 PiAgent，同时避免让 Tailscale 一直占用手机 VPN。DSH 是第一个实际应用，但 PIK.TAK 也可以桥接其他 HTTP、WebSocket 或 TCP Service。

## Roadmap

- 一次性配对码和二维码；
- 长期身份、确认、撤销和密钥轮换；
- Machine 下的 Service 发现；
- Go / TypeScript Host 和 Client SDK；
- Relay 无法读取内容的端到端加密通道。

## License

待定。
