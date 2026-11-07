# ssh

Fork of golang.org/x/crypto/ssh with fewer syscalls per packet; changes are reported upstream.

## Provenance

Imported from golang.org/x/crypto **v0.58.0**, upstream commit
`8fdf07b3114278fb3b78c1cf1814e08bd851eb4c` (https://go.googlesource.com/crypto,
`refs/tags/v0.58.0`): the `ssh/` tree (now this module's root) and the
`internal/{poly1305,alias,testenv}` packages it needs (a Go `internal` package
cannot be imported from another module). `LICENSE` and `PATENTS` are Go's,
verbatim, as are all copyright headers.

The history is reviewable in three steps: a verbatim copy; the mechanical
rewrite of import paths to `github.com/go-filesystems/ssh`; the fix itself.
Every other public package (`cryptobyte`, `curve25519`, `chacha20`, `sha3`,
`blowfish`, `golang.org/x/term`) is still imported from golang.org/x/crypto.
