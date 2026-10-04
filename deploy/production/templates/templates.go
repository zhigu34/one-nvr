package templates

import _ "embed"

//go:embed nginx-http.conf
var HTTP string

//go:embed nginx-https.conf
var HTTPS string

//go:embed nginx-pending.conf
var Pending string
