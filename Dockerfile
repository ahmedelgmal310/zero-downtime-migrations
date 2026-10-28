FROM golang:1.27.3-alpine3.24@sha256:d200d2dd76b03a99b52651fd1325aff7dcd35422c35c01bac56571b0507f3deb AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY cmd/ cmd/
COPY internal/ internal/
COPY migrations/ migrations/

# APP_VERSION selects the release (v1, v2, v3). Each release is a separate,
# immutable image tag, exactly like three consecutive deploys of a real service.
# The migrations are embedded, so the migration Job runs the reviewed SQL.
ARG APP_VERSION=v1
ARG TARGETOS=linux
ARG TARGETARCH=amd64

RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -buildvcs=false \
      -ldflags="-s -w -X main.version=${APP_VERSION}" \
      -o /out/zdm ./cmd/zdm

FROM gcr.io/distroless/static-debian13:nonroot@sha256:18f4cc26c9ffa7f20a359298be7deadac5c607c8e03a02b8119f1a5fc2f82c99

ARG APP_VERSION=v1
LABEL org.opencontainers.image.source="https://github.com/ahmedelgmal310/zero-downtime-migrations" \
      org.opencontainers.image.title="zdm" \
      org.opencontainers.image.description="Users API, migration and backfill jobs for zero-downtime schema changes" \
      org.opencontainers.image.licenses="MIT" \
      org.opencontainers.image.version="${APP_VERSION}"

COPY --from=build /out/zdm /zdm
USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/zdm"]
CMD ["serve"]
