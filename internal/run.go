package buzz

import (
	"log"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"time"
)

func Run(configPath, adminDir string) error {
	cfg, err := loadConfig(configPath)
	if err != nil {
		return err
	}
	srv, err := newServer(cfg)
	if err != nil {
		return err
	}
	srv.adminDir = adminDir

	httpServer := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv,
		ReadHeaderTimeout: 15 * time.Second,
	}

	log.Printf("Buzz listening on http://%s", cfg.Server.Addr)
	return httpServer.ListenAndServe()
}

func init() {
	if seed, err := strconv.ParseInt(os.Getenv("LOCAL_PROXY_RAND_SEED"), 10, 64); err == nil {
		rand.Seed(seed)
		return
	}
	rand.Seed(time.Now().UnixNano())
}
