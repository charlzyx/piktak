/**
 * piktak-cf worker — the Cloudflare-Worker relay for PIK.TAK.
 *
 * Sibling of piktak-go (internal/relay), reimplemented for the Workers runtime.
 * Unlike piktak-go's relay (a dumb raw-TCP byte pipe), this relay is HTTP-aware:
 * it terminates the browser's HTTPS request, ships the request metadata +
 * body to the NAT'd host over the host's single outbound WebSocket, and
 * streams the host's response back into a Fetch API Response.
 *
 * Host session lifecycle (PIK.TAK vocabulary, adapted to one multiplexed WS):
 *   host → wss://<worker>/host?code=<MACHINE_CODE>   (WS upgrade)
 *   worker → {"t":"hello.ok","hostId":"<code>"} | {"t":"hello.err","code":"UNAUTHORIZED"} (+close)
 *   host → {"t":"expose","local":"127.0.0.1:7531"}    (marks host ready)
 *   per browser request:
 *     worker → {"t":"inbound","connId":"c1","method":"GET","path":"/","headers":{...}}
 *     worker → binary data frames (request body)          [framing below]
 *     worker → {"t":"data.end","connId":"c1"}
 *     host   → {"t":"resp","connId":"c1","status":200,"headers":{...}}
 *     host   → binary data frames (response body chunks)
 *     host   → {"t":"data.end","connId":"c1"}
 *   worker → {"t":"close","connId":"c1"}  (best-effort cancel: browser gone / timeout)
 *
 * Binary data-frame framing (WS adaptation of PIK.TAK's data channel):
 *   [u32 big-endian headerLen][header JSON utf8][raw payload bytes]
 *   header = {"t":"data","connId":"<id>"}
 * One WS frame carries both the control envelope and the payload, so
 * multiplexed connIds can never interleave header/payload across frames.
 *
 * WHY A DURABLE OBJECT — the relay routes BOTH the host's WS attach (/host)
 * and every browser HTTP request to ONE Durable Object instance
 * (idFromName("default")). A plain Worker's module scope is PER-ISOLATE: the
 * host's WS would land on isolate A (colo nearest the host's proxy exit) and
 * the phone's HTTP on isolate B (nearest the phone's proxy exit) — different
 * isolate → empty host map → 502 {"error":"no host"}. A DO is a single
 * instance across colos: both routes hit the same DO → same isolate → the
 * host is found. (Locally under wrangler dev this is moot — one instance —
 * which is exactly why the per-isolate bug was masked.)
 *
 * WORKERS RUNTIME CONSTRAINTS — what the DO still may and may not do:
 *   workerd pins every I/O object to the request that created it ("Cannot
 *   perform I/O on behalf of a different request"). A Durable Object LIFTS
 *   the worker→host direction: inside the DO, a browser-request fetch() may
 *   call this.hostWs.send(...) directly on the WebSocket the /host fetch
 *   accepted earlier — so the worker→host outbox and the poll/poll.done
 *   mechanism are gone; inbound/data/data.end are pushed straight through.
 *   The host→browser direction is STILL constrained: you may NOT resolve a
 *   promise a browser-request fetch() is awaiting from inside a WebSocket
 *   message callback (same workerd rule). So the host's resp/data/data.end
 *   messages write into a per-connId PLAIN-JS buffer (a Map on the DO
 *   instance); the browser fetch's ReadableStream.pull polls that buffer
 *   with a short `await sleep(...)` self-timer (a promise created and
 *   awaited inside the same fetch is legal).
 *
 * TODO(v0.1): WebSocket event-stream bridging (browser WS <-> local WS).
 */
import { Hono } from 'hono';

type Env = {
  /** Comma-separated allowlist of machine codes (L0 pairing secret). */
  PIKTAK_CODES: string;
  /** The single Durable Object holding the live host WS + per-conn buffers. */
  PIKTAK_HOST: DurableObjectNamespace;
  /**
   * "1" = require a Cloudflare Access JWT on every browser path (HTTP + WS
   * tunnel). When set, requests without `cf-access-jwt-assertion` (i.e. ones
   * that bypassed Access, e.g. a direct *.workers.dev hit) are rejected with
   * 401. Default unset = open (dormant until Access is configured). v0 checks
   * presence only (Access already validated the request before forwarding);
   * full RS256 verification of the JWT's aud/exp is a v0.2 hardening.
   */
  PIKTAK_REQUIRE_ACCESS: string;
};

