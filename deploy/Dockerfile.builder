# Multi-stage build for csx-builder: static binary on alpine (CSX-451).
#
# A separate image from Dockerfile.server on purpose: the standalone Builder
# is a different OS process with its own PostgreSQL connection pool, and an
# image that could run either binary would make it too easy for a compose
# edit to point both services at the same entrypoint and silently rebuild
# the shared-process topology this split exists to remove.
FROM golang:1.26-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags="-s -w" -o /out/csx-builder ./cmd/csx-builder

FROM alpine:3.22
ARG CSX_VERSION=dev
ARG CSX_BUILD_VERSION=
ARG CSX_BUILT_AT=
ARG CSX_ENV=development
ENV CSX_VERSION=$CSX_VERSION
ENV CSX_BUILD_VERSION=$CSX_BUILD_VERSION
ENV CSX_BUILT_AT=$CSX_BUILT_AT
ENV CSX_ENV=$CSX_ENV
LABEL org.opencontainers.image.revision=$CSX_VERSION
LABEL org.opencontainers.image.version=$CSX_BUILD_VERSION
LABEL org.opencontainers.image.created=$CSX_BUILT_AT
RUN apk add --no-cache ca-certificates tzdata wget && adduser -D -H csx
COPY --from=build /out/csx-builder /usr/local/bin/csx-builder
WORKDIR /
USER csx
EXPOSE 8091
ENTRYPOINT ["csx-builder"]
CMD ["run"]
