#!/usr/bin/env sh
# Install the exact code-generation and quality tools validation uses.
set -eu
go install github.com/bufbuild/buf/cmd/buf@v1.72.0
go install google.golang.org/protobuf/cmd/protoc-gen-go@v1.36.11
go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@v1.6.2
go install honnef.co/go/tools/cmd/staticcheck@v0.7.0
go install golang.org/x/vuln/cmd/govulncheck@v1.6.0
