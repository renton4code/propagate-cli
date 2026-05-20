module propagate/backend

go 1.22

require (
	github.com/lib/pq v1.10.2
	golang.org/x/crypto v0.31.0
	propagate/shared v0.0.0
)

replace propagate/shared => ../shared
