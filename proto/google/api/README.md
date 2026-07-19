# Vendored google/api protos

`annotations.proto` and `http.proto` are vendored unmodified from
[googleapis/googleapis](https://github.com/googleapis/googleapis)
(Apache-2.0, same license as this repository) so that `buf generate` works
fully offline. They provide the `google.api.http` annotations that map the
`prukka.v1.Control` gRPC service onto REST routes via grpc-gateway.

Do not edit these files; refresh them from upstream instead.
