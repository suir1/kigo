# syntax=docker/dockerfile:1

FROM golang:1.27.0-alpine AS builder

WORKDIR /src
RUN apk add --no-cache ca-certificates
ARG VERSION=dev
ARG COMMIT=unknown
ARG DATE=unknown

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w -X github.com/suir1/kigo/internal/version.Version=${VERSION} -X github.com/suir1/kigo/internal/version.Commit=${COMMIT} -X github.com/suir1/kigo/internal/version.Date=${DATE}" -o /out/kigo ./cmd/kigo

FROM alpine:3.24

RUN apk add --no-cache ca-certificates \
	&& addgroup -S kigo \
	&& adduser -S -G kigo -H -h /nonexistent kigo

COPY --from=builder /out/kigo /usr/local/bin/kigo

USER kigo:kigo
EXPOSE 9100/tcp 3478/udp
ENTRYPOINT ["/usr/local/bin/kigo"]
CMD ["serve"]
