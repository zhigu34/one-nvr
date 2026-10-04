package hardware

import (
	"context"
	"os"
	"path/filepath"
	"strings"
)

// The deploy tool binds the target Docker daemon's sysfs read-only at /host/sys.
func Discover(ctx context.Context) (Inventory, error) { return DiscoverAt(ctx, "/host/sys") }
func DiscoverAt(ctx context.Context, root string) (Inventory, error) {
	entries, e := os.ReadDir(filepath.Join(root, "class/drm"))
	if e != nil {
		return Inventory{}, e
	}
	out := Inventory{Known: true, Devices: []Device{}}
	for _, x := range entries {
		if e = ctx.Err(); e != nil {
			return out, e
		}
		if !strings.HasPrefix(x.Name(), "renderD") {
			continue
		}
		dir := filepath.Join(root, "class/drm", x.Name(), "device")
		device, e := filepath.EvalSymlinks(dir)
		if e != nil {
			continue
		}
		rel, e := filepath.Rel(root, device)
		if e != nil || strings.HasPrefix(rel, "..") {
			continue
		}
		vendor, e := os.ReadFile(filepath.Join(device, "vendor"))
		if e != nil {
			continue
		}
		v := strings.TrimPrefix(strings.TrimSpace(string(vendor)), "0x")
		out.Devices = append(out.Devices, Device{ID: filepath.Base(device), Vendor: v, Node: "/dev/dri/" + x.Name(), Accelerator: v == "8086"})
	}
	return out, nil
}
