package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/zhigu34/one-nvr/internal/config"
	"github.com/zhigu34/one-nvr/internal/database"
	"github.com/zhigu34/one-nvr/internal/hardware"
	"github.com/zhigu34/one-nvr/internal/secrets"
	"github.com/zhigu34/one-nvr/internal/tlsmanager"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

func deploymentCommand(args []string) (bool, error) {
	if len(args) == 0 {
		return false, nil
	}
	switch args[0] {
	case "ready-check":
		if len(args) != 2 {
			return true, fmt.Errorf("ready-check URL required")
		}
		client := &http.Client{Timeout: 3 * time.Second}
		r, e := client.Get(args[1])
		if e != nil {
			return true, fmt.Errorf("service not ready")
		}
		defer r.Body.Close()
		if r.StatusCode != 200 {
			return true, fmt.Errorf("service not ready")
		}
		return true, nil
	case "env-value", "render-deployment":
		envFile, output, key, check := "", "", "", false
		for i := 1; i < len(args); i++ {
			if args[i] == "--check" {
				check = true
				continue
			}
			if i+1 >= len(args) {
				return true, fmt.Errorf("missing option value")
			}
			switch args[i] {
			case "--env-file":
				envFile = args[i+1]
			case "--output-dir":
				output = args[i+1]
			case "--key":
				key = args[i+1]
			default:
				return true, fmt.Errorf("unknown option")
			}
			i++
		}
		f, e := os.Open(envFile)
		if e != nil {
			return true, fmt.Errorf("env file unavailable")
		}
		defer f.Close()
		v, e := config.ReadEnv(f)
		if e != nil {
			return true, e
		}
		d, e := config.BuildDeployment(v)
		if e != nil {
			return true, e
		}
		if args[0] == "env-value" {
			c, _ := config.FromValues(v)
			value := ""
			switch key {
			case "data":
				value = c.DataDir
			case "tls":
				value = c.TLSDir
			case "storage":
				value = v["ONE_NVR_STORAGE_ROOT"]
				if value == "" {
					value = "/srv/one-nvr-storage"
				}
			default:
				return true, fmt.Errorf("invalid public configuration key")
			}
			fmt.Println(value)
			return true, nil
		}
		if !check {
			secret, e := secrets.Load("/data")
			if e != nil {
				return true, e
			}
			v["ONE_NVR_POSTGRES_PASSWORD"], e = secret.ComponentCredential("postgres")
			if e != nil {
				return true, e
			}
			d, e = config.BuildDeployment(v)
			if e == nil {
				e = writeComponentConfigs(v, secret)
			}
			if e != nil {
				return true, e
			}
		}
		if output == "" {
			return true, fmt.Errorf("output directory required")
		}
		if !filepath.IsAbs(output) {
			return true, fmt.Errorf("output must be absolute")
		}
		if e = os.MkdirAll(output, 0700); e != nil {
			return true, e
		}
		data, e := d.JSON()
		if e != nil {
			return true, e
		}
		sum := sha256.Sum256(data)
		name := "compose-" + hex.EncodeToString(sum[:]) + ".json"
		if e = immutableFile(filepath.Join(output, name), data, 0600); e != nil {
			return true, e
		}
		manifest := struct {
			File     string   `json:"file"`
			Services []string `json:"services"`
			Images   []string `json:"images"`
			Frigate  bool     `json:"frigate"`
			OpenList bool     `json:"openlist"`
		}{File: name, Services: d.Names()}
		for _, n := range manifest.Services {
			manifest.Images = append(manifest.Images, d.Services[n].Image)
			if e = replaceFile(filepath.Join(output, "image-"+n), []byte(d.Services[n].Image), 0600); e != nil {
				return true, e
			}
		}
		_, manifest.Frigate = d.Services["frigate"]
		_, manifest.OpenList = d.Services["openlist"]
		b, _ := json.Marshal(manifest)
		if e = replaceFile(filepath.Join(output, "manifest.json"), b, 0600); e != nil {
			return true, e
		}
		if e = replaceFile(filepath.Join(output, "compose.json"), data, 0600); e != nil {
			return true, e
		}
		// Public bounded shell-readable service list, no credentials or shell evaluation.
		if e = replaceFile(filepath.Join(output, "services"), []byte(strings.Join(manifest.Services, "\n")+"\n"), 0600); e != nil {
			return true, e
		}
		if e = replaceFile(filepath.Join(output, "images"), []byte(strings.Join(manifest.Images, "\n")+"\n"), 0600); e != nil {
			return true, e
		}
		return true, nil
	case "init-runtime":
		return true, initRuntime()
	case "bootstrap-tls":
		c, e := config.Load()
		if e != nil {
			return true, e
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		db, e := database.Open(ctx, c.DatabaseURL)
		if e != nil {
			return true, fmt.Errorf("database unavailable")
		}
		defer db.Pool.Close()
		input := ""
		if c.TLSDir != "" {
			input = "/tls-input"
		}
		s := tlsmanager.New(db, nil, c.DataDir, c.PublicURL, input)
		if e = s.Initialize(ctx); e != nil {
			return true, e
		}
		return true, s.Bootstrap(ctx)
	case "discover-hardware":
		inv, e := hardware.Discover(context.Background())
		if e != nil {
			inv.Known = false
		}
		return true, json.NewEncoder(os.Stdout).Encode(inv)
	}
	return false, nil
}
func immutableFile(path string, data []byte, mode os.FileMode) error {
	old, e := os.ReadFile(path)
	if e == nil {
		if !reflect.DeepEqual(old, data) {
			return fmt.Errorf("immutable output differs")
		}
		return nil
	}
	if !os.IsNotExist(e) {
		return e
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if e != nil {
		return e
	}
	_, e = f.Write(data)
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	return closeErr
}
func replaceFile(path string, data []byte, mode os.FileMode) error {
	if old, e := os.ReadFile(path); e == nil && reflect.DeepEqual(old, data) {
		return nil
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".pending-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(mode); e != nil {
		f.Close()
		return e
	}
	if _, e = f.Write(data); e != nil {
		f.Close()
		return e
	}
	if e = f.Sync(); e != nil {
		f.Close()
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	return os.Rename(f.Name(), path)
}
func initRuntime() error {
	c, e := config.Load()
	if e != nil {
		return e
	}
	root := c.DataDir
	if e = validateRuntimeIdentity(root); e != nil {
		return e
	}
	entries, readErr := os.ReadDir(root)
	if readErr != nil {
		return readErr
	}
	allowed := map[string]bool{"secrets": true, "gateway": true, "tls": true, "runtime": true, "hardware": true, "postgres": true, "mqtt": true, "frigate": true, "openlist": true}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return fmt.Errorf("data root contains unrelated entries; choose a dedicated directory")
		}
	}
	for _, name := range []string{"", "secrets", "gateway", "tls", "tls/versions", "runtime", "hardware", "postgres", "mqtt", "frigate", "openlist"} {
		path := filepath.Join(root, name)
		if fi, e := os.Lstat(path); e == nil {
			if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("dedicated data path must be a real directory")
			}
		} else if os.IsNotExist(e) {
			if e = os.Mkdir(path, 0700); e != nil {
				return e
			}
		} else {
			return e
		}
		if name != "postgres" && name != "mqtt" && name != "openlist" && name != "frigate" {
			if e = os.Chmod(path, 0700); e != nil {
				return e
			}
			if e = os.Chown(path, 10001, 10001); e != nil {
				return e
			}
		}
	}
	secret, e := secrets.Init(root)
	if e != nil {
		return e
	}
	if e = os.Chown(filepath.Join(root, "secrets/one-nvr.json"), 10001, 10001); e != nil {
		return e
	}
	proxy, e := secret.ComponentCredential("gateway")
	if e != nil {
		return e
	}
	path := filepath.Join(root, "gateway/proxy-token")
	if e = immutableFile(path, []byte(proxy), 0600); e != nil {
		return e
	}
	if e = os.Chown(path, 10001, 10001); e != nil {
		return e
	}
	return nil
}

func validateRuntimeIdentity(root string) error {
	if _, e := os.Lstat(filepath.Join(root, "secrets/one-nvr.json")); e == nil {
		return nil
	} else if !os.IsNotExist(e) {
		return e
	}
	for _, name := range []string{"postgres", "gateway", "tls/versions", "runtime", "hardware", "frigate", "mqtt", "openlist"} {
		entries, e := os.ReadDir(filepath.Join(root, name))
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if len(entries) > 0 {
			return fmt.Errorf("persistent state exists but secrets are missing; restore the original secrets")
		}
	}
	return nil
}
