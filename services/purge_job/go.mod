module github.com/juliandevelops-studium/my-doctors-date/services/purge_job

go 1.27

require (
	github.com/juliandevelops-studium/my-doctors-date/libs/auth v0.0.0
	github.com/oapi-codegen/runtime v1.7.0
)

require github.com/google/uuid v1.6.0 // indirect

replace github.com/juliandevelops-studium/my-doctors-date/libs/auth => ../../libs/auth
