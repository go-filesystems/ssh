# ssh

Fork of golang.org/x/crypto/ssh with fewer syscalls per packet; changes are reported upstream (see `UPSTREAM.md`, nothing sent yet).

## Provenance

Imported from golang.org/x/crypto commit
`2f1f834330b2d36ab3db8bfb33aaa1ac16794588`
(`v0.58.1-0.20261009181718-2f1f834330b2`, https://go.googlesource.com/crypto),
the first commit that fixes GO-2026-5932. No tag contains it yet; its diff from
v0.58.0 is `openpgp/go.mod` only, so the `ssh/` files are those of v0.58.0.
The `ssh/` tree is this module's root, with the `internal/{poly1305,alias,testenv}`
packages it needs (a Go `internal` package cannot be imported from another
module). `LICENSE` and `PATENTS` are Go's, verbatim, as are all copyright
headers. Rebase onto the first tag that contains the fix when there is one.

The history is reviewable in steps: a verbatim copy; the mechanical rewrite of
import paths to `github.com/go-filesystems/ssh` (plus go.mod); the fix itself;
the upstream report. Every other package (`cryptobyte`, `curve25519`,
`chacha20`, `sha3`, `blowfish`, `golang.org/x/term`) is still imported from
golang.org/x/crypto.

## What differs from upstream

One change: the transport writes through a 64 KiB buffer and flushes once per
`Channel.Write` instead of once per packet, so a 32 KiB packet is one `write(2)`
instead of two (96 writes per MiB sent become 32). The API is unchanged.

## CI

Test, race detector, a fuzz smoke run, govulncheck, vet, gofmt of the files this
fork changed (imported upstream files are left as they are), and a coverage
report without a gate.
