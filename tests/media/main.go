// The Docker-only media acceptance runner uses synthetic sources exclusively.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zhigu34/one-nvr/internal/media/probe"
	"github.com/zhigu34/one-nvr/internal/media/zlm"
)

const syntheticSecret = "isolated-media-fixture-only"

var streams = []string{"01887644-36d3-45d6-8d8b-5a4c5b9ebfea", "c8f92b1a-22e5-4c8c-a3ca-7f66d8c2d101", "c8f92b1a-22e5-4c8c-a3ca-7f66d8c2d102", "c8f92b1a-22e5-4c8c-a3ca-7f66d8c2d103"}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "media acceptance failed:", err)
		os.Exit(1)
	}
}
func run() error {
	if len(os.Args) < 2 {
		return fmt.Errorf("expected fixture or contract")
	}
	if os.Args[1] == "fixture" {
		return fixture()
	}
	if os.Args[1] == "prepare-publish" {
		return prepareMediaSession("main")
	}
	if os.Args[1] == "publish" {
		return publishAcceptance()
	}
	if os.Args[1] == "prepare-switch" || os.Args[1] == "prepare-recovery" || os.Args[1] == "prepare-import" {
		return prepareMediaSession("switch")
	}
	if os.Args[1] == "switch" {
		return switchAcceptance()
	}
	if os.Args[1] == "import" {
		return importAcceptance()
	}
	if os.Args[1] == "recovery" {
		return recoveryAcceptance()
	}
	if os.Args[1] == "prepare-probe" {
		return preparePoolProbe()
	}
	if os.Args[1] == "probe" {
		return poolProbeAcceptance()
	}
	if os.Args[1] == "contract" {
		return contract()
	}
	return fmt.Errorf("unknown acceptance mode")
}
func fixture() error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	closeRedirect, err := startRedirectFixture(ctx)
	if err != nil {
		return err
	}
	defer closeRedirect()
	var processes []*exec.Cmd
	var publishers sync.WaitGroup
	defer func() {
		cancel()
		publishers.Wait()
	}()
	type publisherExit struct {
		index int
		err   error
	}
	exits := make(chan publisherExit, len(streams))
	for i, stream := range streams {
		size := "320x180"
		if i%2 != 0 {
			size = "160x90"
		}
		c := exec.CommandContext(ctx, "/usr/bin/ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "error", "-re", "-f", "lavfi", "-i", "testsrc2=size="+size+":rate=5", "-c:v", "libx264", "-threads", "1", "-preset", "ultrafast", "-tune", "zerolatency", "-g", "5", "-rtsp_transport", "tcp", "-f", "rtsp", "rtsp://camera:554/one_nvr/"+stream)
		// Never print raw FFmpeg stderr or upstream URLs.
		if err := c.Start(); err != nil {
			return fmt.Errorf("synthetic publisher unavailable")
		}
		processes = append(processes, c)
		publishers.Add(1)
		go func() { defer publishers.Done(); exits <- publisherExit{index: i, err: c.Wait()} }()
	}
	// This isolated fixture can stop exactly publisher zero after a successful
	// test, so the real source-switch rollback runs against a failed camera.
	var requestedStop [4]atomic.Bool
	mux := http.NewServeMux()
	mux.HandleFunc("POST /stop-one", func(w http.ResponseWriter, r *http.Request) {
		requestedStop[0].Store(true)
		if err := processes[0].Process.Signal(os.Interrupt); err != nil {
			w.WriteHeader(503)
			return
		}
		w.Write([]byte(`{"stopped":true}`))
	})
	mux.HandleFunc("POST /stop-all", func(w http.ResponseWriter, r *http.Request) {
		for i, process := range processes {
			requestedStop[i].Store(true)
			if err := process.Process.Signal(os.Interrupt); err != nil {
				w.WriteHeader(503)
				return
			}
		}
		w.Write([]byte(`{"stopped":true}`))
	})
	listener, err := net.Listen("tcp", ":8557")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go server.Serve(listener)
	defer server.Close()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case result := <-exits:
			if !requestedStop[result.index].Load() {
				return fmt.Errorf("synthetic publisher stopped")
			}
		}
	}
}

type completion struct {
	VHost    string  `json:"vhost"`
	App      string  `json:"app"`
	Stream   string  `json:"stream"`
	Server   string  `json:"mediaServerId"`
	Path     string  `json:"file_path"`
	Size     int64   `json:"file_size"`
	Start    int64   `json:"start_time"`
	Duration float64 `json:"time_len"`
}

