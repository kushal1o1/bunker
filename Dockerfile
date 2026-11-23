# The bunker agent image: bunker, the Postgres client and tar, running as an
# unprivileged user. See docs/agent.md.

FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
ARG TARGETOS
ARG TARGETARCH
ARG VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags "-s -w -X github.com/kushal1o1/bunker/internal/cli.version=${VERSION}" \
    -o /out/bunker ./cmd/bunker

FROM alpine:3.23
# postgresql18-client: the newest pg_dump, which can dump older servers too.
# tzdata: correct local time in snapshot logs when TZ is set.
RUN apk add --no-cache postgresql18-client tar ca-certificates tzdata \
    && adduser -D -H -u 10001 -s /sbin/nologin bunker
COPY --from=build /out/bunker /usr/local/bin/bunker
USER 10001:10001
ENTRYPOINT ["bunker"]
CMD ["agent", "run"]
