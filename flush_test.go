// Copyright 2026 go-filesystems. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package ssh

import (
	"bytes"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingConn counts the Write calls (one write(2) each on a TCP connection)
// and bytes that reach the network.
type countingConn struct {
	net.Conn
	writes, bytes atomic.Int64
}

func (c *countingConn) Write(p []byte) (int, error) {
	c.writes.Add(1)
	c.bytes.Add(int64(len(p)))
	return c.Conn.Write(p)
}

// serveSession runs a server whose connection is counted, hands the session
// channel to handler, and returns the client's end of that channel.
func serveSession(t *testing.T, handler func(ch Channel)) (client Channel, cc *countingConn) {
	t.Helper()
	c1, c2, err := netPipe()
	if err != nil {
		t.Fatal(err)
	}
	cc = &countingConn{Conn: c1}
	var wg sync.WaitGroup
	t.Cleanup(wg.Wait)
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer c1.Close()
		conf := ServerConfig{NoClientAuth: true}
		conf.AddHostKey(testSigners["ed25519"])
		conn, chans, reqs, err := NewServerConn(cc, &conf)
		if err != nil {
			t.Errorf("server handshake: %v", err)
			return
		}
		go DiscardRequests(reqs)
		for nc := range chans {
			ch, in, err := nc.Accept()
			if err != nil {
				t.Errorf("accept: %v", err)
				return
			}
			go DiscardRequests(in)
			handler(ch)
		}
		conn.Wait()
	}()
	cconf := &ClientConfig{User: "u", HostKeyCallback: InsecureIgnoreHostKey()}
	conn, chans, reqs, err := NewClientConn(c2, "", cconf)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	go DiscardRequests(reqs)
	go func() {
		for nc := range chans {
			nc.Reject(Prohibited, "")
		}
	}()
	client, in, err := openChannel(conn)
	if err != nil {
		t.Fatal(err)
	}
	go DiscardRequests(in)
	return client, cc
}

func openChannel(conn Conn) (Channel, <-chan *Request, error) {
	return conn.OpenChannel("session", nil)
}

// A Write of many packets leaves in few writes on the connection: the packets
// share the transport's buffer and go out in its 64 KiB units.
func TestBurstOfPacketsLeavesInFewWrites(t *testing.T) {
	const size = 8 << 20
	data := make([]byte, size)
	client, cc := serveSession(t, func(ch Channel) {
		go func() {
			if _, err := ch.Write(data); err != nil {
				t.Errorf("write: %v", err)
			}
			ch.Close()
		}()
	})
	n, err := io.Copy(io.Discard, client)
	if err != nil || n != size {
		t.Fatalf("read %d bytes, err %v", n, err)
	}
	perMiB := float64(cc.writes.Load()) / (size >> 20)
	t.Logf("%d writes for %d MiB: %.1f writes/MiB (upstream: about 96)", cc.writes.Load(), size>>20, perMiB)
	// One write per 32 KiB packet would be 32 per MiB; the buffer does better.
	if perMiB > 32 {
		t.Errorf("%.1f writes/MiB, want at most 32", perMiB)
	}
}

