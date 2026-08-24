# Documentation

What is here is about the server as it is. The design proposals and the
per-phase briefs that drove the Go rewrite were removed once the rewrite
landed: they described work that is finished, against a Rust tree that no
longer exists, and a document that describes neither the code nor the
deployment is a document that quietly goes wrong.

`git log` still has them if the reasoning behind a decision is ever needed.

| Document | What it covers |
|---|---|
| [`CUTOVER.md`](CUTOVER.md) | what the port changed for a deployment and for the clients attached to one |
| [`RISKS.md`](RISKS.md) | what is likely to break and what to do about it, in order of what it costs |
| [`CONFORMANCE.md`](CONFORMANCE.md) | RFC 4918 WebDAV conformance, asserted by tests in this repository |
| [`FOOTPRINT.md`](FOOTPRINT.md) | memory and timing, measured on one host against one share |
| [`JAIL-PROOF.md`](JAIL-PROOF.md) | the sandbox proved across two architectures, two kernels and both policies |
| [`releases/`](releases/) | release notes, one file per tag, read by the publish workflow |

For running it, the two readmes at the repository root are the entry point:
[English](../README.md), [한국어](../README.ko.md). The compose file and
`deploy/sc.toml.example` carry the rest, at the point an operator meets it.
