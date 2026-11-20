FROM --platform=$BUILDPLATFORM golang:1.26 AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

ARG TARGETOS=linux
ARG TARGETARCH=arm64

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false \
    -ldflags="-s -w" \
    -o /out/no-replacement-guard \
    ./cmd/no-replacement-guard

FROM gcr.io/distroless/static:nonroot

COPY --from=builder /out/no-replacement-guard \
    /no-replacement-guard

USER 65532:65532

ENTRYPOINT ["/no-replacement-guard"]
