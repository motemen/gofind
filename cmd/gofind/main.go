// gofind is a comamnd that searches through Go source code by types.
//
// Usage
//
//    gofind <pkg>.<name>[.<sel>] <pkg>...
//
// Example
//
//    % gofind encoding/json.Encoder.Encode $(go list golang.org/x/...)
//    handlers.go:145:        json.NewEncoder(w).Encode(resp)
//    socket.go:125:                  if err := enc.Encode(m); err != nil {
//
// Description
//
// gofind searches through Go source code by given expression, using type information.
// It finds code entities with the type of given expression:
//
// * Variable definitions/occurrences
// * Struct fields (with <sel>)
// * Methods (with <sel>)
package main

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"sort"
	"strings"
	"sync"

	"go/ast"
	"go/token"
	"go/types"

	"golang.org/x/tools/go/loader"
)

type result struct {
	fset  *token.FileSet
	nodes []ast.Node
}

func (r result) Len() int {
	return len(r.nodes)
}

func (r result) Less(i, j int) bool {
	p := r.fset.Position(r.nodes[i].Pos())
	q := r.fset.Position(r.nodes[j].Pos())

	if p.Filename == q.Filename {
		return p.Offset < q.Offset
	} else {
		return p.Filename < q.Filename
	}
}

func (r result) Swap(i, j int) {
	r.nodes[i], r.nodes[j] = r.nodes[j], r.nodes[i]
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s <pkg>.<name>[.<sel>] <args>...\n", os.Args[0])
	fmt.Fprintln(os.Stderr, `
Example:

   % gofind encoding/json.Encoder.Encode $(go list golang.org/x/...)
   handlers.go:145:        json.NewEncoder(w).Encode(resp)
   socket.go:125:                  if err := enc.Encode(m); err != nil {`)
	fmt.Fprintln(os.Stderr, loader.FromArgsUsage)
}

func main() {
	if len(os.Args) < 3 {
		usage()
		os.Exit(2)
	}

	target := os.Args[1]

	paths := strings.Split(target, "/")              // {"golang.org","x","tools","go","loader.Config"}
	names := strings.Split(paths[len(paths)-1], ".") // {"loader","Config"}

	// TODO(motemen): provide filename-only option like "grep -l"

	pkgPath := strings.Join(append(paths[0:len(paths)-1], names[0]), "/")
	objName := names[1]
	selName := ""
	if len(names) > 2 {
		selName = names[2]
	}

	// TODO validate query

	var conf loader.Config
	_, err := conf.FromArgs(os.Args[2:], false)
	if err != nil {
		log.Fatal(err)
	}

	prog, err := conf.Load()
	if err != nil {
		log.Fatal(err)
	}

	matches := func(typ types.Type, sel string) bool {
		if sel != selName {
			return false
		}

		for {
			if p, ok := typ.(*types.Pointer); ok {
				typ = p.Elem()
			} else {
				break
			}
		}

		tn, ok := typ.(*types.Named)
		if !ok {
			return false
		}

		if tn.Obj().Pkg() == nil {
			// TODO(motemen): eg. "error" in universe scope
			return false
		}

		return tn.Obj().Pkg().Path() == pkgPath && tn.Obj().Name() == objName
	}

	c := make(chan ast.Node)
	res := result{
		fset:  conf.Fset,
		nodes: []ast.Node{},
	}

	done := make(chan struct{})
	go func() {
		for node := range c {
			res.nodes = append(res.nodes, node)
		}
		done <- struct{}{}
	}()

	var wg sync.WaitGroup

	// TODO(motemen): print for each package?
	for _, pi := range prog.InitialPackages() {
		wg.Add(3)

		go func(pi *loader.PackageInfo) {
			defer wg.Done()

			for expr, sel := range pi.Selections {
				if v, ok := sel.Obj().(*types.Var); ok {
					if matches(sel.Recv(), v.Name()) {
						c <- expr.Sel
					}
				} else if f, ok := sel.Obj().(*types.Func); ok {
					if matches(sel.Recv(), f.Name()) {
						c <- expr.Sel
					}
				} else {
					panic("unreachable")
				}
			}
		}(pi)

		go func(pi *loader.PackageInfo) {
			defer wg.Done()

			for ident, obj := range pi.Uses {
				// do not include &TypeName{ ... } to simplify results
				if _, isTypeName := obj.(*types.TypeName); isTypeName {
					continue
				}

				if matches(obj.Type(), "") {
					c <- ident
				}
			}
		}(pi)

		go func(pi *loader.PackageInfo) {
			defer wg.Done()

			for ident, obj := range pi.Defs {
				if obj == nil {
					continue
				}
				if matches(obj.Type(), "") {
					c <- ident
				}
			}
		}(pi)

		// find values inside composite literals with values without keys
		// eg:
		//
		//     &ast.Package{pkgName, pkgScope, imports, files}
		//
		// that highlights "imports" for query "go/ast.Package.Imports"
		if selName != "" {
			wg.Add(1)
			go func(pi *loader.PackageInfo) {
				defer wg.Done()

			typeExprs:
				for expr, tv := range pi.Types {
					comp, ok := expr.(*ast.CompositeLit)
					if !ok || len(comp.Elts) == 0 {
						continue
					}

					if !matches(tv.Type, selName) {
						continue
					}

					st, ok := tv.Type.Underlying().(*types.Struct)
					if !ok {
						continue
					}

					_, isKV := comp.Elts[0].(*ast.KeyValueExpr)
					if isKV {
						for _, elt := range comp.Elts {
							kv := elt.(*ast.KeyValueExpr)
							if kv.Key.(*ast.Ident).Name == selName {
								c <- kv.Key
								continue typeExprs
							}
						}
					} else {
						// positioned composite literals like:
						//    Foo{x, y, z}
						// here must hold st.NumFields() == len(comp.Elts)
						for i, elt := range comp.Elts {
							if st.Field(i).Name() == selName {
								c <- elt
								continue typeExprs
							}
						}
					}
				}
			}(pi)
		}
	}

	wg.Wait()

	close(c)

	<-done

	sort.Sort(res)

	fileLines := map[string][][]byte{}
	for _, n := range res.nodes {
		p := conf.Fset.Position(n.Pos())

		lines := fileLines[p.Filename]
		if lines == nil {
			b, err := ioutil.ReadFile(p.Filename)
			if err != nil {
				log.Fatal(err)
			}

			lines = bytes.Split(b, []byte{'\n'})
			fileLines[p.Filename] = lines
		}

		line := lines[p.Line-1]

		var (
			s = p.Column - 1
			t = s + int(n.End()-n.Pos())
		)

		fmt.Printf("%s:%d:%d:%s\x1b[31m%s\x1b[0m%s\n", p.Filename, p.Line, p.Column, line[0:s], line[s:t], line[t:])
	}
}
