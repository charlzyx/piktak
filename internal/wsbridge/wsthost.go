// Package wsthost is the WS-mode host driver for piktak-cf: it opens a persistent
// WebSocket to the CF Worker's /host endpoint, pairs with a machine code,
// exposes a local TCP address (e.g. a loopback dsh web server), and serves
// browser requests the Worker multiplexes over that WS by connId:
//
//   - HTTP reverse-proxy: dial PIKTAK_LOCAL, write a serialized HTTP/1.1 request
//     (Host rewritten to loopback, browser cross-origin markers stripped so a
//     loopback-trusting backend like dsh accepts it), read the response and
//     stream it back as data frames.
//   - WS event-stream tunnel: on an inbound with Upgrade: websocket, act as a
//     WS CLIENT to the local target and forward MESSAGES with a text/binary
//     flag. A raw byte splice is wrong here: the Worker's WS API is
//     message-oriented, so raw frames would be double-framed / mis-framed
//     (it crashed dsh with WS_ERR_UNEXPECTED_RSV_2_3).
//
// Data frames: [u32be headerLen][JSON {"t":"data","connId","bin"?}][payload].
// Permessage-deflate is never negotiated (the browser<->Worker leg is
// uncompressed; two independent LZ contexts across a splice cannot bridge).
package wsbridge

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/charlzyx/piktak/internal/status"
	"github.com/charlzyx/piktak/internal/wire"
)

const reconnectDelay = 2 * time.Second

// Host is configured by the binary.
type Host struct {
	Relay string // ws:// or wss:// URL, e.g. wss://piktak-host.example/host
	Code  string // machine code (L0 pairing secret)
	Local string // local addr to expose, e.g. 127.0.0.1:3080
	// Name: routing key (e.g. the browser subdomain). Sent as ?name= on the
	// attach so the relay routes this host's WS to its own DO; empty for a
	// single default host.
	Name string
	// RewriteHost: loopback-compat mode. thost forwards faithfully by default
	// (passes the request's Host and browser markers through). Some backends
	// (e.g. dsh) trust only loopback/same-origin, so enable this to rewrite
	// Host to Local and strip the browser's Origin/sec-fetch markers. This is
	// backend-trust POLICY, not thost's core duty — hence opt-in.
	RewriteHost bool
	Status      *status.State
}

// Run dials the Worker, pairs, exposes, and serves until ctx is cancelled
// (reconnecting with backoff if the WS drops).
func (h *Host) Run(ctx context.Context) error {
	hh := &host{cfg: *h}
	for {
		err := hh.runOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			log.Printf("[wsthost] relay error: %v; retrying in %s", err, reconnectDelay)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(reconnectDelay):
		}
	}
}

// ---------------------------------------------------------------------------
// data frame codec: [u32be headerLen][JSON header][payload]
// ---------------------------------------------------------------------------

func encodeDataFrame(connID string, payload []byte, bin bool) []byte {
	h, _ := json.Marshal(struct {
		T      string `json:"t"`
		ConnID string `json:"connId"`
		Bin    bool   `json:"bin,omitempty"`
	}{"data", connID, bin})
	out := make([]byte, 4+len(h)+len(payload))
	binary.BigEndian.PutUint32(out, uint32(len(h)))
	copy(out[4:], h)
	copy(out[4+len(h):], payload)
	return out
}

func decodeDataFrame(buf []byte) (connID string, payload []byte, bin bool, ok bool) {
	if len(buf) < 4 {
		return "", nil, false, false
	}
	hl := int(binary.BigEndian.Uint32(buf[:4]))
	if hl > len(buf)-4 {
		return "", nil, false, false
	}
	var h struct {
		T      string `json:"t"`
		ConnID string `json:"connId"`
		Bin    bool   `json:"bin"`
	}
	if err := json.Unmarshal(buf[4:4+hl], &h); err != nil || h.T != "data" || h.ConnID == "" {
		return "", nil, false, false
	}
	return h.ConnID, buf[4+hl:], h.Bin, true
}

