package onvif

import (
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/xml"
	"strings"
	"time"
)

// The request bodies are fixed by the ONVIF specifications, so they are literals
// rather than generated code. Responses are parsed by local name instead of by
// namespace path: vendors freely choose prefixes, and some omit namespaces
// entirely, while the local element names are the part the spec pins down.
const (
	soapEnvelopeNS = "http://www.w3.org/2003/05/soap-envelope"
	deviceNS       = "http://www.onvif.org/ver10/device/wsdl"
	mediaNS        = "http://www.onvif.org/ver10/media/wsdl"
	securityNS     = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-secext-1.0.xsd"
	utilityNS      = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-wssecurity-utility-1.0.xsd"
	digestType     = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest"
	nonceType      = "http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-soap-message-security-1.0#Base64Binary"
)

const (
	bodyGetSystemDateAndTime = `<tds:GetSystemDateAndTime xmlns:tds="` + deviceNS + `"/>`
	bodyGetDeviceInformation = `<tds:GetDeviceInformation xmlns:tds="` + deviceNS + `"/>`
	bodyGetCapabilities      = `<tds:GetCapabilities xmlns:tds="` + deviceNS + `"><tds:Category>All</tds:Category></tds:GetCapabilities>`
	bodyGetProfiles          = `<trt:GetProfiles xmlns:trt="` + mediaNS + `"/>`
)

// digest is the WS-Security UsernameToken PasswordDigest: Base64(SHA1(nonce ||
// created || password)) over the raw nonce bytes and the exact created string
// that travels in the header.
func digest(nonce []byte, created, password string) string {
	h := sha1.New()
	h.Write(nonce)
	h.Write([]byte(created))
	h.Write([]byte(password))
	return base64.StdEncoding.EncodeToString(h.Sum(nil))
}

func securityHeader(username, password string, created time.Time) (string, error) {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	stamp := created.UTC().Format("2006-01-02T15:04:05Z")
	var b strings.Builder
	b.WriteString(`<s:Header><wsse:Security s:mustUnderstand="1" xmlns:wsse="` + securityNS + `" xmlns:wsu="` + utilityNS + `">`)
	b.WriteString(`<wsse:UsernameToken><wsse:Username>` + escape(username) + `</wsse:Username>`)
	b.WriteString(`<wsse:Password Type="` + digestType + `">` + digest(nonce, stamp, password) + `</wsse:Password>`)
	b.WriteString(`<wsse:Nonce EncodingType="` + nonceType + `">` + base64.StdEncoding.EncodeToString(nonce) + `</wsse:Nonce>`)
	b.WriteString(`<wsu:Created>` + stamp + `</wsu:Created>`)
	b.WriteString(`</wsse:UsernameToken></wsse:Security></s:Header>`)
	return b.String(), nil
}

// envelope wraps a request body, adding the security header only when the
// operation needs it. GetSystemDateAndTime must stay anonymous: it is how the
// client learns the device clock before it can produce a digest the device
// still considers fresh.
func envelope(body string, username, password string, created time.Time) (string, error) {
	header := ""
	if username != "" || password != "" {
		var err error
		header, err = securityHeader(username, password, created)
		if err != nil {
			return "", err
		}
	}
	return `<?xml version="1.0" encoding="UTF-8"?><s:Envelope xmlns:s="` + soapEnvelopeNS + `">` + header + `<s:Body>` + body + `</s:Body></s:Envelope>`, nil
}

func streamRequest(token string) string {
	return `<trt:GetStreamUri xmlns:trt="` + mediaNS + `"><trt:StreamSetup><tt:Stream xmlns:tt="http://www.onvif.org/ver10/schema">RTP-Unicast</tt:Stream><tt:Transport xmlns:tt="http://www.onvif.org/ver10/schema"><tt:Protocol>RTSP</tt:Protocol></tt:Transport></trt:StreamSetup><trt:ProfileToken>` + escape(token) + `</trt:ProfileToken></trt:GetStreamUri>`
}

func escape(value string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(value))
	return b.String()
}

// node is a decoded XML element. Parsing into a name-driven tree rather than
// typed structs keeps the client working across the wrapper elements and
// namespace prefixes different vendors produce.
type node struct {
	XMLName  xml.Name
	Attrs    []xml.Attr `xml:",any,attr"`
	Text     string     `xml:",chardata"`
	Children []node     `xml:",any"`
}

func (n node) attr(name string) string {
	for _, attribute := range n.Attrs {
		if attribute.Name.Local == name {
			return strings.TrimSpace(attribute.Value)
		}
	}
	return ""
}

func decode(raw []byte) (node, error) {
	var root node
	if err := xml.Unmarshal(raw, &root); err != nil {
		return node{}, err
	}
	return root, nil
}

// find returns the first descendant with this local name, which skips the
// envelope, body and response wrappers in one step.
func (n node) find(name string) (node, bool) {
	if n.XMLName.Local == name {
		return n, true
	}
	for _, child := range n.Children {
		if found, ok := child.find(name); ok {
			return found, true
		}
	}
	return node{}, false
}

// all collects every descendant with this local name, which is how repeated
// elements such as media profiles are read without depending on how many
// wrapper elements a vendor inserts around them.
func (n node) all(name string) []node {
	var out []node
	if n.XMLName.Local == name {
		out = append(out, n)
	}
	for _, child := range n.Children {
		out = append(out, child.all(name)...)
	}
	return out
}

func (n node) child(name string) (node, bool) {
	for _, child := range n.Children {
		if child.XMLName.Local == name {
			return child, true
		}
	}
	return node{}, false
}

// value returns the trimmed text of the first matching descendant.
func (n node) value(name string) string {
	if found, ok := n.find(name); ok {
		return strings.TrimSpace(found.Text)
	}
	return ""
}

// faultMessage reports the SOAP fault text when the device rejected the call.
func faultMessage(root node) (string, bool) {
	fault, ok := root.find("Fault")
	if !ok {
		return "", false
	}
	text := strings.TrimSpace(fault.value("faultstring"))
	if text == "" {
		text = strings.TrimSpace(fault.value("Text"))
	}
	if text == "" {
		text = strings.TrimSpace(fault.value("Reason"))
	}
	return text, true
}

func authorized(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "notauthorized") || strings.Contains(lower, "not authorized") ||
		strings.Contains(lower, "unauthorized") || strings.Contains(lower, "authentication") ||
		strings.Contains(lower, "sender not")
}
