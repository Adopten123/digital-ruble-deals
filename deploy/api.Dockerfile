FROM golang:1.27.2-alpine3.24@sha256:f92b6ef800e499660581efdabdf25d9d817a9d124eaf900924f0504e7e27e12d AS build
WORKDIR /src
ENV CGO_ENABLED=0 GOTOOLCHAIN=local
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ cmd/
COPY internal/ internal/
RUN go build -trimpath -ldflags="-s -w" -o /out/poaktu ./cmd/poaktu

FROM alpine:3.24.2@sha256:294b683cb724975bec92580e1e685676bd4b50bda910ddb8c51d4cabeaec77e6 AS runtime
RUN addgroup -g 10001 app
RUN adduser -D -H -u 10001 -G app app
RUN mkdir -p /app /var/lib/poaktu/keys /var/lib/poaktu/documents
RUN chown -R 10001:10001 /app /var/lib/poaktu
WORKDIR /app
COPY --from=build --chown=10001:10001 /out/poaktu /app/poaktu
COPY --from=build /src/internal/core/documentfile/THIRD_PARTY_NOTICES.md /usr/share/licenses/poaktu/documentfile.txt
COPY --chown=10001:10001 migrations/ /app/migrations/
USER 10001:10001
EXPOSE 8080
ENTRYPOINT ["/app/poaktu"]
CMD ["serve"]
