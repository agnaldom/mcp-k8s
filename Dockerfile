# Multi-stage build: compile in a full Go toolchain, ship a static
# distroless image. The binary is pure Go (client-go, no cgo), so a
# scratch/distroless base is enough and no shell or package manager
# exists in the final image.
FROM golang:1.27 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
ARG COMMIT=none
RUN CGO_ENABLED=0 go build \
      -trimpath \
      -ldflags "-s -w -X github.com/agnaldom/mcp-k8s/internal/version.Version=${VERSION} -X github.com/agnaldom/mcp-k8s/internal/version.Commit=${COMMIT}" \
      -o /out/mcp-k8s ./cmd/mcp-k8s

# gcr.io/distroless/static:nonroot runs as UID 65532 ("nobody"). There is
# no shell, no kubectl, no credential helpers — nothing for an injected
# prompt to exec (spec §6.1: the server never shells out anyway).
FROM gcr.io/distroless/static:nonroot
COPY --from=build /out/mcp-k8s /usr/local/bin/mcp-k8s
USER nonroot:nonroot
ENTRYPOINT ["/usr/local/bin/mcp-k8s"]
CMD ["serve", "--transport", "stdio"]
