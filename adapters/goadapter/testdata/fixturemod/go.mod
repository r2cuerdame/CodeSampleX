module example.com/fixture

go 1.26

require github.com/stretchr/testify v1.9.0

require (
	example.com/privatemod v0.0.0
	github.com/davecgh/go-spew v1.1.1 // indirect
	github.com/pmezard/go-difflib v1.0.0 // indirect
)

replace example.com/privatemod => ../privatemod
