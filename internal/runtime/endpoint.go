package runtime

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/mgertz/factory/internal/paths"
)

// The stable per-UID lock and discovery record prevent separate instances when
// two shells use different XDG environments. The record is only a hint: the
// lock, a live socket response, peer UID and instance ID must all agree.
type endpoint struct {
	Socket     string `json:"socket"`
	InstanceID string `json:"instance_id"`
}

func writeEndpoint(p paths.Paths, e endpoint) error {
	f, err := os.CreateTemp(p.CoordinationDir, ".endpoint-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	writeErr := json.NewEncoder(f).Encode(e)
	closeErr := f.Close()
	if err := errors.Join(writeErr, closeErr); err != nil {
		return err
	}
	return os.Rename(f.Name(), p.EndpointFile())
}

func readEndpoint(p paths.Paths) (endpoint, error) {
	var e endpoint
	f, err := paths.OpenFile(p.EndpointFile(), os.O_RDONLY)
	if err != nil {
		return e, err
	}
	defer f.Close()
	if err := json.NewDecoder(io.LimitReader(f, 4096)).Decode(&e); err != nil {
		return e, err
	}
	if !filepath.IsAbs(e.Socket) || e.InstanceID == "" {
		return e, errors.New("invalid Factory endpoint record")
	}
	return e, nil
}
