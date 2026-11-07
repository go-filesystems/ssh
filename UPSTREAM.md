# Report for golang.org/x/crypto/ssh (not sent)

Nothing here has been sent upstream. This file is the text and the patch
outline for an issue and a Gerrit change, to be filed after review.

## Problem

Every SSH packet is encrypted into the transport's `bufio.Writer` and flushed
on its own (`transport.go`, `connectionState.writePacket`), and that writer has
the default size of 4096 bytes (`newTransport`). A channel packet of 32 KiB
therefore costs two `write(2)` calls on the connection: `bufio.Writer.Write`
fills its buffer with the head of the packet, flushes, and writes the rest
directly. A `Channel.Write` of N packets costs 2N. OpenSSH queues packets in
its output buffer and writes the whole buffer at once
(`packet.c: ssh_packet_write_poll`; `sftp-server.c: process_requests`).

Seen in a CPU profile of an SFTP server over loopback (go-fileshare/fileshare
PR 111): most of the server's time was in the write syscall.

## Change

Three parts, 133 lines added and 8 removed in `channel.go`, `common.go`,
`handshake.go` and `transport.go` (commit "perf: one flush per channel Write"):

1. `newTransport` makes the writer 64 KiB (`writeBufferSize`), so a packet is
   not cut at 4 KiB.
2. `connectionState.writePacket` no longer flushes. `transport.writePacket`
   is write + flush, so every existing caller (key exchange, authentication,
   requests, window adjustments, a lone data packet) has its packet on the wire
   when it returns, exactly as before. New `transport.writePacketDeferred` and
   `transport.flush` expose the two halves.
3. `channel.WriteExtended` hands the packets of one `Write` to the connection
   with `writePacketDeferred` and flushes once when it returns, and before it
   waits for remote window (the peer may need these very bytes before it sends
   the adjustment). `handshakeTransport` implements both, under its existing
   lock, through an optional `deferredPacketConn` interface so other
   `keyingTransport`/`packetConn` implementations are untouched.

## Why it is safe

- The bytes and their order are unchanged: encryption still happens at write
  time under `handshakeTransport.mu`; only when `write(2)` is called differs
  (`TestDeferredWritesProduceTheSameStream`).
- Latency: a `Write` that is a single packet is flushed before it returns;
  so are `SendRequest` (keepalive) and `CloseWrite`
  (`TestLoneWritesAreFlushedBeforeTheyReturn`). Nothing waits for a timer or
  for a full buffer.
- Key exchange (RFC 4253 section 7, 9): KEXINIT, NEWKEYS and EXT_INFO are
  written through the flushing `writePacket`, so they are on the wire at the
  point upstream relies on, and a KEXINIT flushes whatever deferred bytes
  precede it. While a key exchange is in progress `handshakeTransport.write`
  queues every packet (as before) and `handshakeTransport.flush` is a no-op,
  so it never touches the write buffer that the exchange owns without the
  lock. A key change takes effect on the next packet encrypted, and packets
  already in the buffer were encrypted under the old keys, which is the order
  they are sent in.
- The window case: `window.reserveFlushing` flushes once before it blocks
  (`TestReserveFlushesBeforeWaiting`, `TestWriteLargerThanTheWindowCompletes`).

## Numbers

Counting `net.Conn` on the server side, one 8 MiB `Channel.Write`
(`TestBurstOfPacketsLeavesInFewWrites`): 96 writes/MiB upstream, 17 with the
change. One 32 KiB `Write` per packet: 96 upstream, 33 with the change.

go-filesystems/sftp v0.10.1, 32 MiB file, 64 READs in flight, real SSH on
loopback, Apple M4 Max, `abbench` interleaved, 8 rounds kept at 1-minute load
at most 8, upstream x/crypto v0.58.0 against this fork (both ends):

| | upstream | fork |
|---|---|---|
| writes/MiB (server connection, exact) | 96.1 | 32.1 (1 session), 32.8 (4) |
| time per fetch, 1 session | 19.25 ms | 15.93 ms (-17%, p=0.010) |
| time per fetch, 4 sessions | 49.45 ms | 42.55 ms (-14%, p<0.001) |
| MB/s, 1 / 4 sessions | 1.62 / 2.53 GiB/s | 1.96 / 2.94 GiB/s (+21% / +16%) |
| bytes allocated per op | unchanged | unchanged |

The spread of the throughput rows is large (26-46% for 1 session); the time rows
and the write counts are the reliable ones.

## What a Gerrit CL would contain

Title: `ssh: flush once per channel Write and size the write buffer to a packet`.

- The three changes above, without the fork's test helpers.
- Tests to add to `ssh`: `TestDeferredWritesProduceTheSameStream`
  (transport_test.go), a counting-conn test for writes per packet and one for
  the lone-write latency (session_test.go style, using `netPipe`), and
  `TestReserveFlushesBeforeWaiting` (common_test.go).
- The commit message states the numbers above, cites RFC 4253 section 7 for
  the key exchange reasoning, and notes the API is unchanged (all new names are
  unexported).
- Still to do before filing: run the upstream trybots; decide whether 64 KiB
  is the right size (it is 2 x the default channel packet; memory is 64 KiB per
  connection against 4 KiB now); consider the same for `bufReader` (4 KiB
  reads), not done here; file an issue first, as the Go project asks.
