FROM golang:1.27-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
COPY web ./web
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /product-analytics ./cmd/product-analytics

FROM alpine:3.22
RUN addgroup -g 10001 productanalytics && adduser -D -u 10001 -G productanalytics productanalytics
USER productanalytics
WORKDIR /app
COPY --from=build /product-analytics /app/product-analytics
EXPOSE 8080
ENTRYPOINT ["/app/product-analytics"]
CMD ["--addr", "0.0.0.0:8080"]
