package main

import (
	"context"
	"log"
	"os"
	"time"

	productanalytics "product-analytics/sdk/go"
)

func main() {
	host := os.Getenv("PRODUCT_ANALYTICS_HOST")
	if host == "" {
		host = "http://127.0.0.1:8080"
	}
	client, err := productanalytics.New(host, os.Getenv("PRODUCT_ANALYTICS_PROJECT_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.Capture(ctx, productanalytics.Event{ID: "example-order-001", Name: "subscription_started", DistinctID: "example-user", Properties: map[string]any{"plan": "Pro", "source": "go-example"}})
	if err != nil {
		log.Fatal(err)
	}
	log.Print("Event accepted. Open the event explorer to see it.")
}
