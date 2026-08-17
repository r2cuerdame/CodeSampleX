# x/sync syncmap.Map in v0.22.0

`golang.org/x/sync/syncmap.Map` is the historical prototype for the standard
library concurrent map. In x/sync v0.22.0 it is a type alias for `sync.Map`,
not a generic `Map[K, V]` implementation.

That distinction has two practical consequences measured by this contract:

- values and keys still use `any`, and indexing the type with type arguments
  fails to compile;
- the available method set follows the Go runtime's `sync.Map`. This sample is
  pinned to Go 1.26 and therefore includes `CompareAndSwap` and `Clear`.

The positive contract checks the zero value, ordinary operations, concurrent
`LoadOrStore`, compare-and-swap behavior, early `Range` termination, and reuse
after `Clear`. A build-tagged negative probe checks the non-generic type error
without making the main contract fail.
