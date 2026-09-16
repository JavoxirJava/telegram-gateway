# Contributing

Bug reports, setup improvements and pull requests are welcome. Describe the
expected behavior, actual behavior and a minimal reproduction. Use synthetic
Telegram profiles and messages in tests; do not attach personal account exports.

Before a pull request:

```bash
go fmt ./...
go vet ./...
go test -race -count=1 ./...
```

For account isolation, OAuth, synchronization, media or database changes, run the
isolated integration suite described in the README. Never point it at a live
database. Apply new schema changes as new numbered migrations; do not rewrite
an applied migration because deployment verifies its checksum.

Keep changes focused and explain how they were verified. Do not commit runtime
data or credentials. Security reports belong in the private reporting workflow
described in [SECURITY.md](SECURITY.md).

Contributions are made under the repository's [MIT license](LICENSE). Retain
the original notices when incorporating third-party material.
