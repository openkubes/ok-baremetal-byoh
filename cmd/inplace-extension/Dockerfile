# GO_VERSION is passed by "make docker-buildx-extension" from the toolchain directive in
# go.mod, so the image builds with the same Go as the controller and the agent.
ARG GO_VERSION
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION} AS builder

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Set by buildx for each platform of --platform.
ARG TARGETOS
ARG TARGETARCH

RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -buildvcs=false \
    -ldflags="-s -w" \
    -o /out/inplace-extension \
    ./cmd/inplace-extension

FROM gcr.io/distroless/static:nonroot

COPY --from=builder /out/inplace-extension \
    /inplace-extension

USER 65532:65532

ENTRYPOINT ["/inplace-extension"]
