// Package webui bundles the command center in the BCC binary.
package webui

import (
	"embed"
	"encoding/json"
	"strings"
)

// FS contains only public UI assets. No credentials or state are embedded.
//
//go:embed bcc-command-center-v3-preview.html bcc-product-v3.css bcc-product-v3.js bcc-operations-v3.js bcc-earth-v3.js bcc-globe-v3.js bcc-topology-snapshot-v3.js bcc-live-adapter-v3.js bcc-welcome-globe-v3.js assets/*
var FS embed.FS

type Config struct {
	OperationsHTML string `json:"operationsHTML,omitempty"`
	OperationsJS   string `json:"operationsJS,omitempty"`
	CSRF           string `json:"csrf,omitempty"`
	CSRFHeader     string `json:"csrfHeader,omitempty"`
	Mode           string `json:"mode"`
	Base           string `json:"base"`
	LoginAction    string `json:"loginAction"`
	Error          string `json:"error,omitempty"`
}

func Page(config Config) string {
	raw, _ := FS.ReadFile("bcc-command-center-v3-preview.html")
	data, _ := json.Marshal(config) // Escapes HTML-sensitive characters.
	return strings.Replace(string(raw), "<head>", "<head><script>window.BAFT_UI="+string(data)+";</script>", 1)
}
