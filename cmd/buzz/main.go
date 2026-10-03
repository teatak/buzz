package main

import (
	"flag"
	"log"

	buzz "github.com/teatak/buzz/internal"
)

func main() {
	configPath := flag.String("config", "config.yaml", "config file path")
	adminDir := flag.String("admin-dir", "admin/dist", "built admin frontend directory")
	flag.Parse()

	if err := buzz.Run(*configPath, *adminDir); err != nil {
		log.Fatal(err)
	}
}
