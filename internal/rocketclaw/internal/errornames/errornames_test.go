package errornames

import (
	"go/ast"
	"go/types"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/tools/go/packages"
)

func TestErrorNamesFollowTheGoWiki(t *testing.T) {
	t.Parallel()

	pkgs, err := packages.Load(&packages.Config{Context: t.Context(), Mode: packages.NeedName | packages.NeedImports | packages.NeedDeps | packages.NeedTypes | packages.NeedTypesInfo | packages.NeedSyntax, Tests: true}, "github.com/Rocketable/platform/...")
	require.NoError(t, err)

	errorType := types.Universe.Lookup("error").Type()
	errorInterface := errorType.Underlying().(*types.Interface)
	implementsError := func(candidate types.Type) bool {
		return types.Implements(candidate, errorInterface) || types.Implements(types.NewPointer(candidate), errorInterface)
	}

	var misnamed []string

	for _, pkg := range pkgs {
		require.Empty(t, pkg.Errors, pkg.PkgPath)

		generated := map[string]bool{}
		for _, file := range pkg.Syntax {
			generated[pkg.Fset.File(file.Pos()).Name()] = ast.IsGenerated(file)
		}

		for id, obj := range pkg.TypesInfo.Defs {
			position := pkg.Fset.Position(id.Pos())
			if obj == nil || id.Name == "_" || generated[position.Filename] {
				continue
			}

			switch obj := obj.(type) {
			case *types.TypeName:
				_, isInterface := obj.Type().Underlying().(*types.Interface)
				_, isTypeParam := obj.Type().(*types.TypeParam)

				if !isInterface && !isTypeParam && implementsError(obj.Type()) && !strings.HasSuffix(obj.Name(), "Error") {
					misnamed = append(misnamed, position.String()+": error type "+obj.Name()+" must end in Error")
				}
			case *types.Var:
				packageLevel := obj.Parent() == obj.Pkg().Scope()
				isErrorVariable := types.Identical(obj.Type(), errorType) || packageLevel && implementsError(obj.Type())

				if !obj.IsField() && isErrorVariable && !strings.HasPrefix(obj.Name(), "err") && !strings.HasPrefix(obj.Name(), "Err") {
					misnamed = append(misnamed, position.String()+": error variable "+obj.Name()+" must start with err or Err")
				}
			}
		}
	}

	slices.Sort(misnamed)
	require.Empty(t, slices.Compact(misnamed), "https://go.dev/wiki/Errors#naming")
}
