package main

import (
	"context"
	"log"
	"os"
	"time"

	"pulse/sdk/go"
)

func main() {
	host := os.Getenv("PULSE_HOST")
	if host == "" {
		host = "http://127.0.0.1:8080"
	}
	client, err := pulse.New(host, os.Getenv("PULSE_PROJECT_KEY"))
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err = client.Capture(ctx, pulse.Event{ID: "example-order-001", Name: "subscription_started", DistinctID: "example-user", Properties: map[string]any{"plan": "Pro", "source": "go-example"}})
	if err != nil {
		log.Fatal(err)
	}
	log.Print("Event accepted. Open the event explorer to see it.")
}