const app = new Hono<{ Bindings: Env }>();

// ---------------------------------------------------------------------------
// Protocol codec
// ---------------------------------------------------------------------------

interface DataHeader {
  t: 'data';
  connId: string;
  /**
   * For WS-tunnel conns only: whether this message is binary (true) or text
   * (false/absent). The Worker's WS API decodes browser messages and re-frames
   * outgoing ones, so the tunnel is MESSAGE-oriented, not a raw byte splice —
   * bridge must call dshWs.send(payload, {binary: bin}) to frame correctly for
   * the local target. (HTTP conns ignore this; their bodies are raw bytes.)
   */
  bin: boolean;
}

/** Encode one binary data frame: [u32be headerLen][header JSON][payload]. */
function encodeDataFrame(connId: string, payload: Uint8Array, bin = false): Uint8Array {
  const headerBytes = new TextEncoder().encode(
    JSON.stringify(bin ? { t: 'data', connId, bin: true } : { t: 'data', connId }),
  );
  const out = new Uint8Array(4 + headerBytes.byteLength + payload.byteLength);
  new DataView(out.buffer).setUint32(0, headerBytes.byteLength, false);
  out.set(headerBytes, 4);
  out.set(payload, 4 + headerBytes.byteLength);
  return out;
}

/** Decode a binary data frame; null on malformed input. */
function decodeDataFrame(buf: ArrayBuffer): { header: DataHeader; payload: Uint8Array } | null {
  if (buf.byteLength < 4) return null;
  const view = new DataView(buf);
  const headerLen = view.getUint32(0, false);
  if (headerLen > buf.byteLength - 4) return null;
  let header: unknown;
  try {
    header = JSON.parse(new TextDecoder().decode(new Uint8Array(buf, 4, headerLen)));
  } catch {
    return null;
  }
  const h = header as Record<string, unknown>;
  if (h.t !== 'data' || typeof h.connId !== 'string') return null;
  return {
    header: { t: 'data', connId: h.connId, bin: h.bin === true },
    payload: new Uint8Array(buf, 4 + headerLen),
  };
}

/** Control messages the DO understands from the host. */
type HostMsg =
  | { t: 'expose'; local: string }
  | { t: 'resp'; connId: string; status: number; headers: Record<string, string> }
  | { t: 'data.end'; connId: string }
  | { t: 'close'; connId: string };

function isStrMap(v: unknown): v is Record<string, string> {
  if (typeof v !== 'object' || v === null) return false;
  return Object.values(v).every((x) => typeof x === 'string');
}

function parseHostMsg(data: string): HostMsg | null {
  let m: unknown;
  try {
    m = JSON.parse(data);
  } catch {
    return null;
  }
  if (typeof m !== 'object' || m === null) return null;
  const o = m as Record<string, unknown>;
  switch (o.t) {
    case 'expose':
      return typeof o.local === 'string' ? { t: 'expose', local: o.local } : null;
    case 'resp':
      if (typeof o.connId === 'string' && typeof o.status === 'number' && isStrMap(o.headers)) {
        return { t: 'resp', connId: o.connId, status: o.status, headers: o.headers };
      }
      return null;
    case 'data.end':
      return typeof o.connId === 'string' ? { t: 'data.end', connId: o.connId } : null;
    case 'close':
      return typeof o.connId === 'string' ? { t: 'close', connId: o.connId } : null;
    default:
      // poll / poll.done are no longer sent by bridge, but tolerate them
      // (ignore) if a stale host ever does. Unknown frames are ignored too.
      return null;
  }
}

// ---------------------------------------------------------------------------
// Pacing constants (host→browser pump only; worker→host is now direct)
// ---------------------------------------------------------------------------

const HEAD_TIMEOUT_MS = 15_000;
/** Browser-side pump cadence: how often a request re-checks its buffer. */
const PUMP_MS = 5;

/** Hop-by-hop / re-framed headers stripped from the inbound request copy. */
const STRIP_INBOUND = new Set([
  'host',
  'connection',
  'content-length',
  'transfer-encoding',
  'upgrade',
]);

