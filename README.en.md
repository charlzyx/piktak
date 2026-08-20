# PIK.TAK

## Pik. Tak. Paired.

PIK.TAK is a Relay and transport protocol that connects remote Clients to Services running on a local Machine. The Machine connects outbound, so it needs no public IP or port forwarding.

[中文](README.md) · [Website](https://piktak.pages.dev) · [Protocol](docs/protocol.md) · [Cloudflare setup](piktak-cf/README.md) · [v0.1.0](https://github.com/charlzyx/piktak/releases/tag/v0.1.0)

> [!WARNING]
> PIK.TAK does not yet provide a complete public-internet security model. Cloudflare mode uses a shared machine code for Host attachment, passes traffic through Cloudflare, and is not end-to-end encrypted. **Every public Service hostname must be protected by Cloudflare Access. Do not connect DSH, an Agent, an admin panel, or another real Service before Access is working.**

## Architecture

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

### Roles

| Name | Meaning |
| --- | --- |
| **Client** | The requesting side, such as a phone browser or native client |
| **Relay** | Lets a Client find a Host and carries control messages and data |
| **Machine** | The local computer or device shown to the user |
| **Host** | The protocol role that connects outbound from a Machine |
| **Bridge** | Connects an existing localhost application without changing it |
| **Service** | DSH, PiAgent, a development dashboard, or another HTTP, WebSocket, or TCP service |

```text
Client → Relay → Host (piktakd) → Bridge → Service
```

PIK.TAK does not run a public Relay cloud. Deploy the Cloudflare backend in your account or run the Go Relay yourself.

## Components

| Component | Path | Purpose |
| --- | --- | --- |
| `piktakd` | `cmd/piktakd` | Machine daemon: Host, Relay connections, Service bridges, and local status |
| `piktak-relay` | `cmd/piktak-relay` | Self-hosted Go Relay with a control port and transparent TCP ingress |
| Cloudflare Relay | `piktak-cf/worker` | Worker + Durable Object for HTTP, streaming responses, and browser WebSockets |
| Native examples | `examples/echo-host`, `examples/echo-client` | Native Host/Client examples; not published as user commands |
| Protocol | `internal/wire`, `internal/l0`, `internal/l2`, `internal/l3` | Framing, pairing, capability negotiation, and adapters |

## `piktakd`

`piktakd` is the only Machine-side program most users need. One process can register multiple Services:

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

- `relay`: Relay address used by the outbound Machine connection;
- `code`: shared machine code used by the current release;
- `name`: Service routing key, normally matching the first label of its browser hostname;
- `protocol`: `ws` for the Cloudflare Relay, `tcp` for the Go Relay;
- `local`: local target;
- `rewrite_host`: compatibility for applications that trust loopback or same-origin requests;
- `status_addr`: loopback-only status page and JSON API.

```sh
piktakd -config ~/.config/piktak/config.yml
piktakd help -config ~/.config/piktak/config.yml
```

```text
http://127.0.0.1:8765/
http://127.0.0.1:8765/api/status
```

The status API reports whether pairing is configured without returning the machine code.

## Relay backends

### Cloudflare Relay

The Cloudflare backend is intended for local web Services used from phones and browsers:

- the Worker accepts HTTP and WebSocket traffic;
- a Durable Object holds the active Host session for each Service name;
- `piktakd` attaches to `/host` over an outbound WebSocket;
- every browser hostname must be protected by Cloudflare Access;
- no separate VPS is required.

#### One-click deployment

[![Deploy to Cloudflare Workers](https://deploy.workers.cloudflare.com/button)](https://deploy.workers.cloudflare.com/?url=https://github.com/charlzyx/piktak/tree/main/piktak-cf/worker)

The button creates the Worker and Durable Object. You must still:

1. generate a random machine code and store it as the `PIKTAK_CODES` Worker Secret;
2. give `/host` a Machine hostname or an Access bypass rule;
3. bind a separate hostname for every browser Service;
4. **create a Cloudflare Access Application and Allow Policy for every browser Service hostname;**
5. set `PIKTAK_REQUIRE_ACCESS = "1"` and redeploy after Access is working;
6. verify that unauthenticated Service requests redirect to Access and direct Worker browser requests return `401`.

See [`piktak-cf/README.md`](piktak-cf/README.md) for the complete setup.

Manual deployment:

```sh
cd piktak-cf/worker
npm install
npm run typecheck
openssl rand -hex 16 | npx wrangler secret put PIKTAK_CODES
npx wrangler deploy
```

### Two Relay paths

PIK.TAK has two independent Relay paths:

| Path | Use it for | Authentication and entry |
| --- | --- | --- |
| Cloudflare Worker | Browsers, HTTP, and WebSocket | Worker secret + Cloudflare Access |
| Go Relay | A self-hosted server and transparent TCP | One-time pairing code + persistent credential |

Cloudflare `ws` configuration and Go `tcp` configuration are not interchangeable. The Go Relay section below applies only to the Go path and does not change the Cloudflare Worker.

### Go Relay

The Go backend is the self-hosted protocol reference implementation for transparent TCP. It uses a dynamic pairing flow independent from the Cloudflare Worker: a one-time `pairing_code` mints a persistent credential saved on the Machine. Cloudflare `code` / `PIKTAK_CODES` is unchanged.

```yaml
relay: 127.0.0.1:7681
pairing_code: one-time-code  # first start only
machine_id: ""
credential_file: ~/.config/piktak/go-credential
services:
  - name: dsh
    protocol: tcp
    local: 127.0.0.1:3080
```

Each Go Host also uses a one-time token for inbound data channels, preventing an authenticated Host from guessing another Host's connection ID. Raw ingress remains transparent TCP; expose it only on a controlled network or dedicated ingress port.

```text
Client / ingress → piktak-relay → piktakd → local Service
```

Generate a one-time pairing code when the Relay has `PIKTAK_STATE` configured:

```sh
PIKTAK_STATE=/var/lib/piktak/state.json piktak-relay pair
```

Put the output in `pairing_code`. After the first connection, `piktakd` saves the credential and reconnects with it automatically.

```sh
make run-relay       # control :7681 / ingress :7682
make run-http        # example Service on 127.0.0.1:7531
make run-bridge      # start piktakd
make run-curl        # request through the Relay
```

> [!CAUTION]
> Go Relay raw ingress is transparent TCP, so it cannot inspect application bytes for authentication. Never expose it directly to the public internet; restrict it with a firewall, private network, or dedicated ingress capability.

## Installation

### Homebrew

```sh
brew tap charlzyx/piktak https://github.com/charlzyx/piktak
brew trust --formula charlzyx/piktak/piktak
brew install charlzyx/piktak/piktak
brew services start piktak
```

The formula installs `piktakd` and reads `~/.config/piktak/config.yml`. If GitHub Release downloads are slow, pass your proxy through `HTTPS_PROXY`, `HTTP_PROXY`, and `ALL_PROXY` while running Homebrew.

### Release assets

[`v0.1.0`](https://github.com/charlzyx/piktak/releases/tag/v0.1.0) provides static Linux and macOS binaries for amd64 and arm64. Release CI calculates their SHA-256 values and updates `Formula/piktak.rb` automatically.

## Available today

- multiple Services in one `piktakd` process;
- HTTP, streaming responses, and browser WebSockets;
- Cloudflare Worker and Durable Object routing;
- transparent TCP through the Go Relay;
- machine-code allowlists, reconnects, and protocol version checks;
- Host/Origin loopback compatibility;
- loopback-only status page and redacted JSON API;
- Linux/macOS amd64/arm64 releases and Homebrew formula.

## Protocol

```text
L0  identity and pairing
L1  discovery and transport
L2  capability negotiation
L3  application adapters
```

L0 currently uses a shared machine code. Existing Services connect through the Bridge; future applications can integrate Host and Client SDKs directly. See [`docs/protocol.md`](docs/protocol.md).

## Security boundaries

- **Cloudflare Access is required for every public browser entry point. It is not an optional enhancement.**
- The machine code protects Host attachment; it does not authenticate browser users.
- A machine code is a shared static secret, not mature device pairing.
- Never commit it, include it in screenshots, or print it in public logs.
- Cloudflare mode passes through Cloudflare and is not end-to-end encrypted.
- Expose only required Services and review their Host, Origin, cookie, and loopback trust rules.
- Restrict all Go raw ingress traffic at the network boundary.
- The project is early.

## Origin

PIK.TAK started as a way to use DSH and PiAgent from a phone without keeping Tailscale active. DSH is the first real application, but PIK.TAK can bridge any suitable HTTP, WebSocket, or TCP Service.

## Roadmap

- one-time pairing and QR codes;
- long-term identities, confirmation, revocation, and key rotation;
- Service discovery under a Machine;
- Go and TypeScript Host/Client SDKs;
- end-to-end encrypted tunnels opaque to the Relay.

## License

TBD.