// Packets written one Write each, as a caller that does not batch does, still
// cost one write each: no split at 4 KiB, no flush per half packet.
func TestOnePacketPerWriteCostsOneWrite(t *testing.T) {
	const size = 8 << 20
	chunk := make([]byte, 32<<10)
	client, cc := serveSession(t, func(ch Channel) {
		go func() {
			for range size / len(chunk) {
				if _, err := ch.Write(chunk); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
			ch.Close()
		}()
	})
	if n, err := io.Copy(io.Discard, client); err != nil || n != size {
		t.Fatalf("read %d bytes, err %v", n, err)
	}
	perMiB := float64(cc.writes.Load()) / (size >> 20)
	t.Logf("%.1f writes/MiB", perMiB)
	if perMiB > 34 {
		t.Errorf("%.1f writes/MiB, want about 32", perMiB)
	}
}

// Latency: when Write returns, the bytes are on the network. Nothing waits for
// a later packet, a timer or a full buffer, for a lone small write, for a
// request (keepalive), or for the end of a channel.
func TestLoneWritesAreFlushedBeforeTheyReturn(t *testing.T) {
	ready := make(chan Channel, 1)
	client, cc := serveSession(t, func(ch Channel) { ready <- ch })
	ch := <-ready

	before := cc.writes.Load()
	if _, err := ch.Write([]byte("x")); err != nil {
		t.Fatal(err)
	}
	if got := cc.writes.Load() - before; got != 1 {
		t.Fatalf("a 1-byte Write made %d writes on the connection before returning, want 1", got)
	}
	var b [1]byte
	done := make(chan error, 1)
	go func() { _, err := io.ReadFull(client, b[:]); done <- err }()
	select {
	case err := <-done:
		if err != nil || b[0] != 'x' {
			t.Fatalf("read %q, %v", b, err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the byte did not arrive")
	}

	before = cc.writes.Load()
	if _, err := ch.SendRequest("keepalive@example.com", false, nil); err != nil {
		t.Fatal(err)
	}
	if got := cc.writes.Load() - before; got != 1 {
		t.Fatalf("a request made %d writes before returning, want 1", got)
	}

	before = cc.writes.Load()
	if err := ch.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if got := cc.writes.Load() - before; got != 1 {
		t.Fatalf("CloseWrite made %d writes before returning, want 1", got)
	}
}

// A Write larger than the peer's window waits for window adjustments, which
// the peer sends only for what it has received: the buffer is flushed before
// the wait, or this never finishes.
func TestWriteLargerThanTheWindowCompletes(t *testing.T) {
	const size = 5 * channelWindowSize / 2
	data := make([]byte, size)
	client, _ := serveSession(t, func(ch Channel) {
		go func() {
			if _, err := ch.Write(data); err != nil {
				t.Errorf("write: %v", err)
			}
			ch.Close()
		}()
	})
	done := make(chan int64, 1)
	go func() { n, _ := io.Copy(io.Discard, client); done <- n }()
	select {
	case n := <-done:
		if n != size {
			t.Fatalf("read %d, want %d", n, size)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("a Write larger than the window did not complete")
	}
}

// reserveFlushing flushes once, before it waits, and not at all when window is
// available.
func TestReserveFlushesBeforeWaiting(t *testing.T) {
	w := window{Cond: newCond()}
	flushes := 0
	go func() {
		w.waitWriterBlocked()
		time.Sleep(10 * time.Millisecond)
		w.add(10)
	}()
	n, err := w.reserveFlushing(10, func() error { flushes++; return nil })
	if err != nil || n != 10 || flushes != 1 {
		t.Fatalf("reserve = %d, %v after %d flushes, want 10, nil, 1", n, err, flushes)
	}
	w.add(5)
	if n, err := w.reserveFlushing(5, func() error { flushes++; return nil }); err != nil || n != 5 || flushes != 1 {
		t.Fatalf("with window available: %d, %v after %d flushes", n, err, flushes)
	}
}

// Deferring the flush changes when bytes are written, never which bytes: with
// the same random source, packets written one flush each and packets written
// deferred then flushed once produce identical streams.
func TestDeferredWritesProduceTheSameStream(t *testing.T) {
	packets := make([][]byte, 20)
	for i := range packets {
		packets[i] = bytes.Repeat([]byte{byte(i + 1)}, 1+i*3001)
		packets[i][0] = msgChannelData
	}
	stream := func(deferred bool) []byte {
		buf := &closerBuffer{}
		tr := newTransport(buf, zeroReader{}, true)
		for _, p := range packets {
			p = append([]byte(nil), p...)
			var err error
			if deferred {
				err = tr.writePacketDeferred(p)
			} else {
				err = tr.writePacket(p)
			}
			if err != nil {
				t.Fatal(err)
			}
		}
		if err := tr.flush(); err != nil {
			t.Fatal(err)
		}
		return buf.Bytes()
	}
	a, b := stream(false), stream(true)
	if !bytes.Equal(a, b) {
		t.Fatalf("streams differ: %d and %d bytes", len(a), len(b))
	}
}

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) { clear(p); return len(p), nil }

// FuzzTransportReadPacket feeds arbitrary bytes to the packet reader (no
// cipher, as during the key exchange): it may fail, never panic or hang, and
// never return an empty packet without an error.
func FuzzTransportReadPacket(f *testing.F) {
	f.Add([]byte{})
	f.Add([]byte{0, 0, 0, 12, 4, msgIgnore, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0})
	f.Add([]byte{0, 0, 0, 5, 4, msgNewKeys, 0, 0, 0})
	f.Add([]byte{0xff, 0xff, 0xff, 0xff})
	f.Fuzz(func(t *testing.T, in []byte) {
		tr := newTransport(&closerBuffer{Buffer: *bytes.NewBuffer(in)}, zeroReader{}, false)
		for range 8 {
			p, err := tr.readPacket()
			if err != nil {
				return
			}
			if len(p) == 0 {
				t.Fatal("empty packet without an error")
			}
		}
	})
}
