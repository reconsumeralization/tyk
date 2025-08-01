package streams

import (
	"context"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/sirupsen/logrus"
	"gopkg.in/yaml.v2"

	_ "github.com/TykTechnologies/tyk/internal/portal"
	_ "github.com/warpstreamlabs/bento/public/components/all"
	"github.com/warpstreamlabs/bento/public/service"
)

// Stream is a wrapper around stream
type Stream struct {
	allowedUnsafe []string
	streamConfig  string
	stream        *service.Stream
	logger        *logrus.Entry
}

// NewStream creates a new stream without initializing it
func NewStream(allowUnsafe []string, logger *logrus.Entry) *Stream {
	if len(allowUnsafe) > 0 {
		logger.Warnf("Allowing unsafe components: %v", allowUnsafe)
	}

	return &Stream{
		logger:        logger,
		allowedUnsafe: allowUnsafe,
	}
}

// Start loads up the configuration and starts the stream. Non-blocking
func (s *Stream) Start(config map[string]interface{}, mux service.HTTPMultiplexer) error {
	s.logger.Debugf("Starting stream")

	configPayload, err := yaml.Marshal(config)
	if err != nil {
		s.logger.Errorf("Failed to marshal config: %v", err)
		return err
	}

	configPayload = s.removeUnsafe(configPayload)

	s.logger.Debugf("Building new stream")
	builder := service.NewStreamBuilder()
	handler := slog.NewJSONHandler(newBentoLogAdapter(s.logger), nil)
	builder.SetLogger(slog.New(handler))

	err = builder.SetYAML(string(configPayload))
	if err != nil {
		s.logger.Errorf("Failed to set YAML: %v", err)
		return err
	}

	if mux != nil {
		builder.SetHTTPMux(mux)
	}

	stream, err := builder.Build()
	if err != nil {
		s.logger.Errorf("Failed to build stream: %v", err)
		return err
	}

	s.streamConfig = string(configPayload)
	s.stream = stream

	s.logger.Debugf("Stream built successfully, starting it")

	errChan := make(chan error, 1)
	go func() {
		s.logger.Infof("Starting stream")
		errChan <- stream.Run(context.Background())
	}()

	select {
	case err := <-errChan:
		if err != nil {
			s.logger.Errorf("Stream encountered an error: %v", err)
			return err
		}
	case <-time.After(100 * time.Millisecond):
		// If no error after a short delay, assume stream started successfully
	}

	s.logger.Debugf("Stream started successfully")
	return nil
}

// Stop cleans up the stream
func (s *Stream) Stop() error {
	s.logger.Printf("Stopping stream")

	if s.stream == nil {
		s.logger.Printf("No active stream to stop")
		return nil
	}

	stopCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	errChan := make(chan error, 1)
	go func() {
		errChan <- s.stream.Stop(stopCtx)
	}()

	select {
	case err := <-errChan:
		if err != nil {
			s.logger.Printf("Error stopping stream: %v", err)
		} else {
			s.logger.Printf("Stream stopped successfully")
		}
	case <-stopCtx.Done():
		s.logger.Printf("Timeout while stopping stream")
	}

	s.streamConfig = ""
	s.stream = nil

	return nil
}

// GetConfig returns the configuration of the stream
func (s *Stream) GetConfig() string {
	return s.streamConfig
}

// Reset stops the stream
func (s *Stream) Reset() error {
	return s.Stop()
}

var unsafeComponents = []string{
	// Inputs
	"csv", "dynamic", "file", "inproc", "socket", "socket_server", "stdin", "subprocess",

	// Processors
	"command", "subprocess", "wasm",

	// Outputs
	"file", "inproc", "socket",

	// Caches
	"file",
}

func (s *Stream) removeUnsafe(yamlBytes []byte) []byte {
	filteredUnsafeComponents := []string{}

	for _, component := range unsafeComponents {
		allowed := false
		for _, allowedComponent := range s.allowedUnsafe {
			if component == allowedComponent {
				allowed = true
				break
			}
		}
		if !allowed {
			filteredUnsafeComponents = append(filteredUnsafeComponents, component)
		}
	}

	yamlString := string(yamlBytes)
	for _, key := range filteredUnsafeComponents {
		if strings.Contains(yamlString, key+":") {
			// Use regexp to match the whole block of the given key
			re := regexp.MustCompile(fmt.Sprintf(`(?m)^\s*%s:\s*(.*\r?\n?)(\s+.*(?:\r?\n?|\z))*`, regexp.QuoteMeta(key)))
			// Remove matched parts
			yamlString = re.ReplaceAllString(yamlString, "")

			s.logger.Info("Removed unsafe component: ", key)
		}
	}
	return []byte(yamlString)
}
