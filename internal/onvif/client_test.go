package onvif

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

const testPassword = "fixture-camera-password"

// deviceClockOffset is deliberately far from the host clock: a digest built from
// the local clock would be rejected by a real device in this state, so the test
// only passes when the client stamps Created from the device's own time.
const deviceClockOffset = 72 * time.Hour

type security struct {
	username string
	password string
	created  time.Time
	nonce    []byte
}

func parseSecurity(t *testing.T, body string) security {
	t.Helper()
	root, err := decode([]byte(body))
	if err != nil {
		t.Fatalf("request is not XML: %v", err)
	}
	token, ok := root.find("UsernameToken")
	if !ok {
		t.Fatal("authenticated request carries no UsernameToken")
	}
	nonce, err := base64.StdEncoding.DecodeString(token.value("Nonce"))
	if err != nil {
		t.Fatalf("nonce is not base64: %v", err)
	}
	created, err := time.Parse("2006-01-02T15:04:05Z", token.value("Created"))
	if err != nil {
		t.Fatalf("created is not a UTC stamp: %v", err)
	}
	return security{username: token.value("Username"), password: token.value("Password"), created: created, nonce: nonce}
}

func deviceTime() time.Time {
	return time.Now().UTC().Add(-deviceClockOffset).Truncate(time.Second)
}

func soapBody(inner string) string {
	return `<?xml version="1.0"?><s:Envelope xmlns:s="` + soapEnvelopeNS + `"><s:Body>` + inner + `</s:Body></s:Envelope>`
}

func dateTimeResponse(at time.Time) string {
	return soapBody(fmt.Sprintf(`<tds:GetSystemDateAndTimeResponse xmlns:tds="%s"><tds:SystemDateAndTime><tt:UTCDateTime xmlns:tt="http://www.onvif.org/ver10/schema"><tt:Time><tt:Hour>%d</tt:Hour><tt:Minute>%d</tt:Minute><tt:Second>%d</tt:Second></tt:Time><tt:Date><tt:Year>%d</tt:Year><tt:Month>%d</tt:Month><tt:Day>%d</tt:Day></tt:Date></tt:UTCDateTime></tds:SystemDateAndTime></tds:GetSystemDateAndTimeResponse>`,
		deviceNS, at.Hour(), at.Minute(), at.Second(), at.Year(), int(at.Month()), at.Day()))
}

func faultResponse(message string) string {
	return soapBody(`<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Code><s:Reason><s:Text xml:lang="en">` + message + `</s:Text></s:Reason></s:Fault>`)
}

// device is a synthetic ONVIF camera. It records every request so a test can
// prove which addresses were contacted and that each authenticated call carries
// a digest the device itself would accept.
type device struct {
	clock       time.Time
	mediaXAddr  string
	profiles    []string
	uri         map[string]string
	faults      map[string]string
	// rawFaults answers an action with a fault body verbatim, for vendor
	// structures the standard helper cannot express.
	rawFaults   map[string]string
	requireAuth bool
	requests    []string
	securities  []security
}

