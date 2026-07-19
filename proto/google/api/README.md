# Vendored google/api protos

`annotations.proto` and `http.proto` are vendored unmodified from
[googleapis/googleapis](https://github.com/googleapis/googleapis)
under Apache-2.0, keeping their upstream license headers, so that
`buf generate` works fully offline. The rest of this repository is licensed
separately (GPL-3.0-or-later per the root `LICENSE`, with `drivers/linux/`
GPL-2.0-only per `drivers/linux/LICENSE`); Apache-2.0 is inbound-compatible
with GPLv3, which is what
permits redistributing these files here under their own terms. They provide
the `google.api.http` annotations that map the `prukka.v1.Control` gRPC
service onto REST routes via grpc-gateway.

Do not edit these files; refresh them from upstream instead.
