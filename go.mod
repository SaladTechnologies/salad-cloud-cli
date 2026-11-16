module github.com/saladtechnologies/salad-cloud-cli

go 1.26.0

toolchain go1.27.4

// Prettier and cSpell live here; nothing under it is part of this module.
ignore ./node_modules

require (
	github.com/saladtechnologies/salad-cloud-sdk-go v0.9.0-alpha.17
	github.com/spf13/cobra v1.10.2
	github.com/spf13/pflag v1.0.9
	golang.org/x/mod v0.43.0
	golang.org/x/term v0.48.0
	sigs.k8s.io/yaml v1.6.0
)

require (
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/sys v0.50.0 // indirect
)
