module github.com/juliandevelops-studium/my-doctors-date/services/mail_service

go 1.27

require (
	github.com/getkin/kin-openapi v0.152.0
	github.com/go-chi/chi/v5 v5.5.0
	github.com/google/uuid v1.6.0
	github.com/juliandevelops-studium/my-doctors-date/libs/auth v0.0.0
	github.com/oapi-codegen/nethttp-middleware v1.3.1
	github.com/oapi-codegen/runtime v1.8.0
)

require (
	github.com/go-openapi/jsonpointer v1.0.0 // indirect
	github.com/gorilla/mux v1.8.1 // indirect
	github.com/oasdiff/yaml v0.1.1 // indirect
	github.com/oasdiff/yaml3 v0.0.14 // indirect
	github.com/rogpeppe/go-internal v1.9.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.3 // indirect
	golang.org/x/text v0.43.0 // indirect
)

replace github.com/juliandevelops-studium/my-doctors-date/libs/auth => ../../libs/auth
