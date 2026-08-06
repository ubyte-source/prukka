package wasapi

import (
	"fmt"
	"io"

	"github.com/ubyte-source/prukka/internal/media/deviceurl"
	"github.com/ubyte-source/prukka/internal/redact"
)

// DefaultEndpointID selects the system's current default render endpoint
// instead of naming one.
const DefaultEndpointID = "default"

// endpointID maps a push target onto the id mmdeviceapi resolves. It parses
// rather than cutting the URL: discovery publishes labeled targets, and
// "3?label=Speakers" is not an endpoint id.
func endpointID(target string) (string, error) {
	ref, err := deviceurl.Parse(target)
	if err != nil {
		return "", err
	}
	if ref.Kind != deviceurl.Audio {
		return "", fmt.Errorf("wasapi: target %s is not device://audio/<id>", redact.URL(target))
	}

	return ref.ID, nil
}

// Open connects a device://audio/<id> target to its render endpoint.
func Open(target string, options ...OpenOption) (io.WriteCloser, error) {
	id, err := endpointID(target)
	if err != nil {
		return nil, err
	}

	config := defaultOpenConfig()
	for _, option := range options {
		option(&config)
	}

	return open(id, config)
}
