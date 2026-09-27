package safego

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// requestPathPackages are the packages whose code runs inside an HTTP request.
// A panic in a bare goroutine there is not caught by the router's Recoverer
// (it only guards the request goroutine) and takes the whole instance down —
// triggered by a single user action. Every goroutine in them must go through
// safego.Go.
//
// Long-lived infrastructure loops (hub.Run, the bus consumer, ListenAndServe)
// are deliberately NOT listed: recovering them would leave a zombie node that
// accepts connections but delivers nothing, and crashing loudly is preferred.
var requestPathPackages = []string{
	"../transport/http/handler",
	"../transport/http/admin",
	"../transport/http/middleware",
	"../service",
}

func TestNoBareGoroutinesOnRequestPath(t *testing.T) {
	fset := token.NewFileSet()
	for _, dir := range requestPathPackages {
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("reading %s: %v", dir, err)
		}
		for _, e := range entries {
			name := e.Name()
			if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
				continue
			}
			path := filepath.Join(dir, name)
			file, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatalf("parsing %s: %v", path, err)
			}
			ast.Inspect(file, func(n ast.Node) bool {
				if g, ok := n.(*ast.GoStmt); ok {
					t.Errorf("%s: bare goroutine on the request path; use safego.Go", fset.Position(g.Pos()))
				}
				return true
			})
		}
	}
}
