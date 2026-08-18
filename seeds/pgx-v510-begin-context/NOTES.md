# pgx v5.10.0 transaction context boundary

`pgx.Conn.Begin(ctx)` uses `ctx` only while sending the `BEGIN` command. It does
not watch that context for the rest of the transaction and does not automatically
roll back when the context is cancelled.

The contract runs against a tiny deterministic PostgreSQL protocol stub. It
records the actual `BEGIN`, `COMMIT`, and `ROLLBACK` messages without requiring a
database service or network access. Production code should normally use this
shape:

```go
tx, err := conn.Begin(ctx)
if err != nil {
    return err
}
defer tx.Rollback(ctx) // pgx.ErrTxClosed is safe to ignore after Commit.

// Perform work using tx and the appropriate operation contexts.
return tx.Commit(ctx)
```
