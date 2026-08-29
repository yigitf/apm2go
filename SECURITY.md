# Security policy

## Reporting a vulnerability

Please do not open a public issue for a security problem.

Report it privately through GitHub's
[security advisory form](https://github.com/yigitf/apm2go/security/advisories/new),
which opens a report visible only to the maintainers.

Include what the problem allows, how to reproduce it, and the version and
platform you saw it on. You can expect an acknowledgement within a week and an
assessment of severity and a fix timeline after that. Please give us a chance to
release a fix before disclosing publicly.

## What is in scope

apm2go runs as root and attaches to processes it did not start, so its
privileged surface is worth stating plainly. All of the following are in scope:

- Anything that lets an unprivileged local user influence what apm2go attaches
  to, or what runs inside the attached process.
- Weaknesses in the privilege drop: apm2go performs the attach itself from a
  short-lived child process holding the target's credentials, never from the
  long-running root process.
- The ingest endpoints (OTLP/gRPC on 4317, OTLP/HTTP on 4318) and their token
  check.
- The web interface and HTTP API on port 8080.
- The RPM and DEB post-install scripts and the systemd unit.

## What is not

- The privileges apm2go asks for. Root, `--pid=host`, and the eBPF capabilities
  are required for it to do its job, and are documented with the reason for each
  in [INSTALL.md](INSTALL.md). Running it on a host means trusting it.
- Exposing the web interface or the ingest ports to an untrusted network. They
  are intended for a trusted network; apm2go does not terminate TLS or
  authenticate users. Put it behind something that does.
- Vulnerabilities in the bundled third-party components listed in
  [NOTICE](NOTICE). Report those upstream; tell us as well if apm2go needs to
  ship a newer version.

## Supported versions

apm2go is pre-1.0. Fixes go to the latest release only.
