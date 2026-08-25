FROM golang:1.26.1-alpine AS builder

WORKDIR /src
COPY scripts/performance/mock-upstream/main.go .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /mock-upstream main.go

FROM alpine:3.22
COPY --from=builder /mock-upstream /mock-upstream
EXPOSE 8080
ENTRYPOINT ["/mock-upstream"]
