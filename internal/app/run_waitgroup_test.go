package app

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

// TestBackgroundWaitGroupIsBalanced pins the one invariant in Run() that nothing
// else can check: every goroutine that reports to the background WaitGroup is
// covered by exactly one background.Add.
//
// It is worth a whole test because both directions fail quietly and late, and
// both already shipped once in the same block:
//
//   - an Add too many: Wait() never returns, so the process hangs on SIGTERM
//     until the orchestrator kills it — recording stops uncleanly;
//   - Done without a matching Add: panic("negative WaitGroup counter") on the
//     shutdown path, i.e. at the least observable moment.
//
// Neither is reachable from the unit tests or from a live run, because both live
// in the assembly path that only fires when the process is told to stop. Reading
// the source is the only cheap place to catch it, so the source is what this
// test reads.
func TestBackgroundWaitGroupIsBalanced(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "run.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	var run *ast.FuncDecl
	for _, decl := range file.Decls {
		if fn, ok := decl.(*ast.FuncDecl); ok && fn.Name.Name == "Run" {
			run = fn
		}
	}
	if run == nil {
		t.Fatal("func Run not found in run.go; this test needs updating, not deleting")
	}

	var added, spawned int
	var unknown []string

	// A loop multiplies everything inside it, so the walk carries the trip
	// count. Counts are read from literals: a loop this test cannot evaluate is
	// reported rather than guessed at, because guessing is how a balance check
	// starts passing for the wrong reason.
	multiplier := func(stack []int) int {
		total := 1
		for _, n := range stack {
			total *= n
		}
		return total
	}

	var walk func(node ast.Node, stack []int)
	walk = func(node ast.Node, stack []int) {
		ast.Inspect(node, func(n ast.Node) bool {
			switch s := n.(type) {
			case *ast.GoStmt:
				// Only goroutines that hand control back to the WaitGroup count;
				// the ones signalling a private channel are deliberately outside
				// it.
				reports := false
				ast.Inspect(s.Call, func(x ast.Node) bool {
					if sel, ok := x.(*ast.SelectorExpr); ok && sel.Sel.Name == "Done" {
						reports = true
					}
					return true
				})
				if reports {
					spawned += multiplier(stack)
				}
				walk(s.Call, stack)
				return false
			case *ast.ForStmt:
				if bin, ok := s.Cond.(*ast.BinaryExpr); ok && bin.Op == token.LSS {
					if lit, ok := bin.Y.(*ast.BasicLit); ok {
						if n, err := strconv.Atoi(lit.Value); err == nil {
							walk(s.Body, append(stack, n))
							return false
						}
					}
				}
				unknown = append(unknown, fset.Position(s.Pos()).String())
				walk(s.Body, append(stack, 1))
				return false
			case *ast.RangeStmt:
				if lit, ok := s.X.(*ast.CompositeLit); ok {
					walk(s.Body, append(stack, len(lit.Elts)))
					return false
				}
				unknown = append(unknown, fset.Position(s.Pos()).String())
				walk(s.Body, append(stack, 1))
				return false
			case *ast.CallExpr:
				sel, ok := s.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Add" || len(s.Args) != 1 {
					break
				}
				ident, ok := sel.X.(*ast.Ident)
				if !ok || ident.Name != "background" {
					break
				}
				if lit, ok := s.Args[0].(*ast.BasicLit); ok {
					if n, err := strconv.Atoi(lit.Value); err == nil {
						added += n * multiplier(stack)
					}
				}
			}
			return true
		})
	}
	walk(run.Body, nil)

	for _, at := range unknown {
		t.Logf("loop counted as one iteration (bound is not a literal): %s", at)
	}
	if added != spawned {
		t.Fatalf("background WaitGroup is unbalanced: Add totals %d but %d goroutines report Done. "+
			"Too many blocks shutdown forever; too few panics with a negative counter. Count the block by hand.",
			added, spawned)
	}
}
