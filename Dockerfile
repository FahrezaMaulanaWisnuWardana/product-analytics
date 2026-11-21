FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pulse ./cmd/pulse

FROM alpine:3.22
# BusyBox wget ships with Alpine and backs the /readyz probe below (same probe as compose.yaml).
LABEL org.opencontainers.image.title="pulse-product-analytics" \
      org.opencontainers.image.description="Product analytics service: CGO-free static binary, non-root, /readyz health check"
RUN addgroup -g 10001 pulse && adduser -D -u 10001 -G pulse pulse
USER pulse
WORKDIR /app
COPY --from=build /pulse /app/pulse
EXPOSE 8080
HEALTHCHECK --interval=10s --timeout=3s --start-period=15s --retries=5 \
  CMD wget -q -O- http://127.0.0.1:8080/readyz >/dev/null || exit 1
ENTRYPOINT ["/app/pulse"]
CMD ["--addr", "0.0.0.0:8080"]
