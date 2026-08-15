# Build context = repo root (compose truyền context: ..) — cần copy ui/ ngoài go-server/
FROM golang:1.25-alpine AS builder
WORKDIR /src
COPY go-server/go.mod go-server/go.sum ./
RUN go mod download
COPY go-server/ ./
RUN CGO_ENABLED=0 go build -o /out/server ./cmd/server/

FROM alpine:3.20
RUN apk add --no-cache ca-certificates
WORKDIR /app
COPY --from=builder /out/server /app/server
COPY ui/ /app/ui/
EXPOSE 8080
ENTRYPOINT ["/app/server"]