func contract() error {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	var mutex sync.Mutex
	var completions []completion
	mux := http.NewServeMux()
	mux.HandleFunc("/complete", func(w http.ResponseWriter, r *http.Request) {
		var c completion
		if json.NewDecoder(io.LimitReader(r.Body, 65537)).Decode(&c) != nil {
			w.WriteHeader(400)
			return
		}
		mutex.Lock()
		completions = append(completions, c)
		mutex.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"code":0,"msg":"success"}`))
	})
	listener, err := net.Listen("tcp", ":8083")
	if err != nil {
		return err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go server.Serve(listener)
	defer server.Close()
	client, err := zlm.New("http://zlm", syntheticSecret, nil)
	if err != nil {
		return err
	}
	runner := probe.Runner{FFmpeg: "/usr/bin/ffmpeg", FFprobe: "/usr/bin/ffprobe"}
	var refs []zlm.ProxyRef
	defer func() {
		for _, r := range refs {
			client.RemoveProxy(context.Background(), r)
		}
	}()
	cameraIPs, err := net.DefaultResolver.LookupIP(ctx, "ip4", "camera")
	if err != nil || len(cameraIPs) != 1 {
		return fmt.Errorf("camera fixture address unavailable")
	}
	evidence := map[string]any{"zlm_digest": "25ecf6c4a55e72bc495be15c8480f0b0cef58c712e5c9f0596452777b41bf344", "sample": "two synthetic H264 main/sub streams", "first_frames": 0, "native_files": []any{}}
	for _, stream := range streams {
		key := zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: stream}
		var ref zlm.ProxyRef
		for attempts := 0; attempts < 15; attempts++ {
			ref, err = client.AddProxy(ctx, zlm.ProxyInput{Key: key, URL: "rtsp://" + cameraIPs[0].String() + ":554/one_nvr/" + stream, Transport: "tcp"})
			if err == nil {
				break
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Second):
			}
		}
		if err != nil {
			return fmt.Errorf("fixed-image addStreamProxy contract failed")
		}
		refs = append(refs, ref)
		raw, err := probe.InternalURL(key, "")
		if err != nil {
			return err
		}
		frame, err := runner.FirstFrame(ctx, raw)
		if err != nil || !frame.FirstFrame {
			// Only the synthetic Docker test image contains this diagnostic.
			// This URL has no credentials and cannot refer to a user camera.
			diagnosticCtx, stop := context.WithTimeout(ctx, 10*time.Second)
			command := exec.CommandContext(diagnosticCtx, "/usr/bin/ffmpeg", "-nostdin", "-hide_banner", "-loglevel", "info", "-threads", "2", "-rtsp_transport", "tcp", "-timeout", "8000000", "-i", raw, "-map", "0:v:0", "-an", "-vf", "showinfo", "-frames:v", "1", "-progress", "pipe:1", "-f", "null", "-")
			command.Stdout = os.Stdout
			command.Stderr = os.Stderr
			command.Run()
			stop()
			return fmt.Errorf("actual first-frame decode failed")
		}
		snapshot, err := client.Inspect(ctx, key)
		if err != nil || snapshot.Recording {
			return fmt.Errorf("test stream unexpectedly records")
		}
		evidence["first_frames"] = evidence["first_frames"].(int) + 1
	}
	key := refs[0].Key
	work := "/storage/pool/.work/zlm/" + key.Stream
	if err := client.StartRecord(ctx, key, work, 60); err != nil {
		return fmt.Errorf("actual startRecord failed")
	}
	snap, err := client.Inspect(ctx, key)
	if err != nil || !snap.Recording {
		return fmt.Errorf("actual recording flag not true")
	}
	// No active reader while recording: demand behavior must not stop the pull.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(66 * time.Second):
	}
	if err := client.StopRecord(ctx, key); err != nil {
		return fmt.Errorf("actual stopRecord failed")
	}
	snap, err = client.Inspect(ctx, key)
	if err != nil || snap.Recording {
		return fmt.Errorf("recording flag stayed true")
	}
	var completed []completion
	for i := 0; i < 50; i++ {
		mutex.Lock()
		completed = append([]completion(nil), completions...)
		mutex.Unlock()
		if len(completed) >= 2 {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if len(completed) < 2 {
		return fmt.Errorf("60-second and final tail hooks missing")
	}
	files := make([]any, 0, len(completed))
	for _, c := range completed {
		if c.Stream != key.Stream || c.App != key.App || c.VHost != key.VHost || c.Server != "one-nvr-media-contract" || c.Start <= 0 || c.Duration <= 0 || c.Size <= 0 {
			return fmt.Errorf("native hook identity/fields inconsistent")
		}
		if !strings.HasPrefix(c.Path, work+"/") || filepath.Ext(c.Path) != ".mp4" {
			return fmt.Errorf("customized_path escaped configured work directory")
		}
		if !strings.HasPrefix(filepath.Base(c.Path), time.Unix(c.Start, 0).UTC().Format("2006-01-02-15-04-05")+"-") {
			return fmt.Errorf("native working timestamp is not UTC")
		}
		f, err := os.Open(c.Path)
		if err != nil {
			return fmt.Errorf("native completed file unreadable")
		}
		info, statErr := f.Stat()
		if statErr != nil {
			f.Close()
			return fmt.Errorf("completed file identity unavailable")
		}
		owner, ok := info.Sys().(*syscall.Stat_t)
		if !ok || owner.Uid != 10001 {
			f.Close()
			return fmt.Errorf("completed media is not owned by Worker UID")
		}
		verified, err := runner.InspectMP4(ctx, f)
		f.Close()
		if err != nil || !verified.Readable || verified.Size != c.Size {
			return fmt.Errorf("completed MP4 structure invalid")
		}
		relative, _ := filepath.Rel(work, c.Path)
		files = append(files, map[string]any{"relative_path": relative, "duration_seconds": verified.Duration.Seconds(), "bytes": verified.Size, "hook_duration_seconds": c.Duration, "hook_start": c.Start})
	}
	evidence["native_files"] = files
	redirectDenied, err := checkRedirect(ctx, client)
	if err != nil {
		return err
	}
	evidence["rtsp_redirect_denied"] = redirectDenied
	if err := os.MkdirAll("/evidence", 0755); err != nil {
		return err
	}
	f, err := os.Create("/evidence/contract.json")
	if err != nil {
		return err
	}
	defer f.Close()
	if err := json.NewEncoder(f).Encode(evidence); err != nil {
		return err
	}
	if !redirectDenied {
		return fmt.Errorf("fixed ZLM follows RTSP redirect to forbidden internal target; source activation gate remains closed")
	}
	fmt.Println("Fixed-image pull, four first frames, no implicit recording, 60-second/tail MP4 hooks and structure PASS")
	return nil
}

func checkRedirect(ctx context.Context, client *zlm.Client) (bool, error) {
	var connections atomic.Int64
	forbidden, err := net.Listen("tcp", ":8555")
	if err != nil {
		return false, err
	}
	defer forbidden.Close()
	go func() {
		for {
			c, e := forbidden.Accept()
			if e != nil {
				return
			}
			connections.Add(1)
			c.Close()
		}
	}()
	fixtureIPs, err := net.DefaultResolver.LookupIP(ctx, "ip4", "fixture")
	if err != nil || len(fixtureIPs) != 1 {
		return false, fmt.Errorf("redirect origin fixture missing")
	}
	key := zlm.StreamKey{VHost: "__defaultVhost__", App: "one_nvr", Stream: "d5934ab7-1098-45fa-bb1d-b28eddf206f9"}
	ref, err := client.AddProxy(ctx, zlm.ProxyInput{Key: key, URL: "rtsp://" + fixtureIPs[0].String() + ":8554/redirect", Transport: "tcp"})
	if err == nil {
		client.RemoveProxy(context.Background(), ref)
		return false, nil
	}
	httpClient := &http.Client{Timeout: 2 * time.Second}
	response, err := httpClient.Get("http://" + fixtureIPs[0].String() + ":8556/count")
	if err != nil {
		return false, fmt.Errorf("redirect origin evidence unavailable")
	}
	defer response.Body.Close()
	var origin struct {
		Requests int64 `json:"requests"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 1024)).Decode(&origin) != nil || origin.Requests < 1 {
		return false, fmt.Errorf("redirect origin not reached; containment unproven")
	}
	return connections.Load() == 0, nil
}

