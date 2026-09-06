package s3

// Config holds the configuration for connecting to an S3-compatible storage.
type Config struct {
	Endpoint       string
	Region         string
	AccessKey      string
	SecretKey      string
	Token          string
	UseSsl         bool
	ForcePathStyle bool
	Bucket         string
}
