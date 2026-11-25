package errornames

import (
	"fmt"
	"go/ast"
	"go/importer"
	"go/parser"
	"go/token"
	"go/types"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sync/errgroup"
	"golang.org/x/tools/go/packages"
)

func TestErrorNamesFollowTheGoWiki(t *testing.T) {
	t.Parallel()

	pkgs, err := packages.Load(&packages.Config{Context: t.Context(), Mode: packages.NeedName | packages.NeedCompiledGoFiles | packages.NeedImports | packages.NeedDeps | packages.NeedExportFile, Tests: true}, "github.com/Rocketable/platform/...")
	require.NoError(t, err)

	found := make([][]string, len(pkgs))

	var group errgroup.Group

	group.SetLimit(runtime.GOMAXPROCS(0))

	for i, pkg := range pkgs {
		require.Empty(t, pkg.Errors, pkg.PkgPath)

		group.Go(func() error {
			misnamed, errCheck := misnamedErrors(pkg)
			found[i] = misnamed

			return errCheck
		})
	}

	require.NoError(t, group.Wait())

	misnamed := slices.Concat(found...)
	slices.Sort(misnamed)
	require.Empty(t, slices.Compact(misnamed), "https://go.dev/wiki/Errors#naming")
}

func misnamedErrors(pkg *packages.Package) ([]string, error) {
	fset := token.NewFileSet()

	var files []*ast.File

	for _, name := range pkg.CompiledGoFiles {
		file, err := parser.ParseFile(fset, name, nil, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", name, err)
		}

		if !ast.IsGenerated(file) {
			files = append(files, file)
		}
	}

	imports := importer.ForCompiler(fset, "gc", func(path string) (io.ReadCloser, error) {
		return os.Open(pkg.Imports[path].ExportFile)
	})
	info := &types.Info{Defs: map[*ast.Ident]types.Object{}}

	if _, err := (&types.Config{Importer: imports}).Check(pkg.PkgPath, fset, files, info); err != nil {
		return nil, fmt.Errorf("type-check %s: %w", pkg.ID, err)
	}

	errorType := types.Universe.Lookup("error").Type()
	errorInterface := errorType.Underlying().(*types.Interface)
	implementsError := func(candidate types.Type) bool {
		return types.Implements(candidate, errorInterface) || types.Implements(types.NewPointer(candidate), errorInterface)
	}

	var misnamed []string

	for id, obj := range info.Defs {
		if obj == nil || id.Name == "_" {
			continue
		}

		position := fset.Position(id.Pos()).String()

		switch obj := obj.(type) {
		case *types.TypeName:
			_, isInterface := obj.Type().Underlying().(*types.Interface)
			_, isTypeParam := obj.Type().(*types.TypeParam)

			if !isInterface && !isTypeParam && implementsError(obj.Type()) && !strings.HasSuffix(obj.Name(), "Error") {
				misnamed = append(misnamed, position+": error type "+obj.Name()+" must end in Error")
			}
		case *types.Var:
			packageLevel := obj.Parent() == obj.Pkg().Scope()
			isErrorVariable := types.Identical(obj.Type(), errorType) || packageLevel && implementsError(obj.Type())

			if !obj.IsField() && isErrorVariable && !strings.HasPrefix(obj.Name(), "err") && !strings.HasPrefix(obj.Name(), "Err") {
				misnamed = append(misnamed, position+": error variable "+obj.Name()+" must start with err or Err")
			}
		}
	}

	return misnamed, nil
}
