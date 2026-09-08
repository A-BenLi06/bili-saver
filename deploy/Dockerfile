# bili-saver 核心镜像：静态二进制 + ffmpeg
FROM golang:1.27-alpine AS build
WORKDIR /src
ENV GOPROXY=https://goproxy.cn,direct CGO_ENABLED=0
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN go build -trimpath -ldflags "-s -w" -o /out/bili-saver ./cmd/bili-saver

FROM alpine:3.20
RUN apk add --no-cache ffmpeg ca-certificates tzdata
COPY --from=build /out/bili-saver /usr/local/bin/bili-saver
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["bili-saver", "-config", "/data/config.yaml"]
CMD ["serve"]
