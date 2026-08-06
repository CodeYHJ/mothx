package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// EnvConfig contains environment variables injected into bash and skill tools.
type EnvConfig struct {
	Vars map[string]string `json:"vars"`
}

func GlobalEnvPath() string { return filepath.Join(ConfigDir(), "env.json") }

func LoadEnv() *EnvConfig {
	c := &EnvConfig{Vars: map[string]string{}}
	data, err := os.ReadFile(GlobalEnvPath())
	if err == nil {
		_ = json.Unmarshal(data, c)
	}
	if c.Vars == nil {
		c.Vars = map[string]string{}
	}
	return c
}

func (c *EnvConfig) List() map[string]string {
	out := make(map[string]string, len(c.Vars))
	for k, v := range c.Vars {
		out[k] = v
	}
	return out
}

func (c *EnvConfig) Set(key, value string) error {
	key = strings.TrimSpace(key)
	if key == "" || strings.ContainsAny(key, "=\x00\r\n") {
		return fmt.Errorf("invalid environment variable name")
	}
	if c.Vars == nil {
		c.Vars = map[string]string{}
	}
	c.Vars[key] = value
	return c.Save()
}
func (c *EnvConfig) Unset(key string) error { delete(c.Vars, strings.TrimSpace(key)); return c.Save() }
func (c *EnvConfig) Clear() error           { c.Vars = map[string]string{}; return c.Save() }
func (c *EnvConfig) Save() error {
	if err := os.MkdirAll(ConfigDir(), 0700); err != nil {
		return err
	}
	keys := make([]string, 0, len(c.Vars))
	for k := range c.Vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	ordered := make(map[string]string, len(keys))
	for _, k := range keys {
		ordered[k] = c.Vars[k]
	}
	data, err := json.MarshalIndent(EnvConfig{Vars: ordered}, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := GlobalEnvPath() + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, GlobalEnvPath())
}
