package logging

type Config struct {
	Development bool

	Console  bool
	File     bool
	FilePath string

	Level string // "debug", "info", "warn", "error"

	EnablePapertrail bool
	PapertrailAddr   string
	PapertrailTag    string
}

func DefaultConfig() Config {
	return Config{
		Development: true,
		Console:     true,
		File:        false,
		FilePath:    "/tmp/blanketops-environment-controller.log",
		Level:       "info",
	}
}
