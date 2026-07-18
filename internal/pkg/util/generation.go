package util

import (
	"encoding/base64"
)

// GenerateBasicAuth returns an HTTP Basic auth header value ("Basic <base64>"),
// or "" if either credential is empty.
func GenerateBasicAuth(user, password string) string {
	if user == "" || password == "" {
		return ""
	}
	credentials := user + ":" + password
	encodedCredentials := base64.StdEncoding.EncodeToString([]byte(credentials))
	return "Basic " + encodedCredentials
}
