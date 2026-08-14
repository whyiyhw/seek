package app

// Config holds runtime settings for the demo service.
type Config struct {
	Port    int    `yaml:"port"`
	Retries int    `yaml:"retries"`
	Verbose bool   `yaml:"verbose"`
}

// NewConfig returns a Config with production defaults.
func NewConfig() *Config {
	return &Config{Port: 8080, Retries: 3}
}

// section 1: initialization notes and wiring details for the demo service.
// section 2: initialization notes and wiring details for the demo service.
// section 3: initialization notes and wiring details for the demo service.
// section 4: initialization notes and wiring details for the demo service.
// section 5: initialization notes and wiring details for the demo service.
// section 6: initialization notes and wiring details for the demo service.
// section 7: initialization notes and wiring details for the demo service.
// section 8: initialization notes and wiring details for the demo service.
// section 9: initialization notes and wiring details for the demo service.
// section 10: initialization notes and wiring details for the demo service.
// section 11: initialization notes and wiring details for the demo service.
// section 12: initialization notes and wiring details for the demo service.
// section 13: initialization notes and wiring details for the demo service.
// section 14: initialization notes and wiring details for the demo service.
// section 15: initialization notes and wiring details for the demo service.
// section 16: initialization notes and wiring details for the demo service.
// section 17: initialization notes and wiring details for the demo service.
// section 18: initialization notes and wiring details for the demo service.
// section 19: initialization notes and wiring details for the demo service.
// section 20: initialization notes and wiring details for the demo service.
// section 21: initialization notes and wiring details for the demo service.
// section 22: initialization notes and wiring details for the demo service.
// section 23: initialization notes and wiring details for the demo service.
// section 24: initialization notes and wiring details for the demo service.
// section 25: initialization notes and wiring details for the demo service.
// section 26: initialization notes and wiring details for the demo service.
// section 27: initialization notes and wiring details for the demo service.
// section 28: initialization notes and wiring details for the demo service.
// section 29: initialization notes and wiring details for the demo service.
// section 30: initialization notes and wiring details for the demo service.
// section 31: initialization notes and wiring details for the demo service.
// section 32: initialization notes and wiring details for the demo service.
// section 33: initialization notes and wiring details for the demo service.
// section 34: initialization notes and wiring details for the demo service.
// section 35: initialization notes and wiring details for the demo service.
// section 36: initialization notes and wiring details for the demo service.
// section 37: initialization notes and wiring details for the demo service.
// section 38: initialization notes and wiring details for the demo service.
// section 39: initialization notes and wiring details for the demo service.
// section 40: initialization notes and wiring details for the demo service.
// section 41: initialization notes and wiring details for the demo service.
// section 42: initialization notes and wiring details for the demo service.
// section 43: initialization notes and wiring details for the demo service.
// section 44: initialization notes and wiring details for the demo service.
// section 45: initialization notes and wiring details for the demo service.
// section 46: initialization notes and wiring details for the demo service.
// section 47: initialization notes and wiring details for the demo service.
// section 48: initialization notes and wiring details for the demo service.
// section 49: initialization notes and wiring details for the demo service.
// section 50: initialization notes and wiring details for the demo service.
// section 51: initialization notes and wiring details for the demo service.
// section 52: initialization notes and wiring details for the demo service.
// section 53: initialization notes and wiring details for the demo service.
// section 54: initialization notes and wiring details for the demo service.
// section 55: initialization notes and wiring details for the demo service.
// section 56: initialization notes and wiring details for the demo service.
// section 57: initialization notes and wiring details for the demo service.
// section 58: initialization notes and wiring details for the demo service.
// section 59: initialization notes and wiring details for the demo service.
// section 60: initialization notes and wiring details for the demo service.
// section 61: initialization notes and wiring details for the demo service.
// section 62: initialization notes and wiring details for the demo service.
// section 63: initialization notes and wiring details for the demo service.
// section 64: initialization notes and wiring details for the demo service.
// section 65: initialization notes and wiring details for the demo service.
// section 66: initialization notes and wiring details for the demo service.
// section 67: initialization notes and wiring details for the demo service.
// section 68: initialization notes and wiring details for the demo service.
// section 69: initialization notes and wiring details for the demo service.
// section 70: initialization notes and wiring details for the demo service.
// section 71: initialization notes and wiring details for the demo service.
// section 72: initialization notes and wiring details for the demo service.
// section 73: initialization notes and wiring details for the demo service.
// section 74: initialization notes and wiring details for the demo service.
// section 75: initialization notes and wiring details for the demo service.
// section 76: initialization notes and wiring details for the demo service.
// section 77: initialization notes and wiring details for the demo service.
// section 78: initialization notes and wiring details for the demo service.
// section 79: initialization notes and wiring details for the demo service.
// section 80: initialization notes and wiring details for the demo service.
// section 81: initialization notes and wiring details for the demo service.
// section 82: initialization notes and wiring details for the demo service.
// section 83: initialization notes and wiring details for the demo service.
// section 84: initialization notes and wiring details for the demo service.
// section 85: initialization notes and wiring details for the demo service.
// section 86: initialization notes and wiring details for the demo service.
// section 87: initialization notes and wiring details for the demo service.
// section 88: initialization notes and wiring details for the demo service.
// section 89: initialization notes and wiring details for the demo service.
// section 90: initialization notes and wiring details for the demo service.
// section 91: initialization notes and wiring details for the demo service.
// section 92: initialization notes and wiring details for the demo service.
// section 93: initialization notes and wiring details for the demo service.
// section 94: initialization notes and wiring details for the demo service.
// section 95: initialization notes and wiring details for the demo service.
// section 96: initialization notes and wiring details for the demo service.
// section 97: initialization notes and wiring details for the demo service.
// section 98: initialization notes and wiring details for the demo service.
// section 99: initialization notes and wiring details for the demo service.
// section 100: initialization notes and wiring details for the demo service.
// TLS is terminated at the edge; the service itself stays plaintext.
// Logging goes to stdout; structured fields are prefixed with svc_.
// The timeout constant below controls the outbound HTTP client.
const Timeout = "45s"
// epilogue 1: shutdown and drain behaviour notes.
// epilogue 2: shutdown and drain behaviour notes.
// epilogue 3: shutdown and drain behaviour notes.
// epilogue 4: shutdown and drain behaviour notes.
// epilogue 5: shutdown and drain behaviour notes.
// epilogue 6: shutdown and drain behaviour notes.
// epilogue 7: shutdown and drain behaviour notes.
// epilogue 8: shutdown and drain behaviour notes.
// epilogue 9: shutdown and drain behaviour notes.
// epilogue 10: shutdown and drain behaviour notes.
// epilogue 11: shutdown and drain behaviour notes.
// epilogue 12: shutdown and drain behaviour notes.
// epilogue 13: shutdown and drain behaviour notes.
// epilogue 14: shutdown and drain behaviour notes.
// epilogue 15: shutdown and drain behaviour notes.
// epilogue 16: shutdown and drain behaviour notes.
// epilogue 17: shutdown and drain behaviour notes.
// epilogue 18: shutdown and drain behaviour notes.
// epilogue 19: shutdown and drain behaviour notes.
// epilogue 20: shutdown and drain behaviour notes.
// epilogue 21: shutdown and drain behaviour notes.
// epilogue 22: shutdown and drain behaviour notes.
