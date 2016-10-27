// gofind is a comamnd that searches through Go source code by types.
//
// Usage
//
//    gofind [-s] [-q] <pkg>.<name>[.<sel>] <pkg>...
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
//
// TODO(motemen): Find return types
package main

import (
	"bytes"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"go/ast"
	_ "go/importer"
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
	fmt.Fprintf(os.Stderr, "Usage: %s [-s] [-q] <pkg>.<name>[.<sel>] <args>\n", os.Args[0])
	fmt.Fprintf(os.Stderr, `
Options:
`)
	flag.PrintDefaults()
	fmt.Fprintln(os.Stderr, `
Example:

   % gofind -s encoding/json.Encoder.Encode $(go list golang.org/x/...)
   handlers.go:145:        json.NewEncoder(w).Encode(resp)
   socket.go:125:                  if err := enc.Encode(m); err != nil {`)
	fmt.Fprintln(os.Stderr, loader.FromArgsUsage)
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("gofind: ")

	var (
		flagSimple = flag.Bool("s", false, "Print simple filenames")
		flagQuiet  = flag.Bool("q", false, "Do not show errors")
	)

	flag.Usage = usage
	flag.Parse()
	if flag.NArg() < 2 {
		flag.Usage()
		os.Exit(2)
	}

	target := flag.Arg(0)

	paths := strings.Split(target, "/")              // {"golang.org","x","tools","go","loader.Config"}
	names := strings.Split(paths[len(paths)-1], ".") // {"loader","Config"}

	// TODO(motemen): provide filename-only option like "grep -l"

	// Build target to find.
	//
	//   target                          pkgPath          objName    selName
	//   -------------------------------------------------------------------
	//   "net/http.Client"               "net/http"       "Client"   ""
	//   "encoding/json.Encoder.Encode"  "encoding/json"  "Encoder"  "Encode"
	//
	pkgPath := strings.Join(append(paths[0:len(paths)-1], names[0]), "/")
	objName := names[1]
	selName := ""
	if len(names) > 2 {
		selName = names[2]
	}

	// XXX We cannot validate query by Import(), as it seems
	// not able to load "main" package
	/*
		queryPkg, err := importer.Default().Import(pkgPath)
		if err != nil {
			log.Fatal(err)
		}
		if queryObj := queryPkg.Scope().Lookup(objName); queryObj == nil {
			log.Fatalf("package %q does not provide %q", pkgPath, objName)
		}
	*/

	var conf loader.Config
	conf.AllowErrors = true
	conf.TypeChecker.Error = func(_ error) {}

	_, err := conf.FromArgs(flag.Args()[1:], false)
	if err != nil {
		log.Fatal(err)
	}

	prog, err := conf.Load()
	if err != nil {
		log.Fatal(err)
	}

	fieldMatches := func(typ types.Type, sel string) bool {
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
		if len(pi.Errors) != 0 {
			if *flagQuiet == false {
				if len(pi.Errors) == 1 {
					log.Printf("%s: %s", pi.Pkg.Name(), pi.Errors[0])
				} else {
					log.Printf("%s: %s and %d error(s)", pi.Pkg.Name(), pi.Errors[0], len(pi.Errors)-1)
				}
			}
			continue
		}

		// Find selections e.g.
		//
		//   % gofind -s encoding/json.Encoder.Encode golang.org/x/tools/cmd/godoc
		//   handlers.go:146:21:     json.NewEncoder(w).Encode(resp)
		//                                              ^^^^^^
		//
		//   % gofind golang.org/x/tools/cmd/stringer.Package.defs golang.org/x/tools/cmd/stringer
		//   stringer.go:262:6:      pkg.defs = make(map[*ast.Ident]types.Object)
		//                               ^^^^
		//
		wg.Add(1)
		go func(pi *loader.PackageInfo) {
			defer wg.Done()

			for expr, sel := range pi.Selections {
				if v, ok := sel.Obj().(*types.Var); ok {
					if fieldMatches(sel.Recv(), v.Name()) {
						debugf("sel: found %v", expr.Sel)
						c <- expr.Sel
					}
				} else if f, ok := sel.Obj().(*types.Func); ok {
					if fieldMatches(sel.Recv(), f.Name()) {
						debugf("sel: found %v", expr.Sel)
						c <- expr.Sel
					}
				} else {
					panic("unreachable")
				}
			}
		}(pi)

		// Find functions and types e.g.
		//
		//   % gofind -s net/http.ListenAndServe net/http
		//   server.go:2351:16:      return server.ListenAndServe()
		//                                         ^^^^^^^^^^^^^^
		//
		//   % gofind -s net/http.Client net/http
		//   client.go:84:5:var DefaultClient = &Client{}
		//                      ^^^^^^^^^^^^^
		wg.Add(1)
		go func(pi *loader.PackageInfo) {
			defer wg.Done()

			for ident, obj := range pi.Uses {
				// do not include &TypeName{ ... } to simplify results
				if _, isTypeName := obj.(*types.TypeName); isTypeName {
					continue
				} else if funcType, ok := obj.(*types.Func); ok {
					if funcType.Pkg() != nil && funcType.Pkg().Path() == pkgPath && funcType.Name() == objName {
						debugf("use: found %v", ident)
						c <- ident
						continue
					}
				}

				if fieldMatches(obj.Type(), "") {
					debugf("use: found %v", ident)
					c <- ident
				}
			}
		}(pi)

		wg.Add(1)
		go func(pi *loader.PackageInfo) {
			defer wg.Done()

			for ident, obj := range pi.Defs {
				if obj == nil {
					continue
				}
				if fieldMatches(obj.Type(), "") {
					debugf("def: found %v")
					c <- ident
				}
			}
		}(pi)

		// find values inside composite literals with values without keys e.g.:
		//
		//   % gofind -s go/ast.Package.Imports go/ast
		//   resolve.go:173:37:      return &Package{pkgName, pkgScope, imports, files}, p.errors.Err()
		//                                                              ^^^^^^^
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

					if !fieldMatches(tv.Type, selName) {
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
								debugf("field: found %v", kv.Key)
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
								debugf("field: found %v", elt)
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

		filename := p.Filename
		if *flagSimple {
			filename = filepath.Base(filename)
		}
		fmt.Printf("%s:%d:%d:%s\x1b[31m%s\x1b[0m%s\n", filename, p.Line, p.Column, line[0:s], line[s:t], line[t:])
	}
}

var debugMode = os.Getenv("GOFIND_DEBUG") != ""

func debugf(format string, args ...interface{}) {
	if debugMode {
		log.Printf("debug: "+format, args...)
	}
}
