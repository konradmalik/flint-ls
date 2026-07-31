// Release tooling, kept out of the main module so that its dependency tree --
// which is larger than the server's -- stays out of go.sum and vendor/.
module github.com/konradmalik/flint-ls/tools

go 1.26.0

tool (
	github.com/Songmu/goxz/cmd/goxz
	github.com/tcnksm/ghr
	github.com/x-motemen/gobump/cmd/gobump
)

require (
	github.com/Masterminds/semver/v3 v3.5.0 // indirect
	github.com/Songmu/goxz v0.10.1 // indirect
	github.com/Songmu/retry v0.1.0 // indirect
	github.com/chzyer/readline v1.5.1 // indirect
	github.com/google/go-github v17.0.0+incompatible // indirect
	github.com/google/go-github/v66 v66.0.0 // indirect
	github.com/google/go-querystring v1.2.0 // indirect
	github.com/hashicorp/go-version v1.9.0 // indirect
	github.com/inconshreveable/mousetrap v1.1.0 // indirect
	github.com/manifoldco/promptui v0.9.0 // indirect
	github.com/mattn/go-colorable v0.1.15 // indirect
	github.com/mattn/go-isatty v0.0.24 // indirect
	github.com/mattn/go-tty v0.0.8 // indirect
	github.com/mitchellh/colorstring v0.0.0-20190213212951-d06e56a500db // indirect
	github.com/spf13/cobra v1.10.2 // indirect
	github.com/spf13/pflag v1.0.10 // indirect
	github.com/tcnksm/ghr v0.18.3 // indirect
	github.com/tcnksm/go-gitconfig v0.1.2 // indirect
	github.com/tcnksm/go-latest v0.0.0-20170313132115-e3007ae9052e // indirect
	github.com/thediveo/enumflag/v2 v2.2.1 // indirect
	github.com/x-motemen/gobump v0.3.0 // indirect
	golang.org/x/net v0.57.0 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/sync v0.22.0 // indirect
	golang.org/x/sys v0.47.0 // indirect
)
