package main

import (
	"context"
	"fmt"
	"log"
	"time"

	v1 "github.com/catalystcommunity/websocks/v1"
)

func main() {
	fmt.Println("Starting WebSocket client test")
	
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	
	conn, err := v1.Connect(ctx, "ws://localhost:8081")
	if err != nil {
		log.Fatal("Failed to connect:", err)
	}
	defer conn.Close()
	
	fmt.Println("Connected to WebSocket server")
	
	// Send a test message
	testMessage := "Hello from Go client!"
	if _, err := conn.Write([]byte(testMessage)); err != nil {
		log.Fatal("Failed to send message:", err)
	}
	
	fmt.Printf("Sent: %s\n", testMessage)
	
	// Read response
	buffer := make([]byte, 1024)
	n, err := conn.Read(buffer)
	if err != nil {
		log.Fatal("Failed to read response:", err)
	}
	
	response := string(buffer[:n])
	fmt.Printf("Received: %s\n", response)
	
	expectedResponse := fmt.Sprintf("Node echo: %s", testMessage)
	if response == expectedResponse {
		fmt.Println("✓ Go client test successful!")
	} else {
		log.Fatalf("✗ Unexpected response. Expected: %s, Got: %s", expectedResponse, response)
	}
}