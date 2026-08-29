# Contributing to apm2go

Thanks for taking the time. Bug reports and small, focused pull requests are
both welcome.

## Before you build

Docker is the only requirement. Everything the binary embeds — the web UI, the
bootstrap agent jar, the OpenTelemetry Java agent, the eBPF binary and the
attach helper — is built or fetched inside `build/Dockerfile.build`, so you do
not need Node.js, a JDK or a Go toolchain on your machine.

```bash
make test          # gofmt, go vet and the unit tests, in a container
make deb           # .deb only
make rpm           # .rpm only
make image         # container image only
```

Each build target produces one deliverable and leaves the others alone. The
packages are always x86-64; only `make image` takes an architecture, with
`ARCH=amd64` or `ARCH=arm64`.

Iterating on the Go code alone is faster with a local toolchain, but the
embedded assets have to exist for the package to compile. `make test` is the
answer that always works; a bare `go test ./...` on the host will not, unless
you have populated `internal/assets/files/` yourself.

## Reporting a bug

apm2go attaches to processes it did not start, on hosts it did not configure, so
the report is only actionable with the surroundings included:

- distribution and version, kernel version (`uname -r`), and architecture
- how apm2go is installed: package or container
- for a Java problem: the JVM vendor and version of the target process
- the relevant log lines — `journalctl -u apm2go` or `docker logs apm2go`
- `sudo apm2go list`, which reports what discovery found and whether each
  process can be attached to

If the problem is that something does not appear in the UI, say which of the
three it is: discovery did not find the process, the attach failed, or the
attach succeeded and no telemetry arrived. They have entirely different causes.

## Pull requests

- Open an issue first for anything that changes behaviour, adds a dependency, or
  touches the attach path. The attach path runs as root against other people's
  processes; changes there need discussion before code.
- Keep the change focused. One concern per pull request.
- `make test` has to pass. Add tests for new behaviour — the existing packages
  show the expected style.
- Match the surrounding code. Comments here explain why a thing is done, not
  what the line does; keep that habit.
- Commits should read as a sentence: what changed, and why.

## Scope

apm2go is deliberately one binary with no external dependencies. Proposals that
add a required service — a database, a broker, a separate collector — are
outside its scope, however useful they might be on their own.

## Licence

By contributing you agree that your contribution is licensed under the Apache
License 2.0, the same terms as the rest of the project. See [LICENSE](LICENSE).
