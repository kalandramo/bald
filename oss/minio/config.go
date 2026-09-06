package minio

// Config holds the configuration for connecting to a MinIO server.
type Config struct {
	Endpoint  string
	AccessKey string
	SecretKey string
	Token     string
	UseSsl    bool
}