/**
 * Sleep on a timer created and awaited inside THIS request context — the
 * only legal way to pace the buffer polling below (cross-context promises
 * are forbidden).
 */
function sleep(ms: number): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function safeSend(ws: WebSocket, data: string | Uint8Array): boolean {
  try {
    ws.send(data);
    return true;
  } catch (err) {
    console.error(`ws send failed: ${err instanceof Error ? err.message : String(err)}`);
    return false;
  }
}

// ---------------------------------------------------------------------------
// Hono shell — every request is forwarded to the PiktakRelay DO for the routing
// key: /host?name=<key> (host attach) or the first label of the Host header
// (browser requests, e.g. agent.example.com -> DO("fx")). Each key is its own
// DO, so hosts never displace each other and the host WS + browser requests
// for one service always land on the same isolate.
// ---------------------------------------------------------------------------

function routeToHost(env: Env, req: Request, name: string): Promise<Response> {
  const id = env.PIKTAK_HOST.idFromName(name || 'default');
  const stub = env.PIKTAK_HOST.get(id);
  return stub.fetch(req);
}

function hostLabel(req: Request): string {
  const host = req.headers.get('host') ?? '';
  const first = host.split('.')[0] ?? '';
  return first.replace(/[^a-zA-Z0-9_-]/g, '').toLowerCase();
}

app.get('/host', (c) => {
  const name = new URL(c.req.url).searchParams.get('name') ?? '';
  return routeToHost(c.env, c.req.raw, name);
});
app.all('*', (c) => routeToHost(c.env, c.req.raw, hostLabel(c.req.raw)));

// ---------------------------------------------------------------------------
// PiktakRelay Durable Object
// ---------------------------------------------------------------------------

/**
 * Per-connection browser-response buffer. Written SYNCHRONOUSLY by the
 * host's WS message handler (plain data only — no promises resolved across
 * request contexts), polled by the browser fetch on its own self-timer.
 */
interface ConnBuf {
  status: number;
  headers: Record<string, string>;
  headReady: boolean; // set once when {"t":"resp"} arrives
  chunks: Uint8Array[]; // response body bytes so far
  ended: boolean; // set when the host's {"t":"data.end"} arrives
  error: string; // '' = no error; set on failure/cancel
  // v0.1 WS event-stream tunnel: when true this conn is a bidirectional
  // browser-WS <-> host-data-frame splice (no HTTP pump). The HTTP fields
  // above are unused for tunnel conns.
  tunnel: boolean;
  browserWs: WebSocket | null;
}

export class PiktakRelay {
  /** The host's accepted server-side WS, or null when no host is attached. */
  hostWs: WebSocket | null = null;
  /** Local target the host exposed (set on {"t":"expose"}). */
  local: string | null = null;
  /** connId -> browser-response buffer. */
  conns: Map<string, ConnBuf> = new Map();

  constructor(private ctx: DurableObjectState, private env: Env) {}

  async fetch(request: Request): Promise<Response> {
    const upgrade = request.headers.get('upgrade') ?? '';
    if (upgrade.toLowerCase() === 'websocket') {
      // /host is the host's own WS attachment; any other path is a browser
      // WebSocket event-stream the DO must tunnel through to the host (v0.1).
      const url = new URL(request.url);
      if (url.pathname === '/host') return this.handleHostAttach(request);
      return this.handleBrowserWsTunnel(request);
    }
    return this.handleBrowserHttp(request);
  }

  // --- /host: the host's outbound WebSocket attachment point ----------------

