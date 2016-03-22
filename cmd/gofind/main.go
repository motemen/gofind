package main

import (
	"fmt"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"strings"

	"go/ast"
	"go/types"

	"golang.org/x/tools/go/loader"
)

func main() {
	target := os.Args[1]

	paths := strings.Split(target, "/")              // {"golang.org","x","tools","go","loader.Config"}
	names := strings.Split(paths[len(paths)-1], ".") // {"loader","Config"}

	pkgPath := strings.Join(append(paths[0:len(paths)-1], names[0]), "/")
	objName := names[1]
	selName := ""
	if len(names) > 2 {
		selName = names[2]
	}

	var conf loader.Config
	_, err := conf.FromArgs(os.Args[2:], false)
	if err != nil {
		log.Fatal(err)
	}

	prog, err := conf.Load()
	if err != nil {
		log.Fatal(err)
	}

	matchPrint := func(node ast.Node, typ types.Type, sel string) {
		if sel != selName {
			return
		}

		prefix := ""
		for {
			if p, ok := typ.(*types.Pointer); ok {
				typ = p.Elem()
				prefix = prefix + "*"
			} else {
				break
			}
		}

		if tn, ok := typ.(*types.Named); ok {
			if tn.Obj().Pkg() == nil {
				// TODO: eg. "error" in universe scope
				return
			}

			if tn.Obj().Pkg().Path() == pkgPath && tn.Obj().Name() == objName {
				p := conf.Fset.Position(node.Pos())
				b, err := ioutil.ReadFile(p.Filename)
				if err != nil {
					log.Fatal(err)
				}

				line := strings.Split(string(b), "\n")[p.Line-1]

				var (
					s = p.Column - 1
					t = s + int(node.End()-node.Pos())
				)

				fmt.Printf("%s:%d:%s\x1b[31m%s\x1b[0m%s\n", filepath.Base(p.Filename), p.Line, line[0:s], line[s:t], line[t:])
			}
		}
	}

	for _, pi := range prog.InitialPackages() {
		log.Println("sel")
		for expr, sel := range pi.Selections {
			if v, ok := sel.Obj().(*types.Var); ok {
				matchPrint(expr.Sel, sel.Recv(), v.Name())
			} else if f, ok := sel.Obj().(*types.Func); ok {
				matchPrint(expr.Sel, sel.Recv(), f.Name())
			} else {
				panic("unreachable")
			}
		}

		log.Println("use")
		for ident, obj := range pi.Uses {
			matchPrint(ident, obj.Type(), "")
		}

		log.Println("def")
		for ident, obj := range pi.Defs {
			if obj == nil {
				continue
			}
			matchPrint(ident, obj.Type(), "")
		}
	}
}
