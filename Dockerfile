FROM golang:1.26.10-alpine AS build

ARG VERSION=dev
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w -X main.version=${VERSION}" -o /out/webhook .

FROM alpine:3.22

RUN apk add --no-cache ca-certificates \
    && adduser -D -u 65532 -g 65532 nonroot
COPY --from=build /out/webhook /webhook
USER 65532:65532

EXPOSE 8080
ENTRYPOINT ["/webhook"]