func startRedirectFixture(ctx context.Context) (func(), error) {
	sentinel := os.Getenv("ONE_NVR_MEDIA_FIXTURE_SENTINEL")
	if sentinel == "" {
		sentinel = "runner"
	}
	if sentinel != "runner" && sentinel != "worker" {
		return nil, fmt.Errorf("invalid private fixture sentinel")
	}
	runnerIPs, err := net.DefaultResolver.LookupIP(ctx, "ip4", sentinel)
	if err != nil || len(runnerIPs) != 1 {
		return nil, fmt.Errorf("sentinel fixture address unavailable")
	}
	var requests atomic.Int64
	redirect, err := net.Listen("tcp", ":8554")
	if err != nil {
		return nil, err
	}
	go func() {
		for {
			c, e := redirect.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				r := bufio.NewReader(c)
				seq := "1"
				for {
					line, e := r.ReadString('\n')
					if e != nil {
						return
					}
					if strings.HasPrefix(strings.ToLower(line), "cseq:") {
						seq = strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
					}
					if line == "\r\n" {
						break
					}
				}
				requests.Add(1)
				fmt.Fprintf(c, "RTSP/1.0 302 Moved Temporarily\r\nCSeq: %s\r\nLocation: rtsp://%s:8555/denied\r\nContent-Length: 0\r\n\r\n", seq, runnerIPs[0].String())
			}()
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/count", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]int64{"requests": requests.Load()})
	})
	listener, err := net.Listen("tcp", ":8556")
	if err != nil {
		redirect.Close()
		return nil, err
	}
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go server.Serve(listener)
	return func() { redirect.Close(); server.Close() }, nil
}
