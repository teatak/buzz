package buzz

import (
	"os"
	"strconv"

	"gopkg.in/yaml.v3"
)

func loadConfig(path string) (Config, error) {
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg, err
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return cfg, err
	}
	if cfg.Server.Addr == "" {
		cfg.Server.Addr = "127.0.0.1:9622"
	}
	if cfg.Upstream.BaseURL == "" {
		cfg.Upstream.BaseURL = "https://generativelanguage.googleapis.com"
	}
	if cfg.Upstream.Timeout == "" {
		cfg.Upstream.Timeout = "10m"
	}
	if cfg.Database.Driver == "" {
		cfg.Database.Driver = "postgres"
	}
	if envURL := os.Getenv("BUZZ_DATABASE_URL"); envURL != "" {
		cfg.Database.Driver = "postgres"
		cfg.Database.URL = envURL
	}
	if envURL := os.Getenv("BUZZ_REDIS_URL"); envURL != "" {
		cfg.Redis.URL = envURL
	}
	if envAddr := os.Getenv("BUZZ_REDIS_ADDR"); envAddr != "" {
		cfg.Redis.Addr = envAddr
	}
	if envPassword := os.Getenv("BUZZ_REDIS_PASSWORD"); envPassword != "" {
		cfg.Redis.Password = envPassword
	}
	if envDB := os.Getenv("BUZZ_REDIS_DB"); envDB != "" {
		db, err := strconv.Atoi(envDB)
		if err != nil {
			return cfg, err
		}
		cfg.Redis.DB = db
	}
	if cfg.Retry.MaxAttempts <= 0 {
		cfg.Retry.MaxAttempts = 8
	}
	if cfg.Retry.CooldownSeconds <= 0 {
		cfg.Retry.CooldownSeconds = 120
	}
	return cfg, nil
}
