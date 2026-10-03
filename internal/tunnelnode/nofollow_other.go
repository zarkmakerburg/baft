//go:build !unix

package tunnelnode

import (
	"errors"
	"os"
)

// Discovery fails closed where a no-follow open is not available.
func openNoFollow(path string) (*os.File, error) {
	return nil, errors.New("discovery is not supported on this platform: no-follow open is unavailable")
}
