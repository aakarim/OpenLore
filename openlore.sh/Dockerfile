FROM golang:1.26-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# Embed openlore.sh docs
RUN rm -rf assets/lore/* && \
    cp -r openlore.sh/docs/* assets/lore/ && \
    cp openlore.sh/openlore.yml assets/config/openlore.yml

RUN CGO_ENABLED=0 go build -o openlore ./cmd/openlore

FROM alpine:3.19
RUN apk add --no-cache ca-certificates
WORKDIR /app
RUN mkdir -p /app/published
COPY --from=builder /app/openlore .
COPY --from=builder /app/openlore.sh/skills ./skills
COPY --from=builder /app/openlore.sh/motd.txt ./motd.txt
# Third-party license notices are embedded in the binary (assets/legal) and
# served at /legal by the HTTP server.

EXPOSE 2222 8080
VOLUME ["/app/published"]

CMD ["./openlore", "--skills-dir", "./skills"]
