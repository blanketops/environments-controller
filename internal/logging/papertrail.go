package logging

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/syslog"
	"net/http"
	"os"
	"time"

	"go.uber.org/zap/zapcore"
)

// buildPapertrailCore attempts to connect to Papertrail.
// Failure is NON-FATAL and returns nil.
func buildPapertrailCore(cfg Config, enc zapcore.Encoder) zapcore.Core {
	if !cfg.EnablePapertrail {
		return nil
	}

	if cfg.PapertrailAddr == "" {
		fmt.Fprintln(os.Stderr, "papertrail enabled but PapertrailAddr is empty")
		return nil
	}

	tag := cfg.PapertrailTag
	if tag == "" {
		tag = "blanketops-environment-operator"
	}

	writer, err := syslog.Dial(
		"tcp",
		cfg.PapertrailAddr,
		syslog.LOG_INFO|syslog.LOG_LOCAL0,
		tag,
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Papertrail unavailable: %v\n", err)
		return nil
	}

	return zapcore.NewCore(
		enc,
		zapcore.AddSync(writer),
		parseLevel(cfg.Level),
	)
}

// SetupPapertrailJSONIngest returns a function that sends JSON logs
// directly to Papertrail / SolarWinds HTTP ingestion API.
//
// This is intentionally decoupled from zap:
// - no global logger
// - no side effects
// - safe to use from controllers, jobs, or goroutines
func SetupPapertrailJSONIngest(token string) func(msg string) error {
	const endpoint = "https://logs.collector.solarwinds.com/v1/log"

	client := &http.Client{
		Timeout: 5 * time.Second,
	}

	return func(msg string) error {
		payload := map[string]interface{}{
			"message":   msg,
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return err
		}

		req, err := http.NewRequest("POST", endpoint, bytes.NewReader(body))
		if err != nil {
			return err
		}

		req.SetBasicAuth("", token)
		req.Header.Set("Content-Type", "application/json")

		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 300 {
			return fmt.Errorf("papertrail ingest error: %s", resp.Status)
		}

		return nil
	}
}