  private handleHostAttach(request: Request): Response {
    const url = new URL(request.url);
    const code = url.searchParams.get('code') ?? '';
    const allowed = (this.env.PIKTAK_CODES ?? '')
      .split(',')
      .map((s) => s.trim())
      .filter(Boolean);

    // Protocol alignment (piktak-ws/1): a peer that declares a different version
    // is rejected loudly. Absent v (legacy peers) is tolerated.
    const v = url.searchParams.get('v');
    if (v !== null && v !== '1') {
      const pair = new WebSocketPair();
      const [client, server] = [pair[0], pair[1]];
      server.accept();
      safeSend(server, JSON.stringify({ t: 'hello.err', code: 'PROTOCOL_MISMATCH' }));
      server.close(1008, 'protocol mismatch');
      return new Response(null, { status: 101, webSocket: client });
    }

    const pair = new WebSocketPair();
    const client = pair[0];
    const server = pair[1];
    server.accept();

    if (!allowed.includes(code)) {
      safeSend(server, JSON.stringify({ t: 'hello.err', code: 'UNAUTHORIZED' }));
      server.close(1008, 'unauthorized');
      return new Response(null, { status: 101, webSocket: client });
    }

    // A new host attaches: any browser conns still waiting on the old host
    // are stale — error out HTTP streams and close leftover tunnel WSes so
    // their browser sockets don't leak.
    for (const c of this.conns.values()) {
      if (c.tunnel) {
        try { c.browserWs?.close(1011, 'host reconnected'); } catch { /* ignore */ }
      } else {
        c.error = 'host reconnected';
      }
    }
    this.conns = new Map();
    this.hostWs = server;
    this.local = null;

    server.addEventListener('message', (event) => this.onHostMsg(event.data));
    server.addEventListener('close', () => {
      if (this.hostWs === server) this.hostWs = null;
    });
    server.addEventListener('error', () => {
      if (this.hostWs === server) this.hostWs = null;
    });

    safeSend(server, JSON.stringify({ t: 'hello.ok', hostId: code }));
    return new Response(null, { status: 101, webSocket: client });
  }

  // --- browser WebSocket event-stream tunnel (v0.1) ------------------------
  // The browser opened a WS (e.g. /api/events.mux). We accept it, hand the
  // upgrade request to the host as `inbound` (keeping Upgrade/Connection/
  // Sec-WebSocket-* so dsh sees a real handshake), then bidirectionally splice:
  // browser WS messages -> host data frames; host data frames (dsh WS bytes)
  // -> browser WS sends. Both directions are WS `send`s inside this DO
  // isolate — workerd allows that (the cross-request I/O restriction is
  // lifted for DOs), so no pump / no outbox is needed here.

  private handleBrowserWsTunnel(request: Request): Response {
    const gate = this.accessGate(request);
    if (gate) return gate;
    const url = new URL(request.url);
    const connId = crypto.randomUUID();
    const pair = new WebSocketPair();
    const client = pair[0];
    const server = pair[1];
    server.accept();

    if (!this.hostWs) {
      // No host attached: handshake then close so the browser's WS fails fast
      // instead of hanging. (A WS-upgrade Response must carry a webSocket.)
      this.conns.delete(connId);
      server.close(1011, 'no host');
      return new Response(null, { status: 101, webSocket: client });
    }

    // Keep Upgrade/Connection/Sec-WebSocket-Key/Version so the local target
    // handshakes; strip host/content-length/transfer-encoding (the local HTTP
    // server sets its own Host; a stale length would misframe the upgrade) AND
    // sec-websocket-extensions — permessage-deflate must NOT be negotiated on
    // the bridge<->dsh leg, because the browser<->Worker leg is uncompressed
    // (the Workers runtime does not accept permessage-deflate) and the two legs
    // would otherwise carry independent LZ contexts the raw splice can't bridge.
    const headers: Record<string, string> = {};
    request.headers.forEach((value, key) => {
      const lk = key.toLowerCase();
      if (lk === 'host' || lk === 'content-length' || lk === 'transfer-encoding' || lk === 'sec-websocket-extensions') return;
      headers[key] = value;
    });

    const buf: ConnBuf = {
      status: 0,
      headers: {},
      headReady: false,
      chunks: [],
      ended: false,
      error: '',
      tunnel: true,
      browserWs: server,
    };
    this.conns.set(connId, buf);

    // browser -> host: each WS message becomes one host data frame. The Worker
    // WS API decodes frames into messages, so we forward MESSAGES (with a text
    // /binary flag) — bridge re-frames for the local target. (A raw byte splice
    // here would mis-frame and crash the target's WS receiver.)
    server.addEventListener('message', (event) => {
      const d = event.data;
      let bytes: Uint8Array;
      let bin = false;
      if (typeof d === 'string') {
        bytes = new TextEncoder().encode(d);
      } else if (d instanceof ArrayBuffer) {
        bytes = new Uint8Array(d);
        bin = true;
      } else if (ArrayBuffer.isView(d)) {
        const v = d as Uint8Array;
        bytes = new Uint8Array(v.buffer.slice(v.byteOffset, v.byteOffset + v.byteLength) as ArrayBuffer);
        bin = true;
      } else {
        bytes = new TextEncoder().encode(String(d));
      }
      if (bytes.byteLength > 0) this.sendHost(encodeDataFrame(connId, bytes, bin));
    });
    const tearDown = () => {
      this.conns.delete(connId);
      this.sendHost(JSON.stringify({ t: 'close', connId }));
    };
    server.addEventListener('close', tearDown);
    server.addEventListener('error', tearDown);

    // Push the upgrade request to the host. No body, no data.end — the tunnel
    // stays open until one side closes.
    this.sendHost(
      JSON.stringify({ t: 'inbound', connId, method: request.method, path: url.pathname + url.search, headers }),
    );
    return new Response(null, { status: 101, webSocket: client });
  }

