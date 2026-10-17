#!/usr/bin/env sh
# Regenerate the Go bindings from proto/ with the pinned plugins.
set -eu
cd "$(dirname "$0")/.."
test "$(protoc-gen-go --version)" = "protoc-gen-go v1.36.11"
test "$(protoc-gen-go-grpc --version)" = "protoc-gen-go-grpc 1.6.2"
buf lint
buf generate