func (d *device) handler(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		text := string(body)
		d.requests = append(d.requests, r.URL.Path)
		if strings.Contains(text, "GetSystemDateAndTime") {
			fmt.Fprint(w, dateTimeResponse(d.clock))
			return
		}
		if d.requireAuth && !strings.Contains(text, "UsernameToken") {
			fmt.Fprint(w, faultResponse("Sender not Authorized"))
			return
		}
		if strings.Contains(text, "UsernameToken") {
			d.securities = append(d.securities, parseSecurity(t, text))
		}
		for action, message := range d.faults {
			if strings.Contains(text, action) {
				fmt.Fprint(w, faultResponse(message))
				return
			}
		}
		for action, fault := range d.rawFaults {
			if strings.Contains(text, action) {
				fmt.Fprint(w, soapBody(fault))
				return
			}
		}
		switch {
		case strings.Contains(text, "GetDeviceInformation"):
			fmt.Fprint(w, soapBody(`<tds:GetDeviceInformationResponse xmlns:tds="`+deviceNS+`"><tds:Manufacturer>Fixture Vision</tds:Manufacturer><tds:Model>FX-9000</tds:Model><tds:FirmwareVersion>4.2.1</tds:FirmwareVersion><tds:SerialNumber>SN-FIXTURE-1</tds:SerialNumber></tds:GetDeviceInformationResponse>`))
		case strings.Contains(text, "GetCapabilities"):
			fmt.Fprint(w, soapBody(`<tds:GetCapabilitiesResponse xmlns:tds="`+deviceNS+`"><tds:Capabilities><tt:Media xmlns:tt="http://www.onvif.org/ver10/schema"><tt:XAddr>`+d.mediaXAddr+`</tt:XAddr></tt:Media><tt:PTZ xmlns:tt="http://www.onvif.org/ver10/schema"><tt:XAddr>http://127.0.0.1/onvif/ptz_service</tt:XAddr></tt:PTZ></tds:Capabilities></tds:GetCapabilitiesResponse>`))
		case strings.Contains(text, "GetProfiles"):
			var builder strings.Builder
			builder.WriteString(`<trt:GetProfilesResponse xmlns:trt="` + mediaNS + `">`)
			for index, token := range d.profiles {
				width := 1920
				if index > 0 {
					width = 704
				}
				builder.WriteString(fmt.Sprintf(`<trt:Profiles token="%s"><tt:Name xmlns:tt="http://www.onvif.org/ver10/schema">profile-%d</tt:Name><tt:VideoEncoderConfiguration xmlns:tt="http://www.onvif.org/ver10/schema"><tt:Encoding>H264</tt:Encoding><tt:Resolution><tt:Width>%d</tt:Width><tt:Height>%d</tt:Height></tt:Resolution></tt:VideoEncoderConfiguration></trt:Profiles>`, token, index, width, width*9/16))
			}
			builder.WriteString(`</trt:GetProfilesResponse>`)
			fmt.Fprint(w, soapBody(builder.String()))
		case strings.Contains(text, "GetStreamUri"):
			token := ""
			if root, err := decode(body); err == nil {
				token = root.value("ProfileToken")
			}
			uri, ok := d.uri[token]
			if !ok {
				fmt.Fprint(w, faultResponse("no such profile"))
				return
			}
			fmt.Fprint(w, soapBody(`<trt:GetStreamUriResponse xmlns:trt="`+mediaNS+`"><trt:MediaUri><tt:Uri xmlns:tt="http://www.onvif.org/ver10/schema">`+uri+`</tt:Uri></trt:MediaUri></trt:GetStreamUriResponse>`))
		default:
			fmt.Fprint(w, faultResponse("unsupported action"))
		}
	}
}

func serve(t *testing.T, d *device) (string, int) {
	t.Helper()
	server := httptest.NewServer(d.handler(t))
	t.Cleanup(server.Close)
	host, port := splitURL(t, server.URL)
	return host, port
}

func splitURL(t *testing.T, raw string) (string, int) {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil {
		t.Fatal(err)
	}
	return parsed.Hostname(), port
}

