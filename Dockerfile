FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /pulse ./cmd/pulse

FROM alpine:3.22
RUN addgroup -g 10001 pulse && adduser -D -u 10001 -G pulse pulse
USER pulse
WORKDIR /app
COPY --from=build /pulse /app/pulse
EXPOSE 8080
ENTRYPOINT ["/app/pulse"]
CMD ["--addr", "0.0.0.0:8080"]