  // --- catch-all: browser HTTP, proxied to the host over its WS --------------

  private async handleBrowserHttp(request: Request): Promise<Response> {
    const gate = this.accessGate(request);
    if (gate) return gate;
    if (!this.hostWs) {
      return Response.json({ error: 'no host' }, { status: 502 });
    }

    const url = new URL(request.url);
    const path = url.pathname + url.search;
    const connId = crypto.randomUUID();
    const headers: Record<string, string> = {};
    request.headers.forEach((value, key) => {
      if (STRIP_INBOUND.has(key.toLowerCase())) return;
      headers[key] = value;
    });

    const buf: ConnBuf = {
      status: 0,
      headers: {},
      headReady: false,
      chunks: [],
      ended: false,
      error: '',
      tunnel: false,
      browserWs: null,
    };
    this.conns.set(connId, buf);

    // 1) push the inbound request straight to the host. Legal inside a DO:
    //    a fetch here may send on the WS the /host fetch accepted earlier
    //    (the cross-request I/O restriction is lifted for DOs).
    this.sendHost(
      JSON.stringify({ t: 'inbound', connId, method: request.method, path, headers }),
    );

    // 2) pump the request body (if any) as binary data frames, then data.end.
    //    Runs concurrently with the head wait below (single-threaded
    //    interleaving via awaits, all inside this request context).
    void (async () => {
      try {
        const body = request.body;
        if (body) {
          const reader = body.getReader();
          for (;;) {
            const { done, value } = await reader.read();
            if (done) break;
            if (value && value.byteLength > 0) {
              this.sendHost(encodeDataFrame(connId, value));
            }
          }
        }
        this.sendHost(JSON.stringify({ t: 'data.end', connId }));
      } catch {
        buf.error = 'request body pump failed';
        this.sendHost(JSON.stringify({ t: 'close', connId }));
      }
    })();

    // 3) wait for the response head on our own timer. The host→browser
    //    direction is still constrained: onHostMsg only writes plain data
    //    into `buf`; we poll it. A promise awaited here cannot be resolved
    //    from a WS message callback, so we deliberately never create one.
    const deadline = Date.now() + HEAD_TIMEOUT_MS;
    while (!buf.headReady && !buf.error) {
      if (Date.now() > deadline) {
        buf.error = 'head timeout';
        break;
      }
      await sleep(PUMP_MS);
    }
    if (buf.error || !buf.headReady) {
      const msg = buf.error || 'head timeout';
      this.conns.delete(connId);
      this.sendHost(JSON.stringify({ t: 'close', connId }));
      return Response.json({ error: msg }, { status: msg === 'head timeout' ? 504 : 502 });
    }

    const respHeaders = new Headers();
    for (const [k, v] of Object.entries(buf.headers)) {
      // Streamed bodies are chunk-encoded by the runtime; a stale
      // content-length would truncate/hang the browser.
      if (k.toLowerCase() === 'content-length') continue;
      respHeaders.set(k, v);
    }

    // 4) stream the body: created AND pumped in this (browser) context,
    //    draining the plain-JS buffer the host's WS events fill.
    const stream = new ReadableStream<Uint8Array>({
      pull: async (controller) => {
        for (;;) {
          const chunk = buf.chunks.shift();
          if (chunk !== undefined) {
            // Re-copy inside this context before touching the stream.
            controller.enqueue(new Uint8Array(chunk));
            return;
          }
          if (buf.ended) {
            controller.close();
            this.conns.delete(connId);
            return;
          }
          if (buf.error) {
            controller.error(new Error(buf.error));
            this.conns.delete(connId);
            return;
          }
          await sleep(PUMP_MS);
        }
      },
      cancel: () => {
        buf.error = 'cancelled';
        this.sendHost(JSON.stringify({ t: 'close', connId }));
      },
    });
    return new Response(stream, { status: buf.status, headers: respHeaders });
  }

