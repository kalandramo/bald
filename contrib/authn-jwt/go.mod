module github.com/kalandramo/bald/contrib/authn-jwt

go 1.27.1

require (
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/kalandramo/bald v0.13.0
	github.com/stretchr/testify v1.12.1
)

require go.yaml.in/yaml/v3 v3.0.5 // indirect

replace github.com/kalandramo/bald => ../..

replace github.com/kalandramo/bald/registry => ../../registry
