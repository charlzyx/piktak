# PIK.TAK

## Pik. Tak. Paired.

PIK.TAK connects phones, browsers, and remote clients to services running on your machine.

It started as a way to use DSH and PiAgent from a phone without keeping Tailscale active. Today, any HTTP, WebSocket, or TCP service can connect through a Cloudflare Worker or a self-hosted Go Relay.

No public IP. No port forwarding. No client on the phone.

[中文](README.md) · [Website](https://piktak.pages.dev) · [Protocol](docs/protocol.md) · [Cloudflare setup](piktak-cf/README.md)

## How it works

```text
Phone / browser / Client
           │
           ▼
      PIK.TAK Relay
 Cloudflare Worker / Go
           │ outbound connection
           ▼
      Your Machine
           │
        piktakd
           │ Bridge
           ▼
 DSH · PiAgent · local services
```

Users see a **Machine** and its **Services**. The protocol uses five precise roles:

- **Client** — a phone browser or native client;
- **Relay** — lets both ends find each other and carries requests and data;
- **Host** — the protocol role that connects outbound from a Machine;
- **Bridge** — connects an existing localhost service that does not speak PIK.TAK;
- **Service** — DSH, PiAgent, a development dashboard, or any other local service.

`piktakd` is the daemon running on a Machine. It implements the Host, maintains Relay connections, and bridges existing services.

## Why PIK.TAK

I run DSH and PiAgent on my computer and wanted to keep using them from my phone. Tailscale solves the network problem, but keeping a VPN active for one local web app felt heavier than necessary.

The first version was a Go Relay that proved an outbound Host connection, discovery, and data tunnels. I later added a Cloudflare Worker because it needs no extra VPS and works with domains, HTTPS, Durable Objects, and Cloudflare Access.

DSH is the first real application, not the boundary. Agent UIs, development dashboards, home services, and other local applications can use the same path.

## What works today

- `piktakd`, a static Go binary that bridges one or more local services;
- HTTP requests, streaming responses, and browser WebSockets;
- Cloudflare Worker and Durable Object routing;
- Cloudflare Access for browser entry points;
- transparent TCP through the Go Relay;
- machine-code allowlists, reconnects, and Host/Origin rewriting;
- a loopback-only status page and JSON API.

Pairing currently uses a shared machine code. One-time codes, long-term identities, revocation, and end-to-end encryption remain roadmap work.

## Two Relay backends

### Cloudflare Worker

For local web services used from a phone or browser. It needs no VPS and works with custom domains, HTTPS, Access, and Durable Objects. You deploy it in your own Cloudflare account.

### Go Relay

For self-hosting and transparent TCP. It is also the reference implementation of the PIK.TAK protocol and has no Cloudflare dependency.

Cloudflare is a convenient Relay backend, not the product boundary.

## Run the Go Relay in three minutes

Requires Go 1.23+ and Python 3.

```sh
make run-relay       # Relay on :7681, ingress on :7682
make run-http        # Example service on 127.0.0.1:7531
make run-bridge      # Start piktakd
make run-curl        # Request the service through the Relay
```

```text
curl → piktak-relay → piktakd → 127.0.0.1:7531
```

Native Host/Client protocol examples live in [`examples/`](examples/) and are not published as user-facing commands.

## Cloudflare quick start

### 1. Deploy the Relay

```sh
cd piktak-cf/worker
npm install
npm run typecheck
npx wrangler deploy
```

See [`piktak-cf/README.md`](piktak-cf/README.md) for deployment and Access setup.

### 2. Configure the Machine

Save as `~/.config/piktak/config.yml`:

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

`name` is the Relay routing key. With subdomain routing, `agent.example.com` connects to the Service named `agent`.

### 3. Run

```sh
piktakd -config ~/.config/piktak/config.yml
piktakd help -config ~/.config/piktak/config.yml
```

The status page listens on loopback by default:

```text
http://127.0.0.1:8765/
http://127.0.0.1:8765/api/status
```

## Homebrew

Install from GitHub Releases:

```sh
brew tap charlzyx/piktak https://github.com/charlzyx/piktak
brew trust --formula charlzyx/piktak/piktak
brew install charlzyx/piktak/piktak
brew services start piktak
```

The formula installs `piktakd` and reads `~/.config/piktak/config.yml` when run as a service. If GitHub Release downloads are slow on your network, pass your HTTP proxy through `HTTPS_PROXY`, `HTTP_PROXY`, and `ALL_PROXY` while running Homebrew.

## Where PIK.TAK fits

| Tool | Primary model |
| --- | --- |
| Tailscale | Connect devices and private networks |
| ngrok | Hosted public tunnels |
| frp | Port forwarding through a self-hosted public server |
| cloudflared | Connect localhost to Cloudflare |
| **PIK.TAK** | Pair a remote Client with a selected local Service |

If you only need to connect one ordinary HTTP service to Cloudflare, cloudflared is more mature. PIK.TAK is working toward a Relay-independent pairing and transport protocol; Cloudflare and Go are two backends.

## Protocol direction

```text
L0  identity and pairing
L1  discovery and transport
L2  capability negotiation
L3  application adapters
```

Existing services connect through the Bridge without code changes. Future applications can integrate Host and Client SDKs for identity, pairing, service discovery, capabilities, and tunnels. See [`docs/protocol.md`](docs/protocol.md).

## Security and limitations

- The current machine code is a shared static secret, not mature device pairing.
- Generate a random code; never use the example value on the public internet.
- Cloudflare Access and the Host machine code protect separate boundaries.
- Cloudflare mode passes through Cloudflare and is not end-to-end encrypted.
- Go Relay raw ingress has no per-connection authentication; restrict it with a firewall or IP allowlist.
- Expose only the services you need and review their Host, Origin, and loopback trust rules.
- The project is early.

## Next

- one-time pairing codes and QR codes;
- long-term identities, confirmation, revocation, and key rotation;
- Service discovery under a Machine;
- Go and TypeScript Host/Client SDKs;
- native DSH and PiAgent integration examples;
- end-to-end encrypted tunnels opaque to the Relay.

## License

TBD.
