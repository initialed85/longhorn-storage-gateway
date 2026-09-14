# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath -ldflags='-s -w' -o /out/longhorn-nfs-gateway ./cmd/controller

FROM gcr.io/distroless/static:nonroot
COPY --from=builder /out/longhorn-nfs-gateway /longhorn-nfs-gateway
USER 65532:65532
ENTRYPOINT ["/longhorn-nfs-gateway"]
