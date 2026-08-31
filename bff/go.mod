module github.com/mktkhr/nlops/bff

go 1.26.2

require (
	github.com/mktkhr/nlops/pkg v0.0.0
	github.com/mktkhr/nlops/orchestrator v0.0.0
)

replace github.com/mktkhr/nlops/pkg => ../pkg
replace github.com/mktkhr/nlops/orchestrator => ../orchestrator
