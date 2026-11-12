# Static core binary plus ffmpeg. CI supplies the exact Go version from go.mod.
ARG GO_VERSION=1.27.1
FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
ARG GOPROXY=https://goproxy.cn,direct
ENV GOPROXY=${GOPROXY} CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -mod=readonly -trimpath \
    -ldflags "-s -w -X main.version=${VERSION}" -o /out/bili-saver ./cmd/bili-saver

FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates tzdata
COPY --from=build /out/bili-saver /usr/local/bin/bili-saver
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["bili-saver", "-config", "/data/config.yaml"]
CMD ["serve"]
