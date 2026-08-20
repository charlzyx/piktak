package relay

import (
	"io"
	"log"
	"net"
)

// splice copies bytes both ways between a (the browser socket) and b (the
// host data channel: bReader reads from it, bWriter writes to it and can be
// half-closed). It uses TCP half-close: when one direction's read hits EOF it
// CloseWrites the other side, so a server that finishes responding (e.g. dsh
// sending a chunked keep-alive reply then idling) can signal end-of-response
// to the client while the reverse direction stays open until the client closes
// too. Both sides are fully closed only after both directions are done.
//
// connID is for diagnostics only — it tags the per-direction byte counts and
// the close log so a stuck connection can be traced on the relay.
func splice(connID string, a net.Conn, bReader io.Reader, bWriter io.WriteCloser) {
	done := make(chan struct{}, 2)
	go func() {
		n, err := io.Copy(bWriter, a) // a -> b : browser -> host
		closeWrite(bWriter)
		log.Printf("splice %s: a->b EOF bytes=%d err=%v", connID, n, err)
		done <- struct{}{}
	}()
	go func() {
		n, err := io.Copy(a, bReader) // b -> a : host -> browser
		closeWrite(a)
		log.Printf("splice %s: b->a EOF bytes=%d err=%v", connID, n, err)
		done <- struct{}{}
	}()
	<-done
	<-done
	_ = a.Close()
	_ = bWriter.Close()
	log.Printf("splice %s: closed both", connID)
}

// closeWrite half-closes the write side of a TCP conn so the peer sees EOF
// without tearing down the whole socket. No-op for non-TCP writers.
func closeWrite(c any) {
	if w, ok := c.(interface{ CloseWrite() error }); ok {
		_ = w.CloseWrite()
	}
}
