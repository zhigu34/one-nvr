package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	if handled, err := hardwareCommand(os.Args[1:]); handled {
		return err
	}
	if handled, err := verifyCommand(os.Args[1:]); handled {
		return err
	}
	if handled, err := deploymentCommand(os.Args[1:]); handled {
		return err
	}
	if len(os.Args) == 2 && os.Args[1] == "init-secrets" {
		c, err := config.Load()
		if err != nil {
			return err
		}
		if err = validateRuntimeIdentity(c.DataDir); err != nil {
			return err
		}
		state, err := secrets.Init(c.DataDir)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(struct {
			SiteID string `json:"site_id"`
		}{string(state.SiteID)})
	}
	if len(os.Args) == 2 && os.Args[1] == "setup-token" {
		c, err := config.Load()
		if err != nil {
			return err
		}
		state, err := secrets.Load(c.DataDir)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		db, err := database.Open(ctx, c.DatabaseURL)
		if err != nil {
			return fmt.Errorf("database unavailable")
		}
		defer db.Pool.Close()
		var initialized bool
		if err := db.Pool.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM sites)").Scan(&initialized); err != nil {
			return fmt.Errorf("database not initialized; run migrate first")
		}
		if initialized {
			return fmt.Errorf("setup token already consumed")
		}
		fmt.Fprintln(os.Stdout, state.SetupToken)
		return nil
	}

	if len(os.Args) == 2 && os.Args[1] == "migrate" {
		c, err := config.Load()
		if err != nil {
			return err
		}
		if c.DatabaseURL == "" {
			return fmt.Errorf("ONE_NVR_DATABASE_URL is required")
		}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		db, err := database.Open(ctx, c.DatabaseURL)
		if err != nil {
			return fmt.Errorf("database unavailable")
		}
		defer db.Pool.Close()
		return database.Migrate(ctx, db)
	}
	if len(os.Args) != 2 || os.Args[1] != "validate-env" {
		return fmt.Errorf("usage: admin validate-env < .env | migrate | init-secrets | setup-token")
	}
	c, err := config.Parse(os.Stdin)
	if err != nil {
		return err
	}
	if err = c.Validate(); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		PublicURL       string `json:"public_url"`
		MediaHost       string `json:"media_host"`
		RTCPort         int    `json:"rtc_port"`
		FrigateEnabled  bool   `json:"frigate_enabled"`
		OpenListEnabled bool   `json:"openlist_enabled"`
		HardwareProfile string `json:"hardware_profile"`
	}{c.PublicURL, c.MediaHost, c.RTCPort, c.FrigateEnabled, c.OpenListEnabled, c.HardwareProfile})
}
