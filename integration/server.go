package main

import (
	"fmt"
	"log"
	"net"

	v1 "github.com/catalystcommunity/websocks/v1"
)

func echoHandler(conn net.Conn) error {
	defer conn.Close()
	
	fmt.Println("New WebSocket connection from:", conn.RemoteAddr())
	
	buffer := make([]byte, 1024)
	for {
		n, err := conn.Read(buffer)
		if err != nil {
			fmt.Println("Connection closed:", err)
			return err
		}
		
		message := string(buffer[:n])
		fmt.Printf("Received: %s\n", message)
		
		response := fmt.Sprintf("Echo: %s", message)
		if _, err := conn.Write([]byte(response)); err != nil {
			fmt.Println("Write error:", err)
			return err
		}
	}
}

func main() {
	fmt.Println("Starting WebSocket server on :8080/ws")
	
	server := v1.NewServer(":8080", "/ws", echoHandler)
	
	if err := server.Start(); err != nil {
		log.Fatal("Server failed:", err)
	}
}