// ---------------------------------------------------------------------------
// worker control messages
// ---------------------------------------------------------------------------

type inboundMsg struct {
	ConnID  string            `json:"connId"`
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	Headers map[string]string `json:"headers"`
}

// parseWorkerMsg returns the control type + its connId (data.end/close) +
// the inbound body (when t == "inbound").
func parseWorkerMsg(raw []byte) (t string, connID string, in *inboundMsg) {
	var m struct {
		T       string            `json:"t"`
		ConnID  string            `json:"connId"`
		Method  string            `json:"method"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return "", "", nil
	}
	switch m.T {
	case "hello.ok", "hello.err":
		return m.T, "", nil
	case "inbound":
		if m.ConnID == "" {
			return "", "", nil
		}
		return "inbound", m.ConnID, &inboundMsg{m.ConnID, m.Method, m.Path, m.Headers}
	case "data.end", "close":
		return m.T, m.ConnID, nil
	}
	return "", "", nil
}

// ---------------------------------------------------------------------------
// per-conn state
// ---------------------------------------------------------------------------

type conn struct {
	id       string
	kind     int // kindHTTP | kindTunnel
	local    net.Conn
	dshWS    *websocket.Conn
	finished bool

	// HTTP request side
	in          *inboundMsg
	headWritten bool
	reqChunked  bool

	// response state machine
	respSent  bool
	bodyMode  string // "none" | "length" | "chunked" | "eof"
	remaining int64
	chunkSize int64
}

const (
	kindHTTP   = 1
	kindTunnel = 2
)

func (c *conn) finish() {
	if c.finished {
		return
	}
	c.finished = true
	if c.local != nil {
		_ = c.local.Close()
	}
	if c.dshWS != nil {
		_ = c.dshWS.Close()
	}
}

// ---------------------------------------------------------------------------
// host
// ---------------------------------------------------------------------------

type host struct {
	cfg     Host
	ws      *websocket.Conn
	wsMu    sync.Mutex // gorilla forbids concurrent writers
	conns   map[string]*conn
	connsMu sync.Mutex
}

func (h *host) sendCtrl(msg any) {
	b, _ := json.Marshal(msg)
	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	if h.ws != nil {
		_ = h.ws.WriteMessage(websocket.TextMessage, b)
	}
}

func (h *host) sendData(connID string, payload []byte, bin bool) {
	if len(payload) == 0 {
		return
	}
	h.wsMu.Lock()
	defer h.wsMu.Unlock()
	if h.ws != nil {
		_ = h.ws.WriteMessage(websocket.BinaryMessage, encodeDataFrame(connID, payload, bin))
	}
}

func (h *host) sendEnd(connID string) {
	h.sendCtrl(struct {
		T      string `json:"t"`
		ConnID string `json:"connId"`
	}{"data.end", connID})
}

func (h *host) getConn(id string) *conn {
	h.connsMu.Lock()
	defer h.connsMu.Unlock()
	return h.conns[id]
}

func (h *host) delConn(id string) {
	h.connsMu.Lock()
	delete(h.conns, id)
	h.connsMu.Unlock()
}

func (h *host) runOnce(ctx context.Context) error {
	u, err := url.Parse(h.cfg.Relay)
	if err != nil {
		return fmt.Errorf("bad PIKTAK_RELAY %q: %w", h.cfg.Relay, err)
	}
	q := u.Query()
	q.Set("code", h.cfg.Code)
	q.Set("v", strconv.Itoa(wire.Version))
	if h.cfg.Name != "" {
		q.Set("name", h.cfg.Name)
	}
	u.RawQuery = q.Encode()

	d := &websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 15 * time.Second,
	}
	logURL := *u
	logQuery := logURL.Query()
	logQuery.Del("code")
	logURL.RawQuery = logQuery.Encode()
	log.Printf("[wsthost] connecting to %s", logURL.String())
	ws, resp, err := d.DialContext(ctx, u.String(), nil)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("dial %s: %s", logURL.String(), resp.Status)
		}
		return fmt.Errorf("dial %s: %w", logURL.String(), err)
	}
	h.ws = ws
	defer func() { _ = ws.Close(); h.ws = nil }()
	h.conns = make(map[string]*conn)

	// The worker speaks first: hello.ok / hello.err.
	mt, raw, err := ws.ReadMessage()
	if err != nil {
		return fmt.Errorf("read hello: %w", err)
	}
	if mt != websocket.TextMessage {
		return fmt.Errorf("unexpected hello frame type")
	}
	t, _, _ := parseWorkerMsg(raw)
	switch t {
	case "hello.err":
		return fmt.Errorf("pairing rejected")
	case "hello.ok":
		log.Printf("[wsthost] paired: service=%s", h.cfg.Name)
		if h.cfg.Status != nil {
			h.cfg.Status.SetConnected(true)
			defer h.cfg.Status.SetConnected(false)
		}
	default:
		return fmt.Errorf("unexpected hello: %s", raw)
	}

	h.sendCtrl(struct {
		T     string `json:"t"`
		Local string `json:"local"`
	}{"expose", h.cfg.Local})
	log.Printf("[wsthost] exposing local=%s", h.cfg.Local)

	for {
		mt, raw, err := ws.ReadMessage()
		if err != nil {
			return err
		}
		if mt == websocket.TextMessage {
			t, connID, in := parseWorkerMsg(raw)
			switch t {
			case "inbound":
				h.onInbound(in)
			case "data.end":
				if c := h.getConn(connID); c != nil {
					h.onRequestEnd(c)
				}
			case "close":
				if c := h.getConn(connID); c != nil {
					c.finish()
					h.delConn(connID)
				}
			}
			continue
		}
		// Binary data frame.
		connID, payload, bin, ok := decodeDataFrame(raw)
		if !ok {
			continue
		}
		if c := h.getConn(connID); c != nil {
			if c.kind == kindTunnel {
				mt := websocket.BinaryMessage
				if !bin {
					mt = websocket.TextMessage
				}
				_ = c.dshWS.WriteMessage(mt, payload)
			} else {
				h.onReqBody(c, payload)
			}
		}
	}
}

// ---------------------------------------------------------------------------
// HTTP reverse-proxy
// ---------------------------------------------------------------------------

// reqStripAlways: hop-by-hop headers never forwarded to the local target.
var reqStripAlways = map[string]bool{
	"host": true, "connection": true, "content-length": true, "transfer-encoding": true,
	"upgrade": true, "keep-alive": true, "te": true, "trailer": true, "expect": true,
	"proxy-authenticate": true, "proxy-authorization": true,
}

// reqStripLoopback: browser cross-origin markers. Stripped ONLY in loopback
// compat mode (RewriteHost) — a faithful forwarder passes them through.
var reqStripLoopback = map[string]bool{
	"origin": true, "referer": true, "sec-fetch-site": true, "sec-fetch-mode": true,
	"sec-fetch-dest": true, "sec-fetch-user": true,
}

func (h *host) onInbound(in *inboundMsg) {
	if h.cfg.Status != nil {
		h.cfg.Status.AddRequest()
	}
	up := strings.ToLower(in.Headers["upgrade"])
	if strings.Contains(up, "websocket") ||
		strings.Contains(strings.ToLower(in.Headers["connection"]), "upgrade") {
		h.onInboundWsTunnel(in)
		return
	}
	log.Printf("[wsthost] inbound %s %s %s", in.ConnID, in.Method, in.Path)

	local, err := net.DialTimeout("tcp", h.cfg.Local, 10*time.Second)
	if err != nil {
		h.sendResp502(in.ConnID, fmt.Sprintf("local target: %v", err))
		return
	}
	c := &conn{id: in.ConnID, kind: kindHTTP, local: local, in: in}
	h.connsMu.Lock()
	h.conns[in.ConnID] = c
	h.connsMu.Unlock()
}

func (h *host) sendResp502(connID, reason string) {
	h.sendCtrl(struct {
		T       string            `json:"t"`
		ConnID  string            `json:"connId"`
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
	}{"resp", connID, 502, map[string]string{"content-type": "application/json"}})
	h.sendData(connID, []byte(fmt.Sprintf(`{"error":%q}`, reason)), false)
	h.sendEnd(connID)
}

func (h *host) writeRequestHead(c *conn, hasBody bool) {
	if c.headWritten {
		return
	}
	c.headWritten = true
	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s HTTP/1.1\r\n", c.in.Method, c.in.Path)
	for k, v := range c.in.Headers {
		lk := strings.ToLower(k)
		if reqStripAlways[lk] {
			continue
		}
		if reqStripLoopback[lk] && h.cfg.RewriteHost {
			continue // loopback compat: strip browser origin markers
		}
		fmt.Fprintf(&sb, "%s: %s\r\n", k, v)
	}
	// The Worker strips the browser's Host, so thost sets the upstream Host
	// (standard reverse-proxy behavior); for the loopback use case Local IS
	// loopback, which is exactly what a loopback-trusting backend wants.
	fmt.Fprintf(&sb, "Host: %s\r\n", h.cfg.Local)
	if hasBody {
		sb.WriteString("Transfer-Encoding: chunked\r\n")
		c.reqChunked = true
	}
	sb.WriteString("Connection: close\r\n\r\n")
	_, _ = io.WriteString(c.local, sb.String())
}

// onReqBody: a browser request-body chunk arrives.
func (h *host) onReqBody(c *conn, payload []byte) {
	h.writeRequestHead(c, true)
	if len(payload) == 0 {
		return
	}
	if c.reqChunked {
		fmt.Fprintf(c.local, "%x\r\n", len(payload))
		_, _ = c.local.Write(payload)
		_, _ = io.WriteString(c.local, "\r\n")
	} else {
		_, _ = c.local.Write(payload)
	}
}

// onRequestEnd: the browser finished its request; finish writing it to the
// local target, then read the response in a goroutine.
func (h *host) onRequestEnd(c *conn) {
	if c.finished {
		return
	}
	h.writeRequestHead(c, false)
	if c.reqChunked {
		_, _ = io.WriteString(c.local, "0\r\n\r\n")
	}
	go h.readResponse(c)
}

// readResponse: read the local target's response head, send resp + body data
// frames, then data.end. Runs per-conn in a goroutine.
func (h *host) readResponse(c *conn) {
	if c.finished {
		return
	}
	br := bufio.NewReader(c.local)

	// Response head.
	status := 0
	headers := map[string]string{}
	var rawHead strings.Builder
	for {
		line, err := br.ReadString('\n')
		if err != nil {
			h.teardownConn(c)
			return
		}
		rawHead.WriteString(line)
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "HTTP/") {
			parts := strings.Fields(line)
			if len(parts) >= 2 {
				status, _ = strconv.Atoi(parts[1])
			}
			continue
		}
		if i := strings.IndexByte(line, ':'); i > 0 {
			k := strings.ToLower(strings.TrimSpace(line[:i]))
			v := strings.TrimSpace(line[i+1:])
			if k == "content-length" || k == "connection" || k == "transfer-encoding" {
				continue // re-framed by the Worker / de-chunked here
			}
			if prev, ok := headers[k]; ok {
				headers[k] = prev + ", " + v
			} else {
				headers[k] = v
			}
		}
	}
	if status == 0 {
		status = 502
	}
	c.respSent = true
	h.sendCtrl(struct {
		T       string            `json:"t"`
		ConnID  string            `json:"connId"`
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
	}{"resp", c.id, status, headers})

	head := strings.ToLower(rawHead.String())
	switch {
	case c.in.Method == "HEAD" || status == 204 || status == 304 || (status >= 100 && status < 200):
		c.bodyMode = "none"
	case strings.Contains(head, "content-length:"):
		if i := strings.Index(head, "content-length:"); i >= 0 {
			rest := head[i+len("content-length:"):]
			if j := strings.IndexByte(rest, '\r'); j > 0 {
				n, _ := strconv.ParseInt(strings.TrimSpace(rest[:j]), 10, 64)
				c.remaining = n
			}
		}
		c.bodyMode = "length"
	case strings.Contains(head, "chunked"):
		c.bodyMode = "chunked"
	default:
		c.bodyMode = "eof"
	}

	switch c.bodyMode {
	case "none":
		h.sendEnd(c.id)
		h.teardownConn(c)
		return
	case "length":
		for c.remaining > 0 {
			buf := make([]byte, 64*1024)
			if int64(len(buf)) > c.remaining {
				buf = buf[:c.remaining]
			}
			n, err := br.Read(buf)
			if n > 0 {
				h.sendData(c.id, buf[:n], false)
				c.remaining -= int64(n)
			}
			if err != nil {
				break
			}
		}
	case "chunked":
		for {
			sizeLine, err := br.ReadString('\n')
			if err != nil {
				break
			}
			size, _ := strconv.ParseInt(strings.TrimSpace(strings.SplitN(sizeLine, ";", 2)[0]), 16, 64)
			if size == 0 {
				// trailers until a blank line
				for {
					tl, err := br.ReadString('\n')
					if err != nil || tl == "\r\n" || tl == "\n" {
						break
					}
				}
				break
			}
			buf := make([]byte, size)
			if _, err := io.ReadFull(br, buf); err != nil {
				break
			}
			h.sendData(c.id, buf, false)
			// trailing CRLF
			br.ReadString('\n')
		}
	case "eof":
		buf := make([]byte, 64*1024)
		for {
			n, err := br.Read(buf)
			if n > 0 {
				h.sendData(c.id, buf[:n], false)
			}
			if err != nil {
				break
			}
		}
	}
	h.sendEnd(c.id)
	h.teardownConn(c)
}

func (h *host) teardownConn(c *conn) {
	c.finish()
	h.delConn(c.id)
}

// ---------------------------------------------------------------------------
// WS event-stream tunnel
// ---------------------------------------------------------------------------

func (h *host) onInboundWsTunnel(in *inboundMsg) {
	log.Printf("[wsthost] inbound(WS-tunnel) %s %s %s", in.ConnID, in.Method, in.Path)
	target := "ws://" + h.cfg.Local + in.Path
	protos := []string{}
	if p := in.Headers["sec-websocket-protocol"]; p != "" {
		for _, s := range strings.Split(p, ",") {
			if s = strings.TrimSpace(s); s != "" {
				protos = append(protos, s)
			}
		}
	}
	d := &websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	var header http.Header
	if len(protos) > 0 {
		header = http.Header{"Sec-WebSocket-Protocol": protos}
	}
	dsh, _, err := d.Dial(target, header)
	if err != nil {
		h.sendResp502(in.ConnID, fmt.Sprintf("ws tunnel: %v", err))
		return
	}
	c := &conn{id: in.ConnID, kind: kindTunnel, dshWS: dsh}
	h.connsMu.Lock()
	h.conns[in.ConnID] = c
	h.connsMu.Unlock()

	// Tell the worker the local target upgraded (informative).
	h.sendCtrl(struct {
		T       string            `json:"t"`
		ConnID  string            `json:"connId"`
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
	}{"resp", in.ConnID, 101, map[string]string{}})

	go func() {
		for {
			mt, payload, err := dsh.ReadMessage()
			if err != nil {
				break
			}
			h.sendData(c.id, payload, mt == websocket.BinaryMessage)
		}
		h.sendEnd(c.id)
		c.finish()
		h.delConn(c.id)
	}()
}
