package server

import "mime"

func jsonMedia(header string) bool {
	mt, _, err := mime.ParseMediaType(header)
	return err == nil && mt == "application/json"
}
