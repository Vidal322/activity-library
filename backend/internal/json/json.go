package json

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strconv"
)

func write(w http.ResponseWriter, status int, data any) error {
	var buf bytes.Buffer

	err := json.NewEncoder(&buf).Encode(data)
	if err != nil {
		return err
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(buf.Len()))
	w.WriteHeader(status)

	_, err := w.Write(buf.Bytes())
	return err
}

func read(r *http.Response) {
}