func TestProbeReturnsStructuredStreamsNotURLs(t *testing.T) {
	d := &device{
		clock:      deviceTime(),
		mediaXAddr: "http://127.0.0.1/onvif/media_service",
		profiles:   []string{"Profile_1", "Profile_2"},
		uri: map[string]string{
			// A device may embed credentials and a foreign host; neither may
			// reach the stored configuration.
			"Profile_1": "rtsp://operator:secret@192.0.2.10:554/Streaming/Channels/101?transport=tcp",
			"Profile_2": "rtsp://192.0.2.10/Streaming/Channels/102",
		},
	}
	host, port := serve(t, d)
	result, err := Probe(context.Background(), Options{IP: host, Port: port, Username: "operator", Password: testPassword, Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if result.Device.Manufacturer != "Fixture Vision" || result.Device.Model != "FX-9000" || result.Device.FirmwareVersion != "4.2.1" {
		t.Fatalf("device identity lost: %+v", result.Device)
	}
	if !result.PTZ {
		t.Fatal("PTZ capability reported by the device was dropped")
	}
	if result.ONVIFPort != port {
		t.Fatalf("onvif port = %d, want %d", result.ONVIFPort, port)
	}
	if len(result.Streams) != 2 {
		t.Fatalf("streams = %+v", result.Streams)
	}
	main := result.Streams[0]
	// The stream host is outside the probed address, so it falls back to the
	// address that was actually probed, while the path and its query survive.
	if main.IP != host || main.RTSPPort != 554 || main.Path != "/Streaming/Channels/101?transport=tcp" {
		t.Fatalf("main stream = %+v", main)
	}
	if main.Width != 1920 || main.Height != 1080 || main.Encoding != "H264" || main.Token != "Profile_1" {
		t.Fatalf("main profile detail = %+v", main)
	}
	sub := result.Streams[1]
	if sub.Width != 704 || sub.Path != "/Streaming/Channels/102" || sub.RTSPPort != 554 {
		t.Fatalf("sub stream = %+v", sub)
	}
	media := false
	for _, path := range d.requests {
		if path != deviceServicePath && path != "/onvif/media_service" {
			t.Fatalf("probe contacted an unexpected path: %s", path)
		}
		media = media || path == "/onvif/media_service"
	}
	if !media {
		t.Fatal("a same-host media service address was not used")
	}
	for _, security := range d.securities {
		if security.username != "operator" {
			t.Fatalf("username = %q", security.username)
		}
		stamp := security.created.UTC().Format("2006-01-02T15:04:05Z")
		sum := sha1.Sum([]byte(string(security.nonce) + stamp + testPassword))
		if security.password != base64.StdEncoding.EncodeToString(sum[:]) {
			t.Fatal("password digest does not match the WS-Security definition")
		}
		if delta := security.created.Sub(d.clock); delta > 30*time.Second || delta < -30*time.Second {
			t.Fatalf("digest created=%s is %s away from the device clock; the device would reject it", security.created, delta)
		}
	}
}

func TestProbeIgnoresForeignServiceAddress(t *testing.T) {
	d := &device{
		clock:      deviceTime(),
		mediaXAddr: "http://192.0.2.10/onvif/media_service",
		profiles:   []string{"Profile_1"},
		uri:        map[string]string{"Profile_1": "rtsp://127.0.0.1/Streaming/Channels/101"},
	}
	host, port := serve(t, d)
	result, err := Probe(context.Background(), Options{IP: host, Port: port, Username: "u", Password: "p", Timeout: 2 * time.Second})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if len(result.Streams) != 1 || result.Streams[0].IP != host {
		t.Fatalf("streams = %+v", result.Streams)
	}
	for _, path := range d.requests {
		if path != deviceServicePath {
			t.Fatalf("a foreign media address was followed: %s", path)
		}
	}
}

func TestProbeMapsTransportAndProtocolFailures(t *testing.T) {
	closed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	closedHost, closedPort := splitURL(t, closed.URL)
	closed.Close()
	web := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, "<html><body>not a camera</body></html>")
	}))
	t.Cleanup(web.Close)
	webHost, webPort := splitURL(t, web.URL)
	rejecting := &device{clock: deviceTime(), mediaXAddr: "http://127.0.0.1/onvif/media_service", faults: map[string]string{"GetDeviceInformation": "Sender not Authorized"}}
	rejectHost, rejectPort := serve(t, rejecting)
	guarded := &device{clock: deviceTime(), mediaXAddr: "http://127.0.0.1/onvif/media_service", requireAuth: true, profiles: []string{"Profile_1"}, uri: map[string]string{"Profile_1": "rtsp://127.0.0.1/Streaming/Channels/101"}}
	guardedHost, guardedPort := serve(t, guarded)
	healthy := &device{clock: deviceTime(), mediaXAddr: "http://127.0.0.1/onvif/media_service", profiles: []string{"Profile_1"}, uri: map[string]string{"Profile_1": "rtsp://127.0.0.1/main"}}
	host, port := serve(t, healthy)
	for _, test := range []struct {
		name    string
		options Options
		want    error
	}{
		{"unreachable", Options{IP: closedHost, Port: closedPort, Username: "u", Password: "p"}, ErrUnreachable},
		{"not a device service", Options{IP: webHost, Port: webPort, Username: "u", Password: "p"}, ErrNotDetected},
		{"missing credentials", Options{IP: guardedHost, Port: guardedPort}, ErrAuthRejected},
		{"credentials rejected", Options{IP: rejectHost, Port: rejectPort, Username: "u", Password: "p"}, ErrAuthRejected},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Probe(context.Background(), test.options); !errors.Is(err, test.want) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
	// Supplying credentials to a device that demands them must succeed, and a
	// device that needs none must not be refused for having no credentials.
	if _, err := Probe(context.Background(), Options{IP: guardedHost, Port: guardedPort, Username: "u", Password: "p", Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("authenticated probe failed: %v", err)
	}
	if _, err := Probe(context.Background(), Options{IP: host, Port: port, Timeout: 2 * time.Second}); err != nil {
		t.Fatalf("a device that needs no credentials was refused: %v", err)
	}
}

// The same refusal is worded differently by different firmwares. A fault that
// means "authentication no" must answer as one instead of falling through to
// the generic "response invalid", whether the words live in the Reason text or
// only in the Code/Subcode values.
func TestProbeClassifiesDifferentlyWordedAuthFaults(t *testing.T) {
	for name, fault := range map[string]string{
		"security token":       `<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Code><s:Reason><s:Text xml:lang="en">The security token could not be authenticated or authorized</s:Text></s:Reason></s:Fault>`,
		"authorization failed": `<s:Fault><s:Code><s:Value>s:Sender</s:Value></s:Code><s:Reason><s:Text xml:lang="en">Authorization failed</s:Text></s:Reason></s:Fault>`,
		"subcode only":         `<s:Fault><s:Code><s:Value>s:Sender</s:Value><s:Subcode><s:Value>ter:NotAuthorized</s:Value></s:Subcode></s:Code></s:Fault>`,
	} {
		t.Run(name, func(t *testing.T) {
			d := &device{clock: deviceTime(), mediaXAddr: "http://127.0.0.1/onvif/media_service", rawFaults: map[string]string{"GetDeviceInformation": fault}}
			host, port := serve(t, d)
			if _, err := Probe(context.Background(), Options{IP: host, Port: port, Username: "u", Password: "p"}); !errors.Is(err, ErrAuthRejected) {
				t.Fatalf("error = %v, want ErrAuthRejected", err)
			}
		})
	}
}

func TestProbeRejectsAProfileWithoutUsableStream(t *testing.T) {
	for name, uri := range map[string]string{
		"not rtsp":  "http://127.0.0.1/stream",
		"no path":   "rtsp://127.0.0.1",
		"not a uri": "://broken",
		"empty":     "",
	} {
		t.Run(name, func(t *testing.T) {
			d := &device{clock: deviceTime(), mediaXAddr: "http://127.0.0.1/onvif/media_service", profiles: []string{"Profile_1"}, uri: map[string]string{"Profile_1": uri}}
			host, port := serve(t, d)
			if _, err := Probe(context.Background(), Options{IP: host, Port: port, Username: "u", Password: "p"}); !errors.Is(err, ErrNoProfile) {
				t.Fatalf("error = %v, want ErrNoProfile", err)
			}
		})
	}
}

func TestDeviceClockRejectsUnusableResponses(t *testing.T) {
	if _, ok := deviceClock(node{XMLName: xml.Name{Local: "Envelope"}, Children: []node{{XMLName: xml.Name{Local: "Body"}}}}); ok {
		t.Fatal("a response without UTCDateTime produced a clock")
	}
	root, err := decode([]byte(dateTimeResponse(deviceTime())))
	if err != nil {
		t.Fatal(err)
	}
	if clock, ok := deviceClock(root); !ok || clock.IsZero() {
		t.Fatal("a valid UTCDateTime was not read")
	}
	broken, err := decode([]byte(strings.Replace(dateTimeResponse(deviceTime()), "<tt:Year>", "<tt:Year>not-a-year-", 1)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := deviceClock(broken); ok {
		t.Fatal("an unparsable date produced a clock")
	}
}
