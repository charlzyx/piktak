# PIK.TAK

## Pik. Tak. Paired.

PIK.TAK 把手机、浏览器和远程客户端接到电脑上运行的服务。

这个项目最初是为了从手机使用本机的 DSH 和 PiAgent，同时避免让 Tailscale 一直占用手机的 VPN。现在，任何 HTTP、WebSocket 或 TCP 服务都可以通过 Cloudflare Worker 或自建 Go Relay 接入。

不需要公网 IP，不需要端口转发，手机也不需要安装客户端。

[English](README.en.md) · [网站](https://piktak.pages.dev) · [协议](docs/protocol.md) · [Cloudflare 部署](piktak-cf/README.md)

## 它怎么工作

```text
手机 / 浏览器 / Client
          │
          ▼
     PIK.TAK Relay
 Cloudflare Worker / Go
          │ 出站连接
          ▼
  你的电脑 / Machine
          │
       piktakd
          │ Bridge
          ▼
 DSH · PiAgent · 本地服务
```

用户看到的是 **Machine** 和它提供的 **Service**。协议内部则使用 **Client、Relay、Host、Bridge、Service**：

- **Client**：手机浏览器或原生客户端；
- **Relay**：帮助两端找到彼此，并转发请求和数据；
- **Host**：Machine 上主动连接 Relay 的协议角色；
- **Bridge**：让没有原生集成 PIK.TAK 的 localhost 服务接入 Host；
- **Service**：DSH、PiAgent、开发面板或其他本地服务。

`piktakd` 是运行在 Machine 上的后台程序。它实现 Host，维护 Relay 连接，并为现有服务提供 Bridge。

## 为什么做 PIK.TAK

我在电脑上运行 DSH 和 PiAgent，离开电脑后仍想从手机继续使用。Tailscale 可以解决网络连接，但如果只是打开一个本地 Web 页面，让手机一直连着 VPN，显得有些重。

第一版是一个 Go Relay，用来验证本机主动连出、远程 Client 找到 Host 并建立数据通道。后来加入 Cloudflare Worker，因为它不需要额外的 VPS，还能直接使用域名、HTTPS、Durable Object 和 Cloudflare Access。

DSH 是第一个实际应用，但不是 PIK.TAK 的边界。Agent UI、开发面板、家庭服务和其他本地应用都可以使用同一条连接路径。

## 当前可用

- `piktakd`：单个静态 Go 二进制，可接入一个或多个本地服务；
- HTTP 请求、流式响应和浏览器 WebSocket；
- Cloudflare Worker + Durable Object 路由；
- Cloudflare Access 浏览器入口；
- Go Relay 的透明 TCP 转发；
- 机器码白名单、断线重连、Host/Origin 改写；
- 本机状态页和 JSON API。

当前配对仍使用共享机器码。一次性配对码、长期设备身份、授权撤销和端到端加密还在规划中。

## 两种 Relay

### Cloudflare Worker

适合从手机或浏览器访问本地 Web 服务：不需要 VPS，可以使用自定义域名、HTTPS、Access 和 Durable Object。Worker 部署在你自己的 Cloudflare 账号中。

### Go Relay

适合自建和透明 TCP。它也是 PIK.TAK 协议的参考实现，不依赖 Cloudflare。

Cloudflare 是一种方便的 Relay 部署方式，不是产品边界。

## 三分钟跑通 Go Relay

环境要求：Go 1.23+ 和 Python 3。

```sh
make run-relay       # Relay：:7681 / ingress：:7682
make run-http        # 示例服务：127.0.0.1:7531
make run-bridge      # 启动 piktakd
make run-curl        # 通过 Relay 请求示例服务
```

请求路径：

```text
curl → piktak-relay → piktakd → 127.0.0.1:7531
```

原生 Host/Client 协议示例位于 [`examples/`](examples/)，不会作为用户命令发布。

## Cloudflare 快速开始

### 1. 部署 Relay

```sh
cd piktak-cf/worker
npm install
npm run typecheck
npx wrangler deploy
```

部署和 Access 配置见 [`piktak-cf/README.md`](piktak-cf/README.md)。

### 2. 配置 Machine

保存为 `~/.config/piktak/config.yml`：

```yaml
relay: wss://relay.example.com/host
code: replace-with-a-random-code
status: true
status_addr: 127.0.0.1:8765

services:
  - name: agent
    protocol: ws
    local: 127.0.0.1:3080
    rewrite_host: true
```

`name` 是 Relay 的路由键。使用子域名路由时，`agent.example.com` 会连接名为 `agent` 的 Service。

### 3. 运行

```sh
piktakd -config ~/.config/piktak/config.yml
```

查看配置和本地状态地址：

```sh
piktakd help -config ~/.config/piktak/config.yml
```

状态页默认只监听 loopback：

```text
http://127.0.0.1:8765/
http://127.0.0.1:8765/api/status
```

## Homebrew

首个 PIK.TAK Release 发布后可使用：

```sh
brew tap charlzyx/piktak https://github.com/charlzyx/piktak
brew install piktak
brew services start piktak
```

Formula 安装 `piktakd`，后台服务读取 `~/.config/piktak/config.yml`。

## PIK.TAK 放在哪一层

| 工具 | 主要用途 |
| --- | --- |
| Tailscale | 让设备加入同一个私有网络 |
| ngrok | 提供托管的公网 Tunnel |
| frp | 通过自建公网服务器转发端口 |
| cloudflared | 把本地服务接入 Cloudflare |
| **PIK.TAK** | 让远程 Client 与指定的本地 Service 配对并连接 |

如果只需要把一个普通 HTTP 服务接入 Cloudflare，cloudflared 更成熟。PIK.TAK 想做的是一套不绑定 Relay 的配对与传输协议，Cloudflare 和 Go 只是两种实现。

## 协议方向

```text
L0  identity and pairing
L1  discovery and transport
L2  capability negotiation
L3  application adapters
```

现有服务通过 Bridge 接入，不需要修改代码。以后应用也可以直接集成 Host/Client SDK，使用身份、配对、服务发现、能力协商和 Tunnel。详见 [`docs/protocol.md`](docs/protocol.md)。

## 安全和限制

- 当前机器码是共享静态秘密，不是成熟的设备配对方案；
- 使用随机机器码，不要把示例值用于公网；
- Cloudflare Access 与 Host 机器码是两个不同的认证边界；
- Cloudflare 模式经过 Cloudflare，不是端到端加密；
- Go Relay 的 raw ingress 没有逐连接认证，必须用防火墙或 IP 白名单限制；
- 只接入必要的服务，并检查目标服务对 Host、Origin 和 loopback 的信任规则；
- 项目仍处于早期阶段。

## 接下来

- 一次性配对码和二维码；
- 长期身份、确认、撤销和密钥轮换；
- Machine 下的 Service 发现；
- Go / TypeScript Host 和 Client SDK；
- DSH、PiAgent 等原生适配示例；
- Relay 看不到内容的端到端加密通道。

## License

待定。
