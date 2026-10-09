// Package onvif speaks the ONVIF device and media services far enough to fill a
// channel's RTSP fields without typing stream paths by hand (PRD CH-06).
//
// Scope: one camera at a time, addressed by the operator. There is no LAN scan,
// no PTZ control and no vendor management here; those stay out until they are
// implemented for real. The package performs no authorization and no address
// policy check — the channel service owns both — so it never dials anything it
// is not handed.
package onvif

import (
	"errors"
	"time"
)

var (
	// ErrUnreachable means nothing answered at the device service address.
	ErrUnreachable = errors.New("onvif_unreachable")
	// ErrNotDetected means something answered but not with an ONVIF device
	// service response.
	ErrNotDetected = errors.New("onvif_not_detected")
	// ErrAuthRejected means the device refused the supplied credentials. The
	// operator can fix this by correcting them.
	ErrAuthRejected = errors.New("onvif_auth_rejected")
	// ErrResponseInvalid means the device answered with a response the client
	// cannot use.
	ErrResponseInvalid = errors.New("onvif_response_invalid")
	// ErrNoProfile means the device exposed no usable RTSP stream.
	ErrNoProfile = errors.New("onvif_no_profile")
)

// Device is the identity a device reports about itself. SerialNumber is
// evidence for CH-09 identity, never an automatic identity claim.
type Device struct {
	Manufacturer    string `json:"manufacturer"`
	Model           string `json:"model"`
	FirmwareVersion string `json:"firmware_version"`
	SerialNumber    string `json:"serial_number"`
}

// Stream is one media profile resolved to a stream this product can store:
// structured fields, never a full URL with embedded credentials (CH-10).
type Stream struct {
	Token    string `json:"token"`
	Name     string `json:"name"`
	Encoding string `json:"encoding"`
	Width    int    `json:"width"`
	Height   int    `json:"height"`
	IP       string `json:"ip"`
	RTSPPort int    `json:"rtsp_port"`
	Path     string `json:"path"`
}

// Result is what the operator needs to fill the connection form.
type Result struct {
	Device    Device   `json:"device"`
	Streams   []Stream `json:"streams"`
	PTZ       bool     `json:"ptz"`
	ONVIFPort int      `json:"onvif_port"`
}

// Options describes one camera to probe. Username and Password are transient:
// they are used for the probe and never returned, logged or audited.
type Options struct {
	IP       string
	Port     int
	Username string
	Password string
	Timeout  time.Duration
}
