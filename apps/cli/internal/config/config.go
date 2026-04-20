package config

import (
	"errors"
	"os"
	"path/filepath"

	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	RelayURL       string `toml:"relay_url"`
	Identity       string `toml:"identity"`
	KeysDir        string `toml:"keys_dir"`
	ParentDomain   string `toml:"parent_domain"`
}

func DefaultConfig() Config {
	keysDir, _ := KeysDir()
	return Config{
		RelayURL:       os.Getenv("RELAY_URL"),
		Identity:       os.Getenv("IDENTITY"),
		KeysDir:        getenvWithFallback("KEYS_DIR", keysDir),
		ParentDomain:   os.Getenv("PARENT_DOMAIN"),
	}
}

func Load() (Config, error) {
	path, err := ConfigPath()
	if err != nil {
		return Config{}, err
	}
	cfg := DefaultConfig()
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return Config{}, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return Config{}, err
	}
	if cfg.RelayURL == "" {
		cfg.RelayURL = os.Getenv("RELAY_URL")
	}
	if cfg.Identity == "" {
		cfg.Identity = os.Getenv("IDENTITY")
	}
	if cfg.KeysDir == "" {
		cfg.KeysDir = getenvWithFallback("KEYS_DIR", "")
	}
	if cfg.ParentDomain == "" {
		cfg.ParentDomain = os.Getenv("PARENT_DOMAIN")
	}
	if cfg.KeysDir == "" {
		if keysDir, err := KeysDir(); err == nil {
			cfg.KeysDir = keysDir
		}
	}
	return cfg, nil
}

func Save(cfg Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := toml.Marshal(cfg)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

func ConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".eurything", "config.toml"), nil
}

func KeysDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".eurything", "keys"), nil
}

func getenvWithFallback(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