  // --- host → DO: response head/body/plain control ---------------------------

  private onHostMsg(data: unknown): void {
    if (typeof data === 'string') {
      const msg = parseHostMsg(data);
      if (!msg) return;
      switch (msg.t) {
        case 'expose':
          this.local = msg.local;
          console.log(`host ready: local=${this.local}`);
          return;
        case 'resp': {
          const c = this.conns.get(msg.connId);
          // resp{101} from a tunnel conn is just an upgrade ack; the browser
          // already handshook with us. Ignore it. The HTTP pump needs it.
          if (c && !c.tunnel && !c.headReady) {
            c.status = msg.status;
            c.headers = msg.headers;
            c.headReady = true;
          }
          return;
        }
        case 'data.end': {
          const c = this.conns.get(msg.connId);
          if (c) {
            if (c.tunnel) {
              // dsh closed its WS: tear down the browser side too.
              if (c.browserWs) c.browserWs.close();
              this.conns.delete(msg.connId);
            } else {
              c.ended = true;
            }
          }
          return;
        }
        case 'close': {
          const c = this.conns.get(msg.connId);
          if (c) {
            if (c.tunnel) {
              if (c.browserWs) c.browserWs.close();
              this.conns.delete(msg.connId);
            } else {
              // HTTP: a host-originated close cancels the browser stream.
              c.error = 'closed';
            }
          }
          return;
        }
      }
      return;
    }

    // Binary data frame: [u32be headerLen][header JSON][payload bytes].
    let ab: ArrayBuffer | null = null;
    if (data instanceof ArrayBuffer) {
      ab = data;
    } else if (ArrayBuffer.isView(data)) {
      const v = data as Uint8Array;
      // The view may alias a larger buffer; slice out just our bytes.
      ab = v.buffer.slice(v.byteOffset, v.byteOffset + v.byteLength) as ArrayBuffer;
    }
    if (!ab) return;
    const frame = decodeDataFrame(ab);
    if (!frame) return;
    const c = this.conns.get(frame.header.connId);
    if (!c || frame.payload.byteLength === 0) return;
    if (c.tunnel) {
      // host -> browser: bridge forwarded a decoded WS MESSAGE (with a text/
      // binary flag). Send it to the browser WS as text or binary so the
      // Worker re-frames it exactly once (a raw-byte send would double-frame).
      if (c.browserWs && c.browserWs.readyState === 1) {
        if (frame.header.bin) {
          safeSend(c.browserWs, new Uint8Array(frame.payload));
        } else {
          safeSend(c.browserWs, new TextDecoder().decode(frame.payload));
        }
      }
      return;
    }
    // Copy: the payload view aliases the WS frame buffer.
    c.chunks.push(new Uint8Array(frame.payload));
  }

  /** Best-effort send to the attached host WS (worker→host, legal in a DO). */
  private sendHost(data: string | Uint8Array): boolean {
    const ws = this.hostWs;
    if (!ws) return false;
    return safeSend(ws, data);
  }

  /**
   * Cloudflare Access gate. When PIKTAK_REQUIRE_ACCESS="1", every browser path
   * (HTTP + WS-tunnel) must carry `cf-access-jwt-assertion`, which Access
   * injects only after it validated the user. A request without it bypassed
   * Access (e.g. a direct *.workers.dev hit) and is rejected with 401. Null
   * when access is not required / the request is allowed.
   */
  private accessGate(request: Request): Response | null {
    if (this.env.PIKTAK_REQUIRE_ACCESS !== '1') return null;
    if (request.headers.get('cf-access-jwt-assertion')) return null;
    return Response.json({ error: 'unauthorized' }, { status: 401 });
  }
}

export default app;